package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"
	"sync"
	"time"

	// Blank-import the MySQL driver here so any caller of this package
	// gets the "mysql" driver registered without relying on a transitive
	// import from the reconciler or tls packages.
	_ "github.com/go-sql-driver/mysql"

	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
	"github.com/pkg/errors"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
)

const (
	errNotSupported = "%s not supported by mysql client"
)

// ConnectionPoolConfig contains optional connection-pool tuning for the
// MySQL client. A nil ConnectionPoolConfig preserves go's database/sql
// defaults.
//
// The fields map directly to *sql.DB methods of the same name; see the
// Go standard library documentation for semantics.
type ConnectionPoolConfig struct {
	// MaxOpenConns bounds simultaneous in-use connections per pool.
	// 0 leaves the limit unbounded (Go default).
	MaxOpenConns int
	// MaxIdleConns bounds idle pool size. 0 uses Go's default (2).
	// Negative values disable idle connection retention entirely.
	MaxIdleConns int
	// ConnMaxLifetime caps how long a connection may be reused. 0
	// allows connections to live forever (Go default). Useful behind
	// load balancers and connection-pooling proxies that close
	// long-lived connections from their side.
	ConnMaxLifetime time.Duration
	// ConnMaxIdleTime caps how long a connection may sit idle in the
	// pool before being closed. 0 allows idle connections to live
	// forever (Go default).
	ConnMaxIdleTime time.Duration
	// DialTimeout bounds the TCP connect and TLS handshake that
	// open new connections to the database. 0 leaves the
	// go-sql-driver default (no timeout) in place — recommended to
	// override behind any proxy that can hang on connect.
	DialTimeout time.Duration
}

// poolCache holds *sql.DB instances keyed by DSN. The crossplane
// reconciler invokes mysql.New on every Connect() call, and *sql.DB
// is itself a connection pool — opening a new one per reconcile (the
// pre-cache behavior) defeats the Go documentation guarantee that
// "Open function should be called just once" (database/sql docs,
// referenced in upstream issue #110). Caching by DSN means a
// credential rotation lands a fresh pool; the now-orphaned previous
// pool eventually drops its idle connections per ConnMaxIdleTime and
// is later eligible for collection if the entry is evicted.
//
// The cache lives for the lifetime of the provider process. There is
// no explicit eviction — DSN cardinality is bounded by the number of
// distinct ProviderConfig credential rotations, which is small in
// practice.
var (
	poolCacheMu sync.Mutex
	poolCache   = map[string]*sql.DB{}
)

// getOrOpenPool returns the cached *sql.DB for the DSN, opening and
// configuring one on first use.
func getOrOpenPool(dsn string, cfg *ConnectionPoolConfig) (*sql.DB, error) {
	poolCacheMu.Lock()
	defer poolCacheMu.Unlock()
	if db, ok := poolCache[dsn]; ok {
		return db, nil
	}
	db, err := sql.Open("mysql", dsn)
	if err != nil {
		return nil, err
	}
	if cfg != nil {
		if cfg.MaxOpenConns != 0 {
			db.SetMaxOpenConns(cfg.MaxOpenConns)
		}
		if cfg.MaxIdleConns != 0 {
			db.SetMaxIdleConns(cfg.MaxIdleConns)
		}
		if cfg.ConnMaxLifetime > 0 {
			db.SetConnMaxLifetime(cfg.ConnMaxLifetime)
		}
		if cfg.ConnMaxIdleTime > 0 {
			db.SetConnMaxIdleTime(cfg.ConnMaxIdleTime)
		}
	}
	poolCache[dsn] = db
	return db, nil
}

