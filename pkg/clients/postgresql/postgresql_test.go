package postgresql

import (
	"context"
	"os"
	"strings"
	"testing"

	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
	"github.com/lib/pq"

	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
)

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

func TestPasswordProviderRejectsMalformedConfigBeforeSigning(t *testing.T) {
	const token = "SYNTHETIC-IAM-TOKEN"
	for _, tc := range []struct {
		name, endpoint, port, database string
	}{
		{"host control character", "db.example.com\n", "5432", "postgres"},
		{"invalid port", "db.example.com", "invalid", "postgres"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			calls := 0
			db := NewWithPasswordProvider(map[string][]byte{
				xpv2.CredentialsSecretEndpointKey: []byte(tc.endpoint),
				xpv2.CredentialsSecretPortKey:     []byte(tc.port),
				xpv2.CredentialsSecretUserKey:     []byte("user"),
				xpv2.CredentialsSecretPasswordKey: []byte("IGNORED-STATIC-PASSWORD"),
			}, tc.database, "verify-full", func(context.Context) (string, error) {
				calls++
				return token, nil
			})
			err := db.Scan(context.Background(), xsql.Query{String: "SELECT 1"}, new(int))
			if err == nil {
				t.Fatal("malformed connection settings must fail")
			}
			if strings.Contains(err.Error(), token) || strings.Contains(err.Error(), "IGNORED-STATIC-PASSWORD") {
				t.Fatalf("connection error exposed a credential: %v", err)
			}
			if calls != 0 {
				t.Fatalf("signed %d tokens before rejecting malformed settings", calls)
			}
		})
	}
}

func TestDSNDatabaseCannotOverrideConnectionSettings(t *testing.T) {
	for _, database := range []string{
		"sales%zz", "sales#archive", "sales\n", "sales/2026",
		"postgres?hostaddr=203.0.113.10&sslmode=disable&x=",
	} {
		t.Run(database, func(t *testing.T) {
			cfg, err := pq.NewConfig(DSN("user", "password", "db.example.com", "5432", database, "verify-full"))
			if err != nil {
				t.Fatal(err)
			}
			if cfg.Host != "db.example.com" || cfg.Hostaddr.IsValid() || cfg.Database != database || cfg.SSLMode != "verify-full" {
				t.Fatalf("database name changed connection settings: host=%q hostaddr=%v database=%q sslmode=%q", cfg.Host, cfg.Hostaddr, cfg.Database, cfg.SSLMode)
			}
		})
	}
}

func TestStaticPasswordNotInConfigError(t *testing.T) {
	const password = "SYNTHETIC-STATIC-PASSWORD"
	db := New(map[string][]byte{
		xpv2.CredentialsSecretEndpointKey: []byte("db.example.com\n"),
		xpv2.CredentialsSecretPortKey:     []byte("5432"),
		xpv2.CredentialsSecretUserKey:     []byte("user"),
		xpv2.CredentialsSecretPasswordKey: []byte(password),
	}, "postgres", "verify-full")
	err := db.Scan(context.Background(), xsql.Query{String: "SELECT 1"}, new(int))
	if err == nil || strings.Contains(err.Error(), password) {
		t.Fatalf("expected a connection error without the password, got %v", err)
	}
}

func TestEnsureTLS(t *testing.T) {
	for _, tc := range []struct{ mode, want string }{
		{"", "require"}, {"disable", "require"}, {"require", "require"},
		{"allow", "require"}, {"prefer", "require"},
		{"verify-ca", "verify-ca"}, {"verify-full", "verify-full"}, {"invalid", "invalid"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			if got := EnsureTLS(tc.mode); got != tc.want {
				t.Errorf("want %q, got %q", tc.want, got)
			}
		})
	}
}

func TestPasswordProviderConstructorDoesNotRetainStoredPassword(t *testing.T) {
	for _, entry := range os.Environ() {
		key, _, _ := strings.Cut(entry, "=")
		if strings.HasPrefix(key, "PG") {
			t.Setenv(key, "")
			if err := os.Unsetenv(key); err != nil {
				t.Fatal(err)
			}
		}
	}
	const stored = "STORED-PASSWORD-MUST-NOT-BE-RETAINED"
	creds := map[string][]byte{
		xpv2.CredentialsSecretEndpointKey: []byte("db.example"),
		xpv2.CredentialsSecretPortKey:     []byte("5432"),
		xpv2.CredentialsSecretUserKey:     []byte("user"),
		xpv2.CredentialsSecretPasswordKey: []byte(stored),
	}
	calls := 0
	password := func(context.Context) (string, error) { calls++; return "fresh-token", nil }
	dynamic := newPostgresDB(creds, "postgres", "verify-full", password)
	if strings.Contains(dynamic.dsn, stored) {
		t.Fatal("dynamic connection retained the stored password in its DSN")
	}
	cfg, err := pq.NewConfig(dynamic.dsn)
	if err != nil {
		t.Fatal(err)
	}
	if cfg.Password != "" {
		t.Fatal("dynamic connection parsed a retained password")
	}
	if calls != 0 || dynamic.passwordProvider == nil {
		t.Fatal("constructor changed when passwords are generated")
	}
	static := newPostgresDB(creds, "postgres", "verify-full", nil)
	cfg, err = pq.NewConfig(static.dsn)
	if err != nil || cfg.Password != stored {
		t.Fatalf("static password behavior changed: %v", err)
	}
	if string(creds[xpv2.CredentialsSecretPasswordKey]) != stored {
		t.Fatal("constructor modified the caller's credentials")
	}
}
