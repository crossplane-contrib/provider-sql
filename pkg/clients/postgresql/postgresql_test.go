package postgresql

import (
	"testing"
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

func TestEnsureTLS(t *testing.T) {
	for _, tc := range []struct{ mode, want string }{
		{"", "require"}, {"disable", "require"}, {"require", "require"},
		{"verify-ca", "verify-ca"}, {"verify-full", "verify-full"}, {"invalid", "invalid"},
	} {
		t.Run(tc.mode, func(t *testing.T) {
			if got := EnsureTLS(tc.mode); got != tc.want {
				t.Errorf("want %q, got %q", tc.want, got)
			}
		})
	}
}
