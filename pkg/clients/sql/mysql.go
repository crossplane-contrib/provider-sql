package sql

import (
	"context"
	"database/sql"
	"fmt"

	runtimev1alpha1 "github.com/crossplane/crossplane-runtime/apis/core/v1alpha1"
	"github.com/go-sql-driver/mysql"
)

// A MySQLDB client.
type MySQLDB struct {
	dsn string
}

// NewMySQLDB returns a new MySQL database client.
func NewMySQLDB(creds map[string][]byte) DB {
	// TODO(negz): Support alternative connection secret formats?
	return MySQLDB{dsn: fmt.Sprintf("%s:%s@tcp(%s:%s)/",
		creds[runtimev1alpha1.ResourceCredentialsSecretUserKey],
		creds[runtimev1alpha1.ResourceCredentialsSecretPasswordKey],
		creds[runtimev1alpha1.ResourceCredentialsSecretEndpointKey],
		creds[runtimev1alpha1.ResourceCredentialsSecretPortKey])}
}

// Exec the supplied query.
func (c MySQLDB) Exec(ctx context.Context, q Query) error {
	d, err := sql.Open("mysql", c.dsn)
	if err != nil {
		return err
	}
	defer d.Close() //nolint:errcheck

	_, err = d.ExecContext(ctx, q.String, q.Parameters...)
	return err
}

// Scan the results of the supplied query into the supplied destination.
func (c MySQLDB) Scan(ctx context.Context, q Query, dest ...interface{}) error {
	db, err := sql.Open("mysql", c.dsn)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck

	return db.QueryRowContext(ctx, q.String, q.Parameters...).Scan(dest...)
}

// IsDoesNotExist returns true if the supplied error indicates a database does
// not exist.
func (c MySQLDB) IsDoesNotExist(err error) bool {
	merr, ok := err.(*mysql.MySQLError)
	if !ok {
		return false
	}
	return merr.Number == 1008 // Can't drop database; database doesn't exist.
}
