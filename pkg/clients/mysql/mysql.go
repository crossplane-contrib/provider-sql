package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strconv"
	"strings"

	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
	"github.com/pkg/errors"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
)

const (
	errNotSupported = "%s not supported by mysql client"

	// lockWaitTimeoutSeconds bounds how long a statement waits for a metadata
	// lock before failing server-side. MySQL's default lock_wait_timeout is
	// 31536000s (1 year), so a statement queued behind a lock — e.g. an
	// ALTER USER / GRANT / DROP DATABASE waiting behind a backup, a long
	// transaction, or concurrent DDL — stays pending on the server
	// effectively forever. This is dangerous here because the go-sql-driver
	// cancels a context by closing the TCP socket WITHOUT issuing KILL, so
	// when the reconcile deadline fires the provider abandons the statement
	// but the server thread (and its connection) keeps waiting. Every retry
	// then issues another statement that also blocks, and account-management
	// statements additionally serialize on a single global ACL lock, so
	// blocked statements pile up until the server becomes unresponsive. A
	// short lock_wait_timeout makes such statements fail fast and release
	// instead of accumulating.
	//
	// See docs/mysql-driver-context-cancellation.md for the full analysis.
	lockWaitTimeoutSeconds = 30
	// dialTimeout bounds TCP connection establishment so a reconcile does not
	// block on an unreachable endpoint.
	dialTimeout = "10s"
)

type mySQLDB struct {
	dsn      string
	endpoint string
	port     string
	tls      string
}

// New returns a new MySQL database client.
func New(creds map[string][]byte, tls *string, binlog *bool) xsql.DB {
	endpoint := string(creds[xpv1.ResourceCredentialsSecretEndpointKey])
	port := string(creds[xpv1.ResourceCredentialsSecretPortKey])
	username := string(creds[xpv1.ResourceCredentialsSecretUserKey])
	password := string(creds[xpv1.ResourceCredentialsSecretPasswordKey])
	if tls == nil {
		defaultTLS := "preferred"
		tls = &defaultTLS
	}
	dsn := DSN(username, password, endpoint, port, *tls, binlog)

	return mySQLDB{
		dsn:      dsn,
		endpoint: endpoint,
		port:     port,
		tls:      *tls,
	}
}

// DSN returns the DSN URL
func DSN(username, password, endpoint, port, tls string, binlog *bool) string {
	// Use net/url UserPassword to encode the username and password
	// This will ensure that any special characters in the username or password
	// are percent-encoded for use in the user info portion of the DSN URL

	// lock_wait_timeout and timeout are appended to every connection so that a
	// statement blocked on a metadata lock, or a connection to an unreachable
	// endpoint, fails fast server-side instead of piling up (see the const
	// docs above). lock_wait_timeout is an unrecognised driver param and is
	// therefore issued as `SET lock_wait_timeout = <n>` by go-sql-driver on
	// connect; timeout is the driver's dial timeout.
	params := fmt.Sprintf("tls=%s&lock_wait_timeout=%d&timeout=%s",
		tls,
		lockWaitTimeoutSeconds,
		dialTimeout,
	)
	if binlog != nil {
		params += fmt.Sprintf("&sql_log_bin=%s", strconv.FormatBool(*binlog))
	}
	return fmt.Sprintf("%s:%s@tcp(%s:%s)/?%s",
		username,
		password,
		endpoint,
		port,
		params)
}

// ExecTx is unsupported in MySQL.
func (c mySQLDB) ExecTx(ctx context.Context, ql []xsql.Query) error {
	return errors.Errorf(errNotSupported, "transactions")
}

// Exec the supplied query.
func (c mySQLDB) Exec(ctx context.Context, q xsql.Query) error {
	d, err := sql.Open("mysql", c.dsn)
	if err != nil {
		return err
	}
	defer d.Close() //nolint:errcheck

	_, err = d.ExecContext(ctx, q.String, q.Parameters...)
	return err
}

// Query the supplied query.
func (c mySQLDB) Query(ctx context.Context, q xsql.Query) (*sql.Rows, error) {
	d, err := sql.Open("mysql", c.dsn)
	if err != nil {
		return nil, err
	}
	defer d.Close() //nolint:errcheck

	rows, err := d.QueryContext(ctx, q.String, q.Parameters...)
	return rows, err
}

// Scan the results of the supplied query into the supplied destination.
func (c mySQLDB) Scan(ctx context.Context, q xsql.Query, dest ...interface{}) error {
	db, err := sql.Open("mysql", c.dsn)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck

	return db.QueryRowContext(ctx, q.String, q.Parameters...).Scan(dest...)
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
