package postgresql

import (
	"context"
	"database/sql"
	"errors"
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

	// redactedPassword replaces the password in the DSN that error messages show.
	redactedPassword = "xxxxx"
)

type postgresDB struct {
	dsn      string
	redacted string // dsn with the password masked, for error messages
	endpoint string
	port     string
	sslmode  string
}

// New returns a new PostgreSQL database client. The default database name is
// an empty string. The underlying pq library will default to either using the
// value of PGDATABASE, or if unset, the hardcoded string 'postgres'.
// The sslmode defines the mode used to set up the connection for the provider.
func New(creds map[string][]byte, database, sslmode string) xsql.DB {
	endpoint := string(creds[xpv2.CredentialsSecretEndpointKey])
	port := string(creds[xpv2.CredentialsSecretPortKey])
	username := string(creds[xpv2.CredentialsSecretUserKey])
	password := string(creds[xpv2.CredentialsSecretPasswordKey])
	dsn := DSN(username, password, endpoint, port, database, sslmode)

	return postgresDB{
		dsn:      dsn,
		redacted: DSN(username, redactedPassword, endpoint, port, database, sslmode),
		endpoint: endpoint,
		port:     port,
		sslmode:  sslmode,
	}
}

// redact masks the password in DSN parse errors. lib/pq reports a DSN it
// cannot parse as a *url.Error that carries the whole DSN, and the error
// ends up in status conditions and events. Text that wraps the *url.Error
// is kept.
func (c postgresDB) redact(err error) error {
	var uerr *url.Error
	if !errors.As(err, &uerr) {
		return err
	}
	masked := *uerr
	masked.URL = c.redacted
	msg := err.Error()
	if !strings.Contains(msg, uerr.Error()) {
		// The wrapper doesn't print the *url.Error as is, so its text might
		// still carry the password in another form.
		return &masked
	}
	return &redactedError{msg: strings.Replace(msg, uerr.Error(), masked.Error(), 1), err: &masked}
}

// redactedError is an error that wrapped a DSN parse error, with the
// password masked in its message. It unwraps to the masked *url.Error.
type redactedError struct {
	msg string
	err error
}

func (e *redactedError) Error() string { return e.msg }

func (e *redactedError) Unwrap() error { return e.err }

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
	d, err := sql.Open("postgres", c.dsn)
	if err != nil {
		return c.redact(err)
	}

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return c.redact(err)
	}

	// Rollback or Commit based on error state. Defer close in defer to make
	// sure the connection is always closed.
	defer func() {
		defer d.Close() //nolint:errcheck
		// We always rollback, it's a no-op if the tx was already committed.
		defer tx.Rollback() //nolint:errcheck

		if err == nil {
			err = tx.Commit()
		}
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
	d, err := sql.Open("postgres", c.dsn)
	if err != nil {
		return c.redact(err)
	}
	defer d.Close() //nolint:errcheck

	_, err = d.ExecContext(ctx, q.String, q.Parameters...)
	return c.redact(err)
}

// Query the supplied query.
func (c postgresDB) Query(ctx context.Context, q xsql.Query) (*sql.Rows, error) {
	d, err := sql.Open("postgres", c.dsn)
	if err != nil {
		return nil, c.redact(err)
	}
	defer d.Close() //nolint:errcheck

	rows, err := d.QueryContext(ctx, q.String, q.Parameters...)
	return rows, c.redact(err)
}

// Scan the results of the supplied query into the supplied destination.
func (c postgresDB) Scan(ctx context.Context, q xsql.Query, dest ...interface{}) error {
	db, err := sql.Open("postgres", c.dsn)
	if err != nil {
		return c.redact(err)
	}
	defer db.Close() //nolint:errcheck

	return c.redact(db.QueryRowContext(ctx, q.String, q.Parameters...).Scan(dest...))
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
	db, err := sql.Open("postgres", c.dsn)
	if err != nil {
		return 0, c.redact(err)
	}
	defer db.Close() //nolint:errcheck

	var version int
	err = db.QueryRowContext(ctx, "SELECT current_setting('server_version_num')::int").Scan(&version)
	return version, c.redact(err)
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
