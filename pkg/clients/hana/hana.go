package hana

import (
	"context"
	"database/sql"
	"log"

	// Blank import as specified by the driver
	_ "github.com/SAP/go-hdb/driver"
	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"

	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
)

type hanaDB struct {
	dsn      string
	endpoint string
	port     string
}

// New returns a new DB client
func New(creds map[string][]byte) xsql.DB {

	endpoint := string(creds[xpv1.ResourceCredentialsSecretEndpointKey])
	port := string(creds[xpv1.ResourceCredentialsSecretPortKey])
	username := string(creds[xpv1.ResourceCredentialsSecretUserKey])
	password := string(creds[xpv1.ResourceCredentialsSecretPasswordKey])
	dsn := DSN(username, password, endpoint, port)

	return hanaDB{
		dsn:      dsn,
		endpoint: endpoint,
		port:     port,
	}
}

// DSN returns a DSN string for the HANA DB connection
func DSN(username string, password string, endpoint string, port string) string {
	return "hdb://" +
		username + ":" +
		password + "@" +
		endpoint + ":" +
		port + "?TLSServerName=" +
		endpoint
}

// Exec executes a query
func (h hanaDB) Exec(ctx context.Context, q xsql.Query) error {
	db, err := sql.Open("hdb", h.dsn)
	if err != nil {
		log.Fatal(err)
	}
	defer db.Close() //nolint:errcheck

	_, err = db.ExecContext(ctx, q.String, q.Parameters...)
	return err
}

// ExecTx executes a transaction
func (h hanaDB) ExecTx(ctx context.Context, ql []xsql.Query) error {
	db, err := sql.Open("hdb", h.dsn)
	if err != nil {
		return err
	}

	tx, err := db.BeginTx(ctx, nil)
	if err != nil {
		return err
	}

	// Rollback or Commit based on error state. Defer close in defer to make
	// sure the connection is always closed.
	defer func() {
		defer db.Close() //nolint:errcheck
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

// Scan scans a query
func (h hanaDB) Scan(ctx context.Context, q xsql.Query, dest ...interface{}) error {
	db, err := sql.Open("hdb", h.dsn)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck

	return db.QueryRowContext(ctx, q.String, q.Parameters...).Scan(dest...)
}

// Query queries a query
func (h hanaDB) Query(ctx context.Context, q xsql.Query) (*sql.Rows, error) {
	db, err := sql.Open("hdb", h.dsn)
	if err != nil {
		return nil, err
	}
	defer db.Close() //nolint:errcheck

	rows, err := db.QueryContext(ctx, q.String, q.Parameters...)
	return rows, err
}

// GetConnectionDetails returns the connection details
func (h hanaDB) GetConnectionDetails(username, password string) managed.ConnectionDetails {
	return managed.ConnectionDetails{
		xpv1.ResourceCredentialsSecretUserKey:     []byte(username),
		xpv1.ResourceCredentialsSecretPasswordKey: []byte(password),
		xpv1.ResourceCredentialsSecretEndpointKey: []byte(h.endpoint),
		xpv1.ResourceCredentialsSecretPortKey:     []byte(h.port),
	}
}

// QueryClient defines the methods for a query client
type QueryClient[P any, O any] interface {
	Read(ctx context.Context, parameters *P) (observed *O, err error)
	Create(ctx context.Context, parameters *P, args ...any) error
	Delete(ctx context.Context, parameters *P) error
}
