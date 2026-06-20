package mysql

import (
	"fmt"
	"strconv"
	"testing"
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
