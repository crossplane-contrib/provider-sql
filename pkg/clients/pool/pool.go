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

// Package pool provides a process-wide cache of *sql.DB connection pools,
// keyed by driver and DSN, so that the many managed-resource reconciles that
// target the same database server share a single, bounded pool instead of
// opening (and immediately closing) a fresh connection on every query.
//
// The previous behaviour — sql.Open + Close per query — left connection count
// effectively unbounded and could contribute to a database becoming
// unresponsive under load. Reusing a bounded pool caps the connections the
// provider holds against any one server.
package pool

import (
	"database/sql"
	"sync"
	"time"
)

// Config holds the tunable settings applied to a *sql.DB pool. It mirrors the
// database/sql pool knobs. A zero MaxOpenConns means unlimited (the
// database/sql default); a zero duration disables that particular limit.
type Config struct {
	MaxOpenConns    int
	MaxIdleConns    int
	ConnMaxLifetime time.Duration
	ConnMaxIdleTime time.Duration
}

// Default is the pool configuration used when a ProviderConfig does not specify
// one, or for any individual field it leaves unset.
var Default = Config{
	MaxOpenConns:    10,
	MaxIdleConns:    5,
	ConnMaxLifetime: time.Hour,
	ConnMaxIdleTime: 10 * time.Minute,
}

func (c Config) apply(db *sql.DB) {
	db.SetMaxOpenConns(c.MaxOpenConns)
	db.SetMaxIdleConns(c.MaxIdleConns)
	db.SetConnMaxLifetime(c.ConnMaxLifetime)
	db.SetConnMaxIdleTime(c.ConnMaxIdleTime)
}

// idleTTL is how long a pool may go unused before the cache closes and evicts
// it. This bounds the number of *sql.DB objects (and their cleaner goroutines)
// retained after the DSN they served stops being used — e.g. after a password
// rotation changes the DSN. It is deliberately larger than the default
// ConnMaxIdleTime so that idle connections are reaped before the pool itself
// is closed.
const idleTTL = 30 * time.Minute

type entry struct {
	db       *sql.DB
	lastUsed time.Time
}

var (
	mu    sync.Mutex
	cache = map[string]*entry{}
	// now is a package variable so tests can control the clock.
	now = time.Now
	// openDB is a package variable so tests can stub pool creation without a
	// real driver. It must apply cfg to the returned *sql.DB.
	openDB = func(driver, dsn string, cfg Config) (*sql.DB, error) {
		db, err := sql.Open(driver, dsn)
		if err != nil {
			return nil, err
		}
		cfg.apply(db)
		return db, nil
	}
)

// Get returns a shared *sql.DB for the given driver and DSN, creating and
// caching one (with cfg applied) on first use. Callers MUST NOT Close the
// returned pool: it is shared across reconciles and owned by the cache.
//
// cfg is applied only when the pool is first created for a (driver, dsn) pair;
// subsequent callers with a different cfg for the same DSN reuse the existing
// pool. In practice a DSN maps to a single ProviderConfig, so its pool config
// is stable.
func Get(driver, dsn string, cfg Config) (*sql.DB, error) {
	key := driver + "\x00" + dsn

	mu.Lock()
	defer mu.Unlock()

	sweepLocked()

	if e, ok := cache[key]; ok {
		e.lastUsed = now()
		return e.db, nil
	}

	db, err := openDB(driver, dsn, cfg)
	if err != nil {
		return nil, err
	}
	cache[key] = &entry{db: db, lastUsed: now()}
	return db, nil
}

// sweepLocked closes and evicts pools that have gone unused for longer than
// idleTTL. Callers must hold mu.
func sweepLocked() {
	t := now()
	for key, e := range cache {
		if t.Sub(e.lastUsed) > idleTTL {
			_ = e.db.Close()
			delete(cache, key)
		}
	}
}
