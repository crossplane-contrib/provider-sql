package postgresql

import (
	"context"
	"database/sql"
	"errors"
	"net/url"
	"os"

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
	dsn      string
	endpoint string
	port     string
	options  Options
}

type Options struct {
	SSLMode     string
	SSLCert     string
	SSLKey      string
	SSLRootCert string
}

func (o Options) queryString() string {
	values := url.Values{}

	if o.SSLMode != "" {
		values.Add("sslmode", o.SSLMode)
	}

	if o.SSLCert != "" {
		values.Add("sslcert", o.SSLCert)
	}

	if o.SSLKey != "" {
		values.Add("sslkey", o.SSLKey)
	}

	if o.SSLRootCert != "" {
		values.Add("sslrootcert", o.SSLRootCert)
	}

	return values.Encode()
}

func (o *Options) withSecretData(data map[string][]byte) error {
	set := func(key string, to *string) error {
		v, ok := data[key]

		if !ok {
			return nil
		}

		f, err := os.CreateTemp("", key)
		if err != nil {
			return err
		}
		defer f.Close()

		if _, err := f.Write(v); err != nil {
			return err
		}

		*to = f.Name()

		return nil
	}

	if err := set(xpv1.ResourceCredentialsSecretClientCertKey, &o.SSLCert); err != nil {
		return err
	}

	if err := set(xpv1.ResourceCredentialsSecretClientKeyKey, &o.SSLKey); err != nil {
		return err
	}

	if err := set(xpv1.ResourceCredentialsSecretCAKey, &o.SSLRootCert); err != nil {
		return err
	}

	return nil
}

// New returns a new PostgreSQL database client. The default database name is
// an empty string. The underlying pq library will default to either using the
// value of PGDATABASE, or if unset, the hardcoded string 'postgres'.
// The options provide additional settings to set up the connection for the
// provider.
func New(data map[string][]byte, database string, options Options) (xsql.DB, error) {
	// TODO(negz): Support alternative connection secret formats?
	endpoint := string(data[xpv1.ResourceCredentialsSecretEndpointKey])
	port := string(data[xpv1.ResourceCredentialsSecretPortKey])
	username := string(data[xpv1.ResourceCredentialsSecretUserKey])
	password := string(data[xpv1.ResourceCredentialsSecretPasswordKey])

	if err := options.withSecretData(data); err != nil {
		return nil, err
	}

	dsn := DSN(username, password, endpoint, port, database, options.queryString())

	return postgresDB{
		dsn:      dsn,
		endpoint: endpoint,
		port:     port,
		options:  options,
	}, nil
}

// DSN returns the DSN URL
func DSN(username, password, endpoint, port, database, options string) string {
	// Use net/url UserPassword to encode the username and password
	// This will ensure that any special characters in the username or password
	// are percent-encoded for use in the user info portion of the DSN URL
	userInfo := url.UserPassword(username, password)
	return "postgres://" +
		userInfo.String() + "@" +
		endpoint + ":" +
		port + "/" +
		database +
		"?" + options

}

// ExecTx executes an array of queries, committing if all are successful and
// rolling back immediately on failure.
func (c postgresDB) ExecTx(ctx context.Context, ql []xsql.Query) error {
	d, err := sql.Open("postgres", c.dsn)
	if err != nil {
		return err
	}

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	// Rollback or Commit based on error state. Defer close in defer to make
	// sure the connection is always closed.
	defer func() {
		defer d.Close() //nolint:errcheck
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
	d, err := sql.Open("postgres", c.dsn)
	if err != nil {
		return err
	}
	defer d.Close() //nolint:errcheck

	_, err = d.ExecContext(ctx, q.String, q.Parameters...)
	return err
}

// Query the supplied query.
func (c postgresDB) Query(ctx context.Context, q xsql.Query) (*sql.Rows, error) {
	d, err := sql.Open("postgres", c.dsn)
	if err != nil {
		return nil, err
	}
	defer d.Close() //nolint:errcheck

	rows, err := d.QueryContext(ctx, q.String, q.Parameters...)
	return rows, err
}

// Scan the results of the supplied query into the supplied destination.
func (c postgresDB) Scan(ctx context.Context, q xsql.Query, dest ...interface{}) error {
	db, err := sql.Open("postgres", c.dsn)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck

	return db.QueryRowContext(ctx, q.String, q.Parameters...).Scan(dest...)
}

// GetConnectionDetails returns the connection details for a user of this DB
func (c postgresDB) GetConnectionDetails(username, password string) managed.ConnectionDetails {
	return managed.ConnectionDetails{
		xpv1.ResourceCredentialsSecretUserKey:       []byte(username),
		xpv1.ResourceCredentialsSecretPasswordKey:   []byte(password),
		xpv1.ResourceCredentialsSecretEndpointKey:   []byte(c.endpoint),
		xpv1.ResourceCredentialsSecretPortKey:       []byte(c.port),
		xpv1.ResourceCredentialsSecretClientCertKey: []byte(c.options.SSLCert),
		xpv1.ResourceCredentialsSecretClientKeyKey:  []byte(c.options.SSLKey),
		xpv1.ResourceCredentialsSecretCAKey:         []byte(c.options.SSLRootCert),
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
