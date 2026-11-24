package postgresql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"net"
	"net/url"
	"strings"

	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
	"github.com/lib/pq"
	"github.com/lib/pq/pqerror"

	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
)

const (
	// https://www.postgresql.org/docs/current/errcodes-appendix.html
	// These are not available as part of the pq library.
	pqInvalidCatalog = pqerror.Code("3D000")
)

type postgresDB struct {
	dsn              string
	endpoint         string
	port             string
	sslmode          string
	passwordProvider xsql.PasswordProvider
}

// New returns a new PostgreSQL database client. The default database name is
// an empty string. The underlying pq library will default to either using the
// value of PGDATABASE, or if unset, the hardcoded string 'postgres'.
// The sslmode defines the mode used to set up the connection for the provider.
func New(creds map[string][]byte, database, sslmode string) xsql.DB {
	return newPostgresDB(creds, database, sslmode, nil)
}

// NewWithPasswordProvider configures credentials generated for each physical connection.
func NewWithPasswordProvider(creds map[string][]byte, database, sslmode string, password xsql.PasswordProvider) xsql.DB {
	return newPostgresDB(creds, database, sslmode, password)
}

func newPostgresDB(creds map[string][]byte, database, sslmode string, passwordProvider xsql.PasswordProvider) postgresDB {
	endpoint := string(creds[xpv2.CredentialsSecretEndpointKey])
	port := string(creds[xpv2.CredentialsSecretPortKey])
	username := string(creds[xpv2.CredentialsSecretUserKey])
	password := string(creds[xpv2.CredentialsSecretPasswordKey])
	if passwordProvider != nil {
		// Parse connection settings without retaining a password in the DSN.
		password = ""
	}
	return postgresDB{
		dsn:      DSN(username, password, endpoint, port, database, connectionSSLMode(sslmode)),
		endpoint: endpoint, port: port, sslmode: sslmode, passwordProvider: passwordProvider,
	}
}

func (c postgresDB) open() (*sql.DB, error) {
	cfg, err := pq.NewConfig(c.dsn)
	if err != nil {
		return nil, withoutConnectionURL(err)
	}
	if strings.HasPrefix(c.sslmode, "pqgo-provider-sql-postgresql-") {
		// Apply the selected CA after parsing environment defaults. In particular,
		// PGSSLROOTCERT must not replace or supplement a ProviderConfig's roots.
		cfg.SSLMode = pq.SSLMode(c.sslmode)
		cfg.SSLRootCert = ""
	}
	if c.passwordProvider == nil {
		connector, err := pq.NewConnectorConfig(cfg)
		if err != nil {
			return nil, err
		}
		return sql.OpenDB(connector), nil
	}
	return sql.OpenDB(&xsql.PasswordConnector{
		Password:  c.passwordProvider,
		SQLDriver: &pq.Driver{},
		NewConnector: func(password string) (driver.Connector, error) {
			next := cfg.Clone()
			next.Password = password
			return pq.NewConnectorConfig(next)
		},
	}), nil
}

func connectionSSLMode(mode string) string {
	if strings.HasPrefix(mode, "pqgo-provider-sql-postgresql-") {
		// Parse with a standard verifying mode before applying the registered
		// CA config. This also accepts PGSSLROOTCERT=system without allowing that
		// process-wide setting to override the selected Secret.
		return "verify-full"
	}
	return mode
}

// URL parser errors quote the complete DSN, including its password.
func withoutConnectionURL(err error) error {
	var parseErr *url.Error
	if errors.As(err, &parseErr) {
		return parseErr.Err
	}
	return err
}

// EnsureTLS requires encryption without weakening configured verification.
// Kubernetes defaults an omitted sslMode to verify-full; empty handles callers
// that construct ProviderConfigs without API-server defaulting.
func EnsureTLS(mode string) string {
	if mode == "" || mode == "disable" || mode == "allow" || mode == "prefer" {
		return "require"
	}
	return mode
}

// DSN returns the DSN URL
func DSN(username, password, endpoint, port, database, sslmode string) string {
	// Keep database names and credentials separate from connection options.
	connection := url.URL{
		Scheme:   "postgres",
		User:     url.UserPassword(username, password),
		Host:     net.JoinHostPort(endpoint, port),
		Path:     "/" + database,
		RawQuery: url.Values{"sslmode": {sslmode}}.Encode(),
	}
	return connection.String()
}

// ExecTx executes an array of queries, committing if all are successful and
// rolling back immediately on failure.
func (c postgresDB) ExecTx(ctx context.Context, ql []xsql.Query) error {
	d, err := c.open()
	if err != nil {
		return err
	}

	defer d.Close() //nolint:errcheck

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	// Rollback or Commit based on error state. Defer close in defer to make
	// sure the connection is always closed.
	defer func() {
		// We always rollback, it's a no-op if the tx was already committed.
		defer tx.Rollback() //nolint:errcheck

		if err == nil {
			err = tx.Commit()
		}
	}()

	for _, q := range ql {
		if _, err = tx.ExecContext(ctx, q.String, q.Parameters...); err != nil {
			return err
		}
	}
	return err
}

// Exec the supplied query.
func (c postgresDB) Exec(ctx context.Context, q xsql.Query) error {
	d, err := c.open()
	if err != nil {
		return err
	}
	defer d.Close() //nolint:errcheck

	_, err = d.ExecContext(ctx, q.String, q.Parameters...)
	return err
}

// Query the supplied query.
func (c postgresDB) Query(ctx context.Context, q xsql.Query) (*sql.Rows, error) {
	d, err := c.open()
	if err != nil {
		return nil, err
	}
	defer d.Close() //nolint:errcheck

	rows, err := d.QueryContext(ctx, q.String, q.Parameters...)
	return rows, err
}

// Scan the results of the supplied query into the supplied destination.
func (c postgresDB) Scan(ctx context.Context, q xsql.Query, dest ...interface{}) error {
	db, err := c.open()
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck

	return db.QueryRowContext(ctx, q.String, q.Parameters...).Scan(dest...)
}

// GetConnectionDetails returns the connection details for a user of this DB
func (c postgresDB) GetConnectionDetails(username, password string) managed.ConnectionDetails {
	return managed.ConnectionDetails{
		xpv2.CredentialsSecretUserKey:     []byte(username),
		xpv2.CredentialsSecretPasswordKey: []byte(password),
		xpv2.CredentialsSecretEndpointKey: []byte(c.endpoint),
		xpv2.CredentialsSecretPortKey:     []byte(c.port),
	}
}

// GetServerVersion returns the PostgreSQL server version as an integer
// For example, PostgreSQL 16.2 would return 160200.
func (c postgresDB) GetServerVersion(ctx context.Context) (int, error) {
	db, err := c.open()
	if err != nil {
		return 0, err
	}
	defer db.Close() //nolint:errcheck

	var version int
	err = db.QueryRowContext(ctx, "SELECT current_setting('server_version_num')::int").Scan(&version)
	return version, err
}

// IsInvalidCatalog returns true if passed a pq error indicating
// that the database does not exist.
func IsInvalidCatalog(err error) bool {
	var pqErr *pq.Error
	if errors.As(err, &pqErr) {
		return pqErr.Code == pqInvalidCatalog
	}
	return false
}
