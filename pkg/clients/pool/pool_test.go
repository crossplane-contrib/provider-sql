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
	"database/sql"
	"testing"
	"time"
)

// resetCache clears package state between tests and restores the real clock
// and opener stub afterwards.
func resetCache(t *testing.T) {
	t.Helper()
	mu.Lock()
	cache = map[string]*entry{}
	mu.Unlock()
	openDB = func(driver, dsn string, cfg Config) (*sql.DB, error) {
		// A closed *sql.DB is a valid, non-nil handle we can cache and Close
		// without needing a registered driver or a real server.
		db, _ := sql.Open("fakedriver-"+driver, dsn)
		return db, nil
	}
	now = time.Now
	t.Cleanup(func() {
		mu.Lock()
		for k, e := range cache {
			_ = e.db.Close()
			delete(cache, k)
		}
		mu.Unlock()
		openDB = func(driver, dsn string, cfg Config) (*sql.DB, error) {
			db, err := sql.Open(driver, dsn)
			if err != nil {
				return nil, err
			}
			cfg.apply(db)
			return db, nil
		}
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
