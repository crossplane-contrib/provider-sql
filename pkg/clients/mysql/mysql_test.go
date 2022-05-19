package mysql

import (
	"fmt"
	"testing"
)

func TestDSNURLEscaping(t *testing.T) {
	endpoint := "endpoint"
	port := "3306"
	user := "username"
	rawPass := "password^"
	encPass := "password%5E"
	tls := "true"
	dsn := DSN(user, rawPass, endpoint, port, tls)
	if dsn != fmt.Sprintf("%s:%s@tcp(%s:%s)/?tls=%s",
		user,
		encPass,
		endpoint,
		port,
		tls) {
		t.Errorf("DSN string did not match expected output with URL encoded")
	}
}
