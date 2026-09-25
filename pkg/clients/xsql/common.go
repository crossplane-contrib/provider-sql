package xsql

import (
	"context"
	"errors"

	"database/sql"

	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
)

// AuthenticationMode identifies how a SQL client authenticates to the server.
type AuthenticationMode string

const (
	// AuthenticationModePassword uses the username and password from the
	// connection secret.
	AuthenticationModePassword AuthenticationMode = "Password"
	// AuthenticationModeAzureWorkloadIdentity obtains a Microsoft Entra token
	// from the provider pod's federated workload identity.
	AuthenticationModeAzureWorkloadIdentity AuthenticationMode = "AzureWorkloadIdentity"

	credentialKeyAuthentication  = "provider-sql.io/authentication"
	credentialKeyAzureTokenScope = "provider-sql.io/azure-token-scope"
)

// WithAuthentication returns a copy of credentials carrying internal,
// provider-controlled authentication metadata. These keys are never read from
// the user's connection secret.
func WithAuthentication(credentials map[string][]byte, mode AuthenticationMode, azureTokenScope string) map[string][]byte {
	configured := make(map[string][]byte, len(credentials)+2)
	for key, value := range credentials {
		configured[key] = value
	}
	configured[credentialKeyAuthentication] = []byte(mode)
	if azureTokenScope != "" {
		configured[credentialKeyAzureTokenScope] = []byte(azureTokenScope)
	}
	return configured
}

// AuthenticationFromCredentials returns provider-controlled authentication
// metadata attached by the ProviderConfig resolver.
func AuthenticationFromCredentials(credentials map[string][]byte) (AuthenticationMode, string) {
	mode := AuthenticationMode(credentials[credentialKeyAuthentication])
	if mode == "" {
		mode = AuthenticationModePassword
	}
	return mode, string(credentials[credentialKeyAzureTokenScope])
}

// A Query that may be run against a DB.
type Query struct {
	String     string
	Parameters []interface{}
}

// A DB client.
type DB interface {
	Exec(ctx context.Context, q Query) error
	ExecTx(cts context.Context, ql []Query) error
	Scan(ctx context.Context, q Query, dest ...interface{}) error
	Query(ctx context.Context, q Query) (*sql.Rows, error)
	GetConnectionDetails(username, password string) managed.ConnectionDetails
	GetServerVersion(ctx context.Context) (int, error)
}

// IsNoRows returns true if the supplied error indicates no rows were returned.
func IsNoRows(err error) bool {
	return errors.Is(err, sql.ErrNoRows)
}

// RemapCredentialKeys returns a copy of data where standard Crossplane
// connection secret keys ("endpoint", "port", "username", "password")
// are populated from the custom key names defined in mapping.
// Entries with empty values are skipped. A nil or empty mapping returns
// data unchanged.
func RemapCredentialKeys(data map[string][]byte, mapping map[string]string) map[string][]byte {
	if len(mapping) == 0 {
		return data
	}
	remapped := make(map[string][]byte, len(data))
	for k, v := range data {
		remapped[k] = v
	}
	for standardKey, customKey := range mapping {
		if customKey != "" {
			if val, ok := data[customKey]; ok {
				remapped[standardKey] = val
			}
		}
	}
	return remapped
}
