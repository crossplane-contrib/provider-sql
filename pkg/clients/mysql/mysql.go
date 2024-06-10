package mysql

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
	"github.com/pkg/errors"
	"k8s.io/utils/ptr"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
)

const (
	errNotSupported = "%s not supported by mysql client"
	errSetSQLLogBin = "cannot set sql_log_bin = 0"
)

type mySQLDB struct {
	dsn      string
	endpoint string
	port     string
	tls      string
}

// New returns a new MySQL database client.
func New(creds map[string][]byte, tls *string) xsql.DB {
	// TODO(negz): Support alternative connection secret formats?
	endpoint := string(creds[xpv1.ResourceCredentialsSecretEndpointKey])
	port := string(creds[xpv1.ResourceCredentialsSecretPortKey])
	username := string(creds[xpv1.ResourceCredentialsSecretUserKey])
	password := string(creds[xpv1.ResourceCredentialsSecretPasswordKey])
	if tls == nil {
		defaultTLS := "preferred"
		tls = &defaultTLS
	}
	dsn := DSN(username, password, endpoint, port, *tls)

	return mySQLDB{
		dsn:      dsn,
		endpoint: endpoint,
		port:     port,
		tls:      *tls,
	}
}

// DSN returns the DSN URL
func DSN(username, password, endpoint, port, tls string) string {
	// Use net/url UserPassword to encode the username and password
	// This will ensure that any special characters in the username or password
	// are percent-encoded for use in the user info portion of the DSN URL
	return fmt.Sprintf("%s:%s@tcp(%s:%s)/?tls=%s",
		username,
		password,
		endpoint,
		port,
		tls)
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

// ExecOptions parametrizes which optional statements will be executed before or after ExecQuery.Query
type ExecOptions struct {
	// Binlog defines whether storing binlogs will be disabled before executing the query. Defaults to true
	Binlog *bool
}

// ExecWrapper is a wrapper function for xsql.DB.Exec() that allows the execution of optional queries before and after the provided query
func ExecWrapper(ctx context.Context, db xsql.DB, query ExecQuery, options ExecOptions) error {
	if options.Binlog == nil {
		options.Binlog = ptr.To(true)
	}

	if !*options.Binlog {
		if err := db.Exec(ctx, xsql.Query{
			String: "SET sql_log_bin = 0",
		}); err != nil {
			return errors.Wrap(err, errSetSQLLogBin)
		}
	}

	if err := db.Exec(ctx, xsql.Query{
		String: query.Query,
	}); err != nil {
		return errors.Wrap(err, query.ErrorValue)
	}

	return nil
}
