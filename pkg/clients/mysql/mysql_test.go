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
	dsn := DSN(user, rawPass, endpoint, port, tls, &binlog)
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
	dsn := DSN(user, rawPass, endpoint, port, tls, nil)
	if dsn != fmt.Sprintf("%s:%s@tcp(%s:%s)/?tls=%s",
		user,
		rawPass,
		endpoint,
		port,
		tls) {
		t.Errorf("DSN string did not match expected output with URL encoded")
	}
}
