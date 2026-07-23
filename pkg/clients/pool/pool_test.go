/*
Copyright 2020 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package pool

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"testing"
	"time"
)

// fakeConnector yields a non-nil *sql.DB via sql.OpenDB without needing a
// registered driver. The tests never actually query, so Connect/Open only
// need to exist; the resulting *sql.DB can be cached, compared by pointer, and
// Closed cleanly.
type fakeConnector struct{}

func (fakeConnector) Connect(context.Context) (driver.Conn, error) { return nil, errors.New("fake") }
func (fakeConnector) Driver() driver.Driver                        { return fakeDriver{} }

type fakeDriver struct{}

func (fakeDriver) Open(string) (driver.Conn, error) { return nil, errors.New("fake") }

// newFakeDB returns a fresh, non-nil, closeable *sql.DB.
func newFakeDB() *sql.DB { return sql.OpenDB(fakeConnector{}) }

// realOpenDB is the production opener, restored after each test.
func realOpenDB(driverName, dsn string, cfg Config) (*sql.DB, error) {
	db, err := sql.Open(driverName, dsn)
	if err != nil {
		return nil, err
	}
	cfg.apply(db)
	return db, nil
}

// resetCache clears package state between tests and restores the real clock
// and opener stub afterwards.
func resetCache(t *testing.T) {
	t.Helper()
	mu.Lock()
	cache = map[string]*entry{}
	mu.Unlock()
	openDB = func(_, _ string, _ Config) (*sql.DB, error) {
		return newFakeDB(), nil
	}
	now = time.Now
	t.Cleanup(func() {
		mu.Lock()
		for k, e := range cache {
			if e.db != nil {
				_ = e.db.Close()
			}
			delete(cache, k)
		}
		mu.Unlock()
		openDB = realOpenDB
		now = time.Now
	})
}

func TestGetReusesSamePool(t *testing.T) {
	resetCache(t)

	a, err := Get("mysql", "dsn-1", Default)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	b, err := Get("mysql", "dsn-1", Default)
	if err != nil {
		t.Fatalf("Get: %v", err)
	}
	if a != b {
		t.Errorf("expected the same *sql.DB for the same DSN, got two different pools")
	}
}

func TestGetDistinctPoolsPerDSN(t *testing.T) {
	resetCache(t)

	a, _ := Get("mysql", "dsn-1", Default)
	b, _ := Get("mysql", "dsn-2", Default)
	if a == b {
		t.Errorf("expected distinct pools for distinct DSNs")
	}
	if len(cache) != 2 {
		t.Errorf("expected 2 cached pools, got %d", len(cache))
	}
}

func TestGetDistinctPoolsPerDriver(t *testing.T) {
	resetCache(t)

	a, _ := Get("mysql", "same-dsn", Default)
	b, _ := Get("postgres", "same-dsn", Default)
	if a == b {
		t.Errorf("expected distinct pools when the driver differs for the same DSN")
	}
}

func TestIdleSweepEvictsStalePools(t *testing.T) {
	resetCache(t)

	base := time.Unix(0, 0)
	now = func() time.Time { return base }

	first, _ := Get("mysql", "dsn-1", Default)
	if len(cache) != 1 {
		t.Fatalf("expected 1 cached pool, got %d", len(cache))
	}

	// Advance the clock beyond idleTTL, then touch a different DSN. The stale
	// entry for dsn-1 must be swept.
	now = func() time.Time { return base.Add(idleTTL + time.Minute) }
	if _, err := Get("mysql", "dsn-2", Default); err != nil {
		t.Fatalf("Get: %v", err)
	}
	if _, ok := cache["mysql\x00dsn-1"]; ok {
		t.Errorf("stale pool for dsn-1 was not swept")
	}

	// A fresh Get for dsn-1 must now yield a new pool, not the evicted one.
	again, _ := Get("mysql", "dsn-1", Default)
	if again == first {
		t.Errorf("expected a new pool after eviction, got the evicted one")
	}
}

func TestGetPropagatesOpenError(t *testing.T) {
	resetCache(t)

	wantErr := errors.New("boom")
	openDB = func(_, _ string, _ Config) (*sql.DB, error) {
		return nil, wantErr
	}

	db, err := Get("mysql", "dsn-1", Default)
	if !errors.Is(err, wantErr) {
		t.Errorf("Get: got err %v, want %v", err, wantErr)
	}
	if db != nil {
		t.Errorf("Get: expected a nil *sql.DB on error, got %v", db)
	}
	if _, ok := cache["mysql\x00dsn-1"]; ok {
		t.Errorf("a failed open must not be cached")
	}
}

func TestConfigApply(t *testing.T) {
	db := newFakeDB()
	defer db.Close() //nolint:errcheck // test cleanup only; the fake driver never errors on Close.

	cfg := Config{
		MaxOpenConns:    7,
		MaxIdleConns:    3,
		ConnMaxLifetime: 42 * time.Minute,
		ConnMaxIdleTime: 5 * time.Minute,
	}
	cfg.apply(db)

	// sql.DBStats only surfaces MaxOpenConnections directly, so that field is
	// value-verified. The other three setters (SetMaxIdleConns,
	// SetConnMaxLifetime, SetConnMaxIdleTime) have no equivalent public
	// getter; calling apply without panicking is the extent to which they can
	// be smoke-tested here.
	if got := db.Stats().MaxOpenConnections; got != cfg.MaxOpenConns {
		t.Errorf("apply: MaxOpenConnections = %d, want %d", got, cfg.MaxOpenConns)
	}
}

func TestActivePoolNotSwept(t *testing.T) {
	resetCache(t)

	base := time.Unix(0, 0)
	now = func() time.Time { return base }
	a, _ := Get("mysql", "dsn-1", Default)

	// Keep using it as the clock advances past idleTTL between calls; each Get
	// refreshes lastUsed so it must survive.
	for i := 1; i <= 5; i++ {
		now = func() time.Time { return base.Add(time.Duration(i) * (idleTTL - time.Minute)) }
		b, _ := Get("mysql", "dsn-1", Default)
		if a != b {
			t.Fatalf("active pool was evicted at iteration %d", i)
		}
	}
}
