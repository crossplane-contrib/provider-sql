package sql

import (
	"context"
	"database/sql"
	"strings"

	runtimev1alpha1 "github.com/crossplane/crossplane-runtime/apis/core/v1alpha1"
)

// A PostgresDB client.
type PostgresDB struct {
	dsn string
}

// NewPostgresDB returns a new PostgreSQL database client.
func NewPostgresDB(creds map[string][]byte) DB {
	// TODO(negz): Support alternative connection secret formats?
	return PostgresDB{dsn: "postgres://" +
		string(creds[runtimev1alpha1.ResourceCredentialsSecretUserKey]) + ":" +
		string(creds[runtimev1alpha1.ResourceCredentialsSecretPasswordKey]) + "@" +
		string(creds[runtimev1alpha1.ResourceCredentialsSecretEndpointKey]) + ":" +
		string(creds[runtimev1alpha1.ResourceCredentialsSecretPortKey])}
}

// Exec the supplied query.
func (c PostgresDB) Exec(ctx context.Context, q Query) error {
	d, err := sql.Open("postgres", c.dsn)
	if err != nil {
		return err
	}
	defer d.Close() //nolint:errcheck

	_, err = d.ExecContext(ctx, q.String, q.Parameters...)
	return err
}

// Scan the results of the supplied query into the supplied destination.
func (c PostgresDB) Scan(ctx context.Context, q Query, dest ...interface{}) error {
	db, err := sql.Open("postgres", c.dsn)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck

	return db.QueryRowContext(ctx, q.String, q.Parameters...).Scan(dest...)
}

// IsDoesNotExist returns true if the supplied error indicates a database does
// not exist.
func (c PostgresDB) IsDoesNotExist(err error) bool {
	if err == nil {
		return false
	}

	// TODO(negz): Is there a less lame way to determine this?
	return strings.HasSuffix(err.Error(), "does not exist")
}
