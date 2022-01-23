package postgresql

import (
	"testing"
)

func TestDSNURLEscaping(t *testing.T) {
	endpoint := "endpoint"
	port := "5432"
	db := "postgres"
	user := "username"
	pass := "password"
	dsn := DSN(user, pass, endpoint, port, db)
	if dsn != "postgres://"+user+":"+pass+"@"+endpoint+":"+port+"/"+db {
		t.Errorf("DSN string did not match expected output with userinfo URL encoded")
	}
}
