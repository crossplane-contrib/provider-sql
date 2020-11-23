package xsql

import (
	"context"
	"errors"

	"database/sql"

	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
)

// A Query that may be run against a DB.
type Query struct {
	String     string
	Parameters []interface{}
}

// A DB client.
type DB interface {
	Exec(ctx context.Context, q Query) error
	Scan(ctx context.Context, q Query, dest ...interface{}) error
	GetConnectionDetails(username, password string) managed.ConnectionDetails
}

// IsNoRows returns true if the supplied error indicates no rows were returned.
func IsNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}
