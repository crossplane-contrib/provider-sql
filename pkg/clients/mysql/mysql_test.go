package mysql

import (
	"fmt"
	"strconv"
	"testing"

	driverMySQL "github.com/go-sql-driver/mysql"
)

func TestDSNURLEscaping(t *testing.T) {
	endpoint := "endpoint"
	port := "3306"
	user := "username"
	rawPass := "password^"
	tls := "true"
	binlog := false
	dsn := DSN(user, rawPass, endpoint, port, tls, &binlog, false)
	if dsn != fmt.Sprintf("%s:%s@tcp(%s:%s)/?tls=%s&sql_log_bin=%s",
		user,
		rawPass,
		endpoint,
		port,
		tls,
		strconv.FormatBool(binlog)) {
		t.Errorf("DSN string did not match expected output with URL encoded and binlog")
	}
}

func TestDSNURLEscapingWithoutBinLog(t *testing.T) {
	endpoint := "endpoint"
	port := "3306"
	user := "username"
	rawPass := "password^"
	tls := "true"
	dsn := DSN(user, rawPass, endpoint, port, tls, nil, false)
	if dsn != fmt.Sprintf("%s:%s@tcp(%s:%s)/?tls=%s",
		user,
		rawPass,
		endpoint,
		port,
		tls) {
		t.Errorf("DSN string did not match expected output with URL encoded")
	}
}

func TestDSNCleartext(t *testing.T) {
	base := "username:password@tcp(endpoint:3306)/?tls=true"

	// cleartext=true appends allowCleartextPasswords=true (required for IAM auth).
	if got := DSN("username", "password", "endpoint", "3306", "true", nil, true); got != base+"&allowCleartextPasswords=true" {
		t.Errorf("cleartext DSN: want %q, got %q", base+"&allowCleartextPasswords=true", got)
	}
	// cleartext=false must NOT append it (guards existing non-IAM users).
	if got := DSN("username", "password", "endpoint", "3306", "true", nil, false); got != base {
		t.Errorf("non-cleartext DSN: want %q, got %q", base, got)
	}
}

func TestEnsureTLS(t *testing.T) {
	for _, mode := range []string{"unset", "", "false", "preferred", "true", "skip-verify", "custom-example"} {
		t.Run(mode, func(t *testing.T) {
			var input *string
			if mode != "unset" {
				input = &mode
			}
			want := mode
			if input == nil || mode == "" || mode == "false" || mode == "preferred" {
				want = "true"
			}
			got := EnsureTLS(input)
			if got == nil || *got != want {
				t.Fatalf("want %q, got %v", want, got)
			}
			if want == mode && got != input {
				t.Error("explicit setting should be preserved")
			}
			if want == "true" {
				cfg, err := driverMySQL.ParseDSN(DSN("user", "token", "db.example.com", "3306", *got, nil, true))
				if err != nil {
					t.Fatal(err)
				}
				if cfg.TLS == nil || cfg.TLS.InsecureSkipVerify || cfg.AllowFallbackToPlaintext {
					t.Fatal("default IAM mode must verify TLS without plaintext fallback")
				}
				if cfg.TLS.ServerName != "db.example.com" {
					t.Errorf("missing hostname: %q", cfg.TLS.ServerName)
				}
			}
		})
	}
}

func TestPasswordAuthenticationTLSDefaultUnchanged(t *testing.T) {
	db := New(map[string][]byte{}, nil, nil, false).(mySQLDB)
	if db.tls != "preferred" {
		t.Fatalf("non-IAM default changed to %q", db.tls)
	}
}
