package postgresql

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
	"github.com/lib/pq"

	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
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
			if !strings.Contains(err.Error(), "invalid control character in URL") {
				t.Errorf("%s(): error %q no longer says why the DSN is invalid", name, err)
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
