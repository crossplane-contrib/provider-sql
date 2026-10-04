package mysql

import (
	"fmt"
	"strconv"
	"testing"

	mysqldriver "github.com/go-sql-driver/mysql"
)

func TestDSNURLEscaping(t *testing.T) {
	endpoint := "endpoint"
	port := "3306"
	user := "username"
	rawPass := "password^"
	tls := "true"
	binlog := false
	dsn := DSN(user, rawPass, endpoint, port, tls, &binlog)
	if dsn != fmt.Sprintf("%s:%s@tcp(%s:%s)/?tls=%s&lock_wait_timeout=%d&timeout=%s&sql_log_bin=%s",
		user,
		rawPass,
		endpoint,
		port,
		tls,
		lockWaitTimeoutSeconds,
		dialTimeout,
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
	dsn := DSN(user, rawPass, endpoint, port, tls, nil)
	if dsn != fmt.Sprintf("%s:%s@tcp(%s:%s)/?tls=%s&lock_wait_timeout=%d&timeout=%s",
		user,
		rawPass,
		endpoint,
		port,
		tls,
		lockWaitTimeoutSeconds,
		dialTimeout) {
		t.Errorf("DSN string did not match expected output with URL encoded")
	}
}

// TestDSNParsesWithLockWaitTimeout guards against a typo in the DSN param
// names: it verifies the go-sql-driver actually parses the DSN we build and
// that lock_wait_timeout is carried as a session variable (issued as
// `SET lock_wait_timeout = <n>` on connect) and timeout as the dial timeout.
func TestDSNParsesWithLockWaitTimeout(t *testing.T) {
	dsn := DSN("username", "password", "endpoint", "3306", "preferred", nil)

	cfg, err := mysqldriver.ParseDSN(dsn)
	if err != nil {
		t.Fatalf("go-sql-driver could not parse the DSN we build: %v", err)
	}

	want := strconv.Itoa(lockWaitTimeoutSeconds)
	if got := cfg.Params["lock_wait_timeout"]; got != want {
		t.Errorf("lock_wait_timeout: got %q, want %q", got, want)
	}
	if cfg.Timeout == 0 {
		t.Errorf("dial timeout was not parsed from the DSN")
	}
}
