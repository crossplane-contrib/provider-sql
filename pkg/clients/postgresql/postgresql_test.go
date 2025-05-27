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
	options := Options{
		SSLMode: sslmode,
	}
	dsn := DSN(user, rawPass, endpoint, port, db, options.queryString())
	if dsn != "postgres://"+user+":"+encPass+"@"+endpoint+":"+port+"/"+db+"?sslmode="+sslmode {
		t.Errorf("DSN string did not match expected output with userinfo URL encoded")
	}
}

func TestOptionsToQueryString(t *testing.T) {
	cases := []struct {
		name     string
		given    Options
		expected string
	}{
		{
			name:     "empty",
			given:    Options{},
			expected: "",
		},
		{
			name: "everything",
			given: Options{
				SSLMode:     "require",
				SSLCert:     "/path/to/ssl.crt",
				SSLKey:      "/path/to/ssl.key",
				SSLRootCert: "/path/to/ca.crt",
			},
			expected: "sslcert=%2Fpath%2Fto%2Fssl.crt&sslkey=%2Fpath%2Fto%2Fssl.key&sslmode=require&sslrootcert=%2Fpath%2Fto%2Fca.crt",
		},
	}

	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := c.given.queryString()
			if c.expected != got {
				t.Errorf("Expected query string to be %q, but it was %q", c.expected, got)
			}
		})
	}
}
