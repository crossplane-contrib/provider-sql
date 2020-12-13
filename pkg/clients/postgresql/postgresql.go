package postgresql

import (
	"context"
	"database/sql"

	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
)

type postgresDB struct {
	dsn      string
	endpoint string
	port     string
}

// New returns a new PostgreSQL database client.
func New(creds map[string][]byte) xsql.DB {
	// TODO(negz): Support alternative connection secret formats?
	endpoint := string(creds[xpv1.ResourceCredentialsSecretEndpointKey])
	port := string(creds[xpv1.ResourceCredentialsSecretPortKey])
	return postgresDB{
		dsn: "postgres://" +
			string(creds[xpv1.ResourceCredentialsSecretUserKey]) + ":" +
			string(creds[xpv1.ResourceCredentialsSecretPasswordKey]) + "@" +
			endpoint + ":" +
			port,
		endpoint: endpoint,
		port:     port,
	}
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
		xpv1.ResourceCredentialsSecretUserKey:     []byte(username),
		xpv1.ResourceCredentialsSecretPasswordKey: []byte(password),
		xpv1.ResourceCredentialsSecretEndpointKey: []byte(c.endpoint),
		xpv1.ResourceCredentialsSecretPortKey:     []byte(c.port),
	}
}
