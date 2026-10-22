package mysql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"fmt"
	"strconv"
	"strings"

	driverMySQL "github.com/go-sql-driver/mysql"

	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
	"github.com/pkg/errors"

	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
)

const (
	errNotSupported = "%s not supported by mysql client"
	tlsVerify       = "true"
)

type mySQLDB struct {
	dsn              string
	endpoint         string
	port             string
	tls              string
	passwordProvider xsql.PasswordProvider
}

// New returns a new MySQL database client. When cleartext is true the DSN sets
// allowCleartextPasswords=true, which is required for AWS RDS IAM authentication
// (the AWSAuthenticationPlugin uses MySQL's cleartext client plugin).
func New(creds map[string][]byte, tls *string, binlog *bool, cleartext bool) xsql.DB {
	endpoint := string(creds[xpv2.CredentialsSecretEndpointKey])
	port := string(creds[xpv2.CredentialsSecretPortKey])
	username := string(creds[xpv2.CredentialsSecretUserKey])
	password := string(creds[xpv2.CredentialsSecretPasswordKey])
	if tls == nil {
		defaultTLS := "preferred"
		tls = &defaultTLS
	}
	dsn := DSN(username, password, endpoint, port, *tls, binlog, cleartext)

	return mySQLDB{
		dsn:      dsn,
		endpoint: endpoint,
		port:     port,
		tls:      *tls,
	}
}

// NewWithPasswordProvider configures credentials generated for each physical connection.
func NewWithPasswordProvider(creds map[string][]byte, tls *string, binlog *bool, cleartext bool, password xsql.PasswordProvider) xsql.DB {
	db := New(creds, tls, binlog, cleartext).(mySQLDB)
	db.passwordProvider = password
	return db
}

func (c mySQLDB) open() (*sql.DB, error) {
	if c.passwordProvider == nil {
		return sql.Open("mysql", c.dsn)
	}
	cfg, err := driverMySQL.ParseDSN(c.dsn)
	if err != nil {
		return nil, err
	}
	return sql.OpenDB(&xsql.PasswordConnector{
		Password:  c.passwordProvider,
		SQLDriver: &driverMySQL.MySQLDriver{},
		NewConnector: func(password string) (driver.Connector, error) {
			next := cfg.Clone()
			next.Passwd = password
			return driverMySQL.NewConnector(next)
		},
	}), nil
}

// EnsureTLS selects verified TLS for IAM when mode is unset or permits plaintext.
// Explicit skip-verify and registered custom configurations remain operator choices.
func EnsureTLS(mode *string) *string {
	if mode == nil || *mode == "" || *mode == "false" || *mode == "preferred" {
		verified := tlsVerify
		return &verified
	}
	return mode
}

// DSN returns the DSN URL. When cleartext is true, allowCleartextPasswords=true
// is appended (required for AWS RDS IAM authentication).
func DSN(username, password, endpoint, port, tls string, binlog *bool, cleartext bool) string {
	// MySQL DSN passwords are passed verbatim. URL-encoding would change IAM tokens.
	dsn := fmt.Sprintf("%s:%s@tcp(%s:%s)/?tls=%s", username, password, endpoint, port, tls)
	if binlog != nil {
		dsn += "&sql_log_bin=" + strconv.FormatBool(*binlog)
	}
	if cleartext {
		dsn += "&allowCleartextPasswords=true"
	}
	return dsn
}

// ExecTx is unsupported in MySQL.
func (c mySQLDB) ExecTx(ctx context.Context, ql []xsql.Query) error {
	return errors.Errorf(errNotSupported, "transactions")
}

// Exec the supplied query.
func (c mySQLDB) Exec(ctx context.Context, q xsql.Query) error {
	d, err := c.open()
	if err != nil {
		return err
	}
	defer d.Close() //nolint:errcheck

	_, err = d.ExecContext(ctx, q.String, q.Parameters...)
	return err
}

// Query the supplied query.
func (c mySQLDB) Query(ctx context.Context, q xsql.Query) (*sql.Rows, error) {
	d, err := c.open()
	if err != nil {
		return nil, err
	}
	defer d.Close() //nolint:errcheck

	rows, err := d.QueryContext(ctx, q.String, q.Parameters...)
	return rows, err
}

// Scan the results of the supplied query into the supplied destination.
func (c mySQLDB) Scan(ctx context.Context, q xsql.Query, dest ...interface{}) error {
	db, err := c.open()
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck

	return db.QueryRowContext(ctx, q.String, q.Parameters...).Scan(dest...)
}

// GetConnectionDetails returns the connection details for a user of this DB
func (c mySQLDB) GetConnectionDetails(username, password string) managed.ConnectionDetails {
	return managed.ConnectionDetails{
		xpv2.CredentialsSecretUserKey:     []byte(username),
		xpv2.CredentialsSecretPasswordKey: []byte(password),
		xpv2.CredentialsSecretEndpointKey: []byte(c.endpoint),
		xpv2.CredentialsSecretPortKey:     []byte(c.port),
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
