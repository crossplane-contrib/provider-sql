package postgresql

import (
	"context"
	"database/sql"
	"errors"
	"net/url"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/Azure/azure-sdk-for-go/sdk/azidentity"
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

	defaultAzurePostgreSQLTokenScope = "https://ossrdbms-aad.database.windows.net/.default"
)

type postgresDB struct {
	username        string
	password        string
	endpoint        string
	port            string
	database        string
	sslmode         string
	authMode        xsql.AuthenticationMode
	tokenScope      string
	tokenCredential azcore.TokenCredential
	credentialErr   error
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
	authMode, tokenScope := xsql.AuthenticationFromCredentials(creds)

	var tokenCredential azcore.TokenCredential
	var credentialErr error
	if authMode == xsql.AuthenticationModeAzureWorkloadIdentity {
		if tokenScope == "" {
			tokenScope = defaultAzurePostgreSQLTokenScope
		}
		tokenCredential, credentialErr = azidentity.NewWorkloadIdentityCredential(nil)
	}

	return postgresDB{
		username:        username,
		password:        password,
		endpoint:        endpoint,
		port:            port,
		database:        database,
		sslmode:         sslmode,
		authMode:        authMode,
		tokenScope:      tokenScope,
		tokenCredential: tokenCredential,
		credentialErr:   credentialErr,
	}
}

func (c postgresDB) open(ctx context.Context) (*sql.DB, error) {
	dsn, err := c.connectionString(ctx)
	if err != nil {
		return nil, err
	}
	return sql.Open("postgres", dsn)
}

func (c postgresDB) connectionString(ctx context.Context) (string, error) {
	if c.credentialErr != nil {
		return "", c.credentialErr
	}
	password := c.password
	if c.authMode == xsql.AuthenticationModeAzureWorkloadIdentity {
		token, err := c.tokenCredential.GetToken(ctx, policy.TokenRequestOptions{Scopes: []string{c.tokenScope}})
		if err != nil {
			return "", err
		}
		password = token.Token
	}
	return DSN(c.username, password, c.endpoint, c.port, c.database, c.sslmode), nil
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
	d, err := c.open(ctx)
	if err != nil {
		return err
	}

	tx, err := d.BeginTx(ctx, nil)
	if err != nil {
		d.Close() //nolint:errcheck
		return err
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
	d, err := c.open(ctx)
	if err != nil {
		return err
	}
	defer d.Close() //nolint:errcheck

	_, err = d.ExecContext(ctx, q.String, q.Parameters...)
	return err
}

// Query the supplied query.
func (c postgresDB) Query(ctx context.Context, q xsql.Query) (*sql.Rows, error) {
	d, err := c.open(ctx)
	if err != nil {
		return nil, err
	}
	defer d.Close() //nolint:errcheck

	rows, err := d.QueryContext(ctx, q.String, q.Parameters...)
	return rows, err
}

// Scan the results of the supplied query into the supplied destination.
func (c postgresDB) Scan(ctx context.Context, q xsql.Query, dest ...interface{}) error {
	db, err := c.open(ctx)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck

	return db.QueryRowContext(ctx, q.String, q.Parameters...).Scan(dest...)
}

// GetConnectionDetails returns the connection details for a user of this DB
func (c postgresDB) GetConnectionDetails(username, password string) managed.ConnectionDetails {
	details := managed.ConnectionDetails{
		xpv2.CredentialsSecretUserKey:     []byte(username),
		xpv2.CredentialsSecretEndpointKey: []byte(c.endpoint),
		xpv2.CredentialsSecretPortKey:     []byte(c.port),
	}
	if c.authMode == xsql.AuthenticationModeAzureWorkloadIdentity {
		details["authentication"] = []byte(xsql.AuthenticationModeAzureWorkloadIdentity)
	} else {
		details[xpv2.CredentialsSecretPasswordKey] = []byte(password)
	}
	return details
}

// GetServerVersion returns the PostgreSQL server version as an integer
// For example, PostgreSQL 16.2 would return 160200.
func (c postgresDB) GetServerVersion(ctx context.Context) (int, error) {
	db, err := c.open(ctx)
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