// NewConnectionPoolConfig builds a ConnectionPoolConfig from plain
// values. Returns nil if every input is zero, which preserves the
// "no pool tuning" path in NewWithConfig (Go defaults apply).
// Convenience constructor for reconcilers — they unwrap the optional
// fields from ProviderConfigSpec.ConnectionPool via
// ConnectionPoolSpec.ToPoolValues.
func NewConnectionPoolConfig(maxOpen, maxIdle int, lifetime, idleTime, dialTimeout time.Duration) *ConnectionPoolConfig {
	if maxOpen == 0 && maxIdle == 0 && lifetime == 0 && idleTime == 0 && dialTimeout == 0 {
		return nil
	}
	return &ConnectionPoolConfig{
		MaxOpenConns:    maxOpen,
		MaxIdleConns:    maxIdle,
		ConnMaxLifetime: lifetime,
		ConnMaxIdleTime: idleTime,
		DialTimeout:     dialTimeout,
	}
}

// resetPoolCacheForTest is exported only for tests to ensure cache
// state is deterministic across test runs. Not part of the public API.
func resetPoolCacheForTest() {
	poolCacheMu.Lock()
	defer poolCacheMu.Unlock()
	for dsn, db := range poolCache {
		_ = db.Close()
		delete(poolCache, dsn)
	}
}

type mySQLDB struct {
	db       *sql.DB
	openErr  error // sticky error from initial pool open, returned by Exec/Query/Scan
	dsn      string
	endpoint string
	port     string
	tls      string
}

// New returns a MySQL database client. Equivalent to NewWithConfig with
// a nil config. Preserved for backward compatibility with existing
// callers; new code should prefer NewWithConfig.
func New(creds map[string][]byte, tls *string, binlog *bool) xsql.DB {
	return NewWithConfig(creds, tls, binlog, nil)
}

// NewWithConfig returns a MySQL database client backed by a process-wide
// connection pool keyed by DSN. Multiple calls with equivalent
// credentials share a *sql.DB instance, so reconciles across resources
// amortize connection cost rather than opening a fresh TCP+TLS+MySQL
// handshake per call.
//
// A nil cfg preserves Go's database/sql default pool settings
// (unlimited open connections, 2 idle connections, no max lifetime,
// no dial timeout). Provide a ConnectionPoolConfig to bound the pool
// — see upstream issues #110, #195, #220 for the failure modes the
// defaults can cause under load.
func NewWithConfig(creds map[string][]byte, tls *string, binlog *bool, cfg *ConnectionPoolConfig) xsql.DB {
	endpoint := string(creds[xpv1.ResourceCredentialsSecretEndpointKey])
	port := string(creds[xpv1.ResourceCredentialsSecretPortKey])
	username := string(creds[xpv1.ResourceCredentialsSecretUserKey])
	password := string(creds[xpv1.ResourceCredentialsSecretPasswordKey])
	if tls == nil {
		defaultTLS := "preferred"
		tls = &defaultTLS
	}
	var dialTimeout time.Duration
	if cfg != nil {
		dialTimeout = cfg.DialTimeout
	}
	dsn := dsnWithTimeout(username, password, endpoint, port, *tls, binlog, dialTimeout)

	db, err := getOrOpenPool(dsn, cfg)
	return mySQLDB{
		db:       db,
		openErr:  err,
		dsn:      dsn,
		endpoint: endpoint,
		port:     port,
		tls:      *tls,
	}
}

// DSN returns the DSN URL with no dial timeout. Preserved for backward
// compatibility; new code should call DSNWithDialTimeout.
func DSN(username, password, endpoint, port, tls string, binlog *bool) string {
	return dsnWithTimeout(username, password, endpoint, port, tls, binlog, 0)
}

// DSNWithDialTimeout returns the DSN URL with an optional dial timeout
// appended as the go-sql-driver `timeout` parameter. A zero or negative
// dialTimeout omits the parameter, leaving the driver default (no
// timeout) in place.
func DSNWithDialTimeout(username, password, endpoint, port, tls string, binlog *bool, dialTimeout time.Duration) string {
	return dsnWithTimeout(username, password, endpoint, port, tls, binlog, dialTimeout)
}

