package postgresql

import (
	"context"
	"testing"

	"github.com/Azure/azure-sdk-for-go/sdk/azcore"
	"github.com/Azure/azure-sdk-for-go/sdk/azcore/policy"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
)

type fakeTokenCredential struct {
	token  string
	scopes []string
}

func (f *fakeTokenCredential) GetToken(_ context.Context, options policy.TokenRequestOptions) (azcore.AccessToken, error) {
	f.scopes = options.Scopes
	return azcore.AccessToken{Token: f.token}, nil
}

func TestDSNURLEscaping(t *testing.T) {
	endpoint := "endpoint"
	port := "5432"
	db := "postgres"
	user := "username"
	rawPass := "password^"
	encPass := "password%5E"
	sslmode := "require"
	dsn := DSN(user, rawPass, endpoint, port, db, sslmode)
	if dsn != "postgres://"+user+":"+encPass+"@"+endpoint+":"+port+"/"+db+"?sslmode="+sslmode {
		t.Errorf("DSN string did not match expected output with userinfo URL encoded")
	}
}

func TestAzureWorkloadIdentityConnectionString(t *testing.T) {
	credential := &fakeTokenCredential{token: "entra-token"}
	db := postgresDB{
		username:        "provider-admin",
		endpoint:        "server.postgres.database.azure.com",
		port:            "5432",
		database:        "postgres",
		sslmode:         "verify-full",
		authMode:        xsql.AuthenticationModeAzureWorkloadIdentity,
		tokenScope:      "https://example.invalid/.default",
		tokenCredential: credential,
	}

	dsn, err := db.connectionString(context.Background())
	if err != nil {
		t.Fatalf("connectionString returned an unexpected error: %v", err)
	}
	want := DSN("provider-admin", "entra-token", "server.postgres.database.azure.com", "5432", "postgres", "verify-full")
	if dsn != want {
		t.Fatalf("connectionString = %q, want %q", dsn, want)
	}
	if len(credential.scopes) != 1 || credential.scopes[0] != db.tokenScope {
		t.Fatalf("token scopes = %v, want [%s]", credential.scopes, db.tokenScope)
	}
	if _, found := db.GetConnectionDetails("app", "")["password"]; found {
		t.Fatal("Azure workload identity connection details must not contain a password")
	}
}
