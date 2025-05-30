package postgresql

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"time"

	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
	"github.com/lib/pq"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
)

const (
	// https://www.postgresql.org/docs/current/errcodes-appendix.html
	// These are not available as part of the pq library.
	pqInvalidCatalog = pq.ErrorCode("3D000")
)

type postgresDB struct {
	db       *sql.DB
	err      error
	dsn      string
	endpoint string
	port     string
	sslmode  string
}

// New returns a new PostgreSQL database client. The default database name is
// an empty string. The underlying pq library will default to either using the
// value of PGDATABASE, or if unset, the hardcoded string 'postgres'.
// The sslmode defines the mode used to set up the connection for the provider.
func New(creds map[string][]byte, database, sslmode string) xsql.DB {
	// TODO(negz): Support alternative connection secret formats?
	endpoint := string(creds[xpv1.ResourceCredentialsSecretEndpointKey])
	port := string(creds[xpv1.ResourceCredentialsSecretPortKey])
	username := string(creds[xpv1.ResourceCredentialsSecretUserKey])
	password := string(creds[xpv1.ResourceCredentialsSecretPasswordKey])
	dsn := DSN(username, password, endpoint, port, database, sslmode)

	db, err := openDB(dsn, true)

	return postgresDB{
		db:       db,
		err:      err,
		dsn:      dsn,
		endpoint: endpoint,
		port:     port,
		sslmode:  sslmode,
	}
}

// openDB returns a new database connection
func openDB(dsn string, setLimits bool) (*sql.DB, error) {
	db, err := sql.Open("postgres", dsn)
	if err != nil {
		return nil, err
	}

	// Since we are now using connection pooling, establish some sensible defaults for connections
	// Ideally these parameters would be set in the config section for the provider, but that
	// can be deferred to a later time.
	if setLimits {
		db.SetMaxOpenConns(5)
		db.SetMaxIdleConns(2)
		db.SetConnMaxIdleTime(2 * time.Minute)
		db.SetConnMaxLifetime(10 * time.Minute)
	}

	ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer cancel()

	err = db.PingContext(ctx)
	if err != nil {
		return nil, err
	}

	return db, nil
}

// DSN returns the DSN URL
func DSN(username, password, endpoint, port, database, sslmode string) string {
	// Use net/url UserPassword to encode the username and password
	// This will ensure that any special characters in the username or password
	// are percent-encoded for use in the user info portion of the DSN URL
	userInfo := url.UserPassword(username, password)
	return "postgres://" +
		userInfo.String() + "@" +
		endpoint + ":" +
		port + "/" +
		database +
		"?sslmode=" + sslmode
}

// ExecTx executes an array of queries, committing if all are successful and
// rolling back immediately on failure.
func (c postgresDB) ExecTx(ctx context.Context, ql []xsql.Query) error {
	if c.db == nil || c.err != nil {
		return c.err
	}

	err := c.db.PingContext(ctx)
	if err != nil {
		return err
	}

	tx, err := c.db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	// Rollback or Commit based on error state. Defer close in defer to make
	// sure the connection is always closed.
	defer func() {
		if err != nil {
			tx.Rollback() //nolint:errcheck
			return
		}
		err = tx.Commit()
	}()

	for _, q := range ql {
		if _, err = tx.Exec(q.String, q.Parameters...); err != nil {
			return err
		}
	}
	return err
}

// Exec the supplied query.
func (c postgresDB) Exec(ctx context.Context, q xsql.Query) error {
	if c.db == nil || c.err != nil {
		return c.err
	}

	err := c.db.PingContext(ctx)
	if err != nil {
		return err
	}

	_, err = c.db.ExecContext(ctx, q.String, q.Parameters...)
	return err
}

// Query the supplied query.
func (c postgresDB) Query(ctx context.Context, q xsql.Query) (*sql.Rows, error) {
	if c.err != nil || c.db == nil {
		return nil, c.err
	}

	err := c.db.PingContext(ctx)
	if err != nil {
		return nil, err
	}

	rows, err := c.db.QueryContext(ctx, q.String, q.Parameters...)
	return rows, err
}

// Scan the results of the supplied query into the supplied destination.
func (c postgresDB) Scan(ctx context.Context, q xsql.Query, dest ...interface{}) error {
	if c.db == nil || c.err != nil {
		return c.err
	}

	err := c.db.PingContext(ctx)
	if err != nil {
		return err
	}

	return c.db.QueryRowContext(ctx, q.String, q.Parameters...).Scan(dest...)
}

// GetConnectionDetails returns the connection details for a user of this DB
func (c postgresDB) GetConnectionDetails(username, password string) managed.ConnectionDetails {
	return managed.ConnectionDetails{
		xpv1.ResourceCredentialsSecretUserKey:     []byte(username),
		xpv1.ResourceCredentialsSecretPasswordKey: []byte(password),
		xpv1.ResourceCredentialsSecretEndpointKey: []byte(c.endpoint),
		xpv1.ResourceCredentialsSecretPortKey:     []byte(c.port),
	}
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