func dsnWithTimeout(username, password, endpoint, port, tls string, binlog *bool, dialTimeout time.Duration) string {
	var extra []string
	if binlog != nil {
		extra = append(extra, "sql_log_bin="+strconv.FormatBool(*binlog))
	}
	if dialTimeout > 0 {
		// go-sql-driver accepts Go duration strings (e.g. "10s", "1m")
		// and turns them into the underlying net.Dialer Timeout.
		extra = append(extra, "timeout="+dialTimeout.String())
	}
	base := fmt.Sprintf("%s:%s@tcp(%s:%s)/?tls=%s",
		username, password, endpoint, port, tls)
	for _, p := range extra {
		base += "&" + p
	}
	return base
}

// ExecTx is unsupported in MySQL.
func (c mySQLDB) ExecTx(ctx context.Context, ql []xsql.Query) error {
	return errors.Errorf(errNotSupported, "transactions")
}

// Exec the supplied query.
func (c mySQLDB) Exec(ctx context.Context, q xsql.Query) error {
	if c.openErr != nil {
		return c.openErr
	}
	_, err := c.db.ExecContext(ctx, q.String, q.Parameters...)
	return err
}

// Query the supplied query.
func (c mySQLDB) Query(ctx context.Context, q xsql.Query) (*sql.Rows, error) {
	if c.openErr != nil {
		return nil, c.openErr
	}
	return c.db.QueryContext(ctx, q.String, q.Parameters...)
}

// Scan the results of the supplied query into the supplied destination.
func (c mySQLDB) Scan(ctx context.Context, q xsql.Query, dest ...interface{}) error {
	if c.openErr != nil {
		return c.openErr
	}
	return c.db.QueryRowContext(ctx, q.String, q.Parameters...).Scan(dest...)
}

// GetConnectionDetails returns the connection details for a user of this DB
func (c mySQLDB) GetConnectionDetails(username, password string) managed.ConnectionDetails {
	return managed.ConnectionDetails{
		xpv1.ResourceCredentialsSecretUserKey:     []byte(username),
		xpv1.ResourceCredentialsSecretPasswordKey: []byte(password),
		xpv1.ResourceCredentialsSecretEndpointKey: []byte(c.endpoint),
		xpv1.ResourceCredentialsSecretPortKey:     []byte(c.port),
	}
}

// GetServerVersion is not supported by the MySQL client (only used by PostgreSQL).
func (c mySQLDB) GetServerVersion(ctx context.Context) (int, error) {
	// This method should never be called for MySQL clients
	// but is implemented to satisfy the xsql.DB interface
	return 0, nil
}

// QuoteIdentifier for MySQL queries
func QuoteIdentifier(id string) string {
	return "`" + strings.ReplaceAll(id, "`", "``") + "`"
}

// QuoteValue for MySQL queries
func QuoteValue(id string) string {
	return "'" + strings.ReplaceAll(id, "'", "''") + "'"
}

// SplitUserHost splits a MySQL user by name and host
func SplitUserHost(user string) (username, host string) {
	username = user
	host = "%"
	if strings.Contains(user, "@") {
		parts := strings.SplitN(user, "@", 2)
		username = parts[0]
		host = parts[1]
	}
	return username, host
}

// ExecQuery declares the query to execute and its error value if it fails
type ExecQuery struct {
	// Query defines the sql statement to execute
	Query string
	// ErrorValue defines what error will be returned if the provided sql statement failed when executing
	ErrorValue string
}

// ExecWrapper is a wrapper function for xsql.DB.Exec() that allows the execution of optional queries before and after the provided query
func ExecWrapper(ctx context.Context, db xsql.DB, query ExecQuery) error {
	if err := db.Exec(ctx, xsql.Query{
		String: query.Query,
	}); err != nil {
		return errors.Wrap(err, query.ErrorValue)
	}

	return nil
}
