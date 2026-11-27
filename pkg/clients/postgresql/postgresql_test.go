package postgresql

import (
	"context"
	"database/sql"
	"errors"
	"fmt"
	"net/url"
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

// A stray newline in a connection Secret value, often left behind by
// base64-encoding with echo, makes the DSN unparseable. That error is written
// to the managed resource's status conditions and events, so it must not carry
// the password.
func TestDSNParseErrorOmitsPassword(t *testing.T) {
	const password = "s3cr3t-pa55"
	db := New(map[string][]byte{
		xpv2.CredentialsSecretEndpointKey: []byte("db.example.org\n"),
		xpv2.CredentialsSecretPortKey:     []byte("5432"),
		xpv2.CredentialsSecretUserKey:     []byte("admin"),
		xpv2.CredentialsSecretPasswordKey: []byte(password),
	}, "postgres", "require")
	q := xsql.Query{String: "SELECT 1"}

	cases := map[string]func(ctx context.Context) error{
		"Exec":   func(ctx context.Context) error { return db.Exec(ctx, q) },
		"ExecTx": func(ctx context.Context) error { return db.ExecTx(ctx, []xsql.Query{q}) },
		"Query": func(ctx context.Context) error {
			rows, err := db.Query(ctx, q)
			if err == nil {
				_ = rows.Close()
			}
			return err
		},
		"Scan": func(ctx context.Context) error {
			var v int
			return db.Scan(ctx, q, &v)
		},
		"GetServerVersion": func(ctx context.Context) error {
			_, err := db.GetServerVersion(ctx)
			return err
		},
	}
	for name, call := range cases {
		t.Run(name, func(t *testing.T) {
			err := call(t.Context())
			if err == nil {
				t.Fatalf("%s(): got nil error, want a DSN parse error", name)
			}
			if strings.Contains(err.Error(), password) {
				t.Errorf("%s(): error %q contains the password", name, err)
			}
			if !strings.Contains(err.Error(), "invalid URL escape") {
				t.Errorf("%s(): error %q no longer says why the DSN is invalid", name, err)
			}
			if !strings.Contains(err.Error(), "db.example.org") {
				t.Errorf("%s(): error %q no longer names the host", name, err)
			}
		})
	}
}

// Callers branch on some of the errors the client returns, such as
// xsql.IsNoRows and IsInvalidCatalog, so redact must leave everything except
// a DSN parse error untouched.
func TestRedactPassesOtherErrorsThrough(t *testing.T) {
	c := postgresDB{redacted: "postgres://admin:xxxxx@db.example.org/postgres"}

	cases := map[string]struct {
		err  error
		want func(error) bool
	}{
		"Nil":            {err: nil, want: func(err error) bool { return err == nil }},
		"NoRows":         {err: sql.ErrNoRows, want: xsql.IsNoRows},
		"InvalidCatalog": {err: &pq.Error{Code: pqInvalidCatalog}, want: IsInvalidCatalog},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := c.redact(tc.err); !tc.want(got) {
				t.Errorf("redact(%v) = %v, want the same error back", tc.err, got)
			}
		})
	}
}

// A DSN parse error that reaches redact wrapped by another error keeps the
// wrapper's text, and errors.As still finds the *url.Error, with the password
// masked.
func TestRedactKeepsWrapperText(t *testing.T) {
	c := postgresDB{redacted: "postgres://admin:xxxxx@db.example.org/postgres"}
	parseErr := &url.Error{Op: "parse", URL: "postgres://admin:s3cr3t@db.example.org/postgres", Err: errors.New("invalid port")}

	err := c.redact(fmt.Errorf("connecting: %w", parseErr))
	want := `connecting: parse "postgres://admin:xxxxx@db.example.org/postgres": invalid port`
	if err.Error() != want {
		t.Errorf("redact(...).Error() = %q, want %q", err.Error(), want)
	}
	var uerr *url.Error
	if !errors.As(err, &uerr) {
		t.Fatalf("errors.As(redact(...), *url.Error) = false, want true")
	}
	if uerr.URL != c.redacted {
		t.Errorf("unwrapped *url.Error URL = %q, want %q", uerr.URL, c.redacted)
	}
}
