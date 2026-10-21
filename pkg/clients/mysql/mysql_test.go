package mysql

import (
	"fmt"
	"strconv"
	"strings"
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

func TestNewTLSAndCleartext(t *testing.T) {
	creds := map[string][]byte{
		"username": []byte("user"),
		"password": []byte("secret"),
		"endpoint": []byte("db.example"),
		"port":     []byte("3306"),
	}

	db := New(creds, nil, nil, false).(mySQLDB)
	if db.tls != defaultTLSMode {
		t.Errorf("nil tls: want %s, got %s", defaultTLSMode, db.tls)
	}
	if strings.Contains(db.dsn, "allowCleartextPasswords") {
		t.Errorf("password login set cleartext: %s", db.dsn)
	}

	for _, mode := range []string{"true", "skip-verify", "preferred", "custom", "false"} {
		got := New(creds, &mode, nil, false).(mySQLDB)
		if got.tls != mode {
			t.Errorf("tls: want %s, got %s", mode, got.tls)
		}
		if strings.Contains(got.dsn, "allowCleartextPasswords") {
			t.Errorf("mode %s set cleartext: %s", mode, got.dsn)
		}
	}

	disabled := "false"
	iam := New(creds, &disabled, nil, true).(mySQLDB)
	if iam.tls != disabled || !strings.Contains(iam.dsn, "allowCleartextPasswords=true") {
		t.Errorf("IAM dsn: got tls=%s dsn=%s", iam.tls, iam.dsn)
	}
}
