package postgresql

import (
	"net/url"
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

func TestDSNIAMTokenEscaping(t *testing.T) {
	token := "db.example.com:5432/?Action=connect&DBUser=crossplane_admin&X-Amz-Algorithm=AWS4-HMAC-SHA256"
	dsn := DSN("crossplane_admin", token, "db.example.com", "5432", "postgres", "verify-full")
	u, err := url.Parse(dsn)
	if err != nil {
		t.Fatalf("parse DSN: %v", err)
	}
	pass, ok := u.User.Password()
	if !ok || pass != token {
		t.Errorf("password: want the IAM token, got %q", pass)
	}
	if u.Query().Get("sslmode") != "verify-full" {
		t.Errorf("sslmode: want verify-full, got %q", u.Query().Get("sslmode"))
	}
	if u.Query().Get("sslrootcert") != "" {
		t.Errorf("sslrootcert must stay unset so a registered CA is used, got %q", u.Query().Get("sslrootcert"))
	}
}
