package postgresql

import (
	"os"
	"testing"

	v1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
)

const (
	cert = `-----BEGIN CERTIFICATE-----
MIIE/zCCAuegAwIBAgIUaQ2NqCCwap45bHpheQkI8ogei9wwDQYJKoZIhvcNAQEL
nttUIJXC4NaaNY8xpwYBQF+Qm/Fkr68f2PbEQKp5MH1SI6U=
-----END CERTIFICATE-----`

	key = `-----BEGIN PRIVATE KEY-----
MIIJQQIBADANBgkqhkiG9w0BAQEFAASCCSswggknAgEAAoICAQDBLPQTfvYTlFE1
cX7ME/e2Dw2PsBiJYZaE7Nt/J1U5
-----END PRIVATE KEY-----`

	ca = `-----BEGIN CERTIFICATE-----
MIIE+zCCAuOgAwIBAgIUSGfXbm4N5zBbvOo3tKJHMwxMu7YwDQYJKoZIhvcNAQEL
tayuDzWN/5JRcsY3WSewY9kAzwLCXWzY9GzvBcRrPQ==
-----END CERTIFICATE-----`
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
	tmpdir := t.TempDir()
	os.Setenv("TMPDIR", tmpdir)

	inline := Options{}
	inline.withSecretData(map[string][]byte{
		v1.ResourceCredentialsSecretClientCertKey: []byte(cert),
		v1.ResourceCredentialsSecretClientKeyKey:  []byte(key),
		v1.ResourceCredentialsSecretCAKey:         []byte(ca),
	})

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
