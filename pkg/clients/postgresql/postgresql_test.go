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
	dsn := DSN(user, rawPass, endpoint, port, db)
	if dsn != "postgres://"+user+":"+encPass+"@"+endpoint+":"+port+"/"+db {
		t.Errorf("DSN string did not match expected output with userinfo URL encoded")
	}
}
