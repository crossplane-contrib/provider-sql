package postgresql

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/binary"
	"encoding/pem"
	"io"
	"math/big"
	"net"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
)

func TestIAMRequiresTLSWhenServerRefusesIt(t *testing.T) {
	for _, mode := range []string{"", "disable", "allow", "prefer"} {
		t.Run(mode, func(t *testing.T) {
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			done := make(chan bool, 1)
			go func() {
				conn, err := listener.Accept()
				if err != nil {
					done <- false
					return
				}
				defer conn.Close()
				_ = listener.Close() // Any plaintext retry must fail too.
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				request := make([]byte, 8)
				_, err = io.ReadFull(conn, request)
				isSSL := err == nil && binary.BigEndian.Uint32(request[4:]) == 80877103
				if isSSL {
					_, _ = conn.Write([]byte("N"))
				}
				done <- isSSL
			}()
			host, port, err := net.SplitHostPort(listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			db := NewWithPasswordProvider(map[string][]byte{
				xpv2.CredentialsSecretEndpointKey: []byte(host),
				xpv2.CredentialsSecretPortKey:     []byte(port),
				xpv2.CredentialsSecretUserKey:     []byte("user"),
			}, "postgres", EnsureTLS(mode), func(context.Context) (string, error) { return "fresh-token", nil })
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			err = db.Scan(ctx, xsql.Query{String: "SELECT 1"}, new(int))
			_ = listener.Close()
			if !<-done {
				t.Error("driver did not request TLS before authenticating")
			}
			if err == nil || !strings.Contains(err.Error(), "SSL is not enabled on the server") {
				t.Fatalf("expected refusal of plaintext without fallback, got %v", err)
			}
		})
	}
}

// Isolate Go's cached system roots so this test exercises the documented
// SSL_CERT_FILE mount mechanism without affecting other tests in the package.
func TestTLSSystemRoots(t *testing.T) {
	if os.Getenv("PROVIDER_SQL_TLS_TEST_CHILD") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestTLSSystemRoots$", "-test.v", "-test.timeout=30s")
		cmd.Env = append(os.Environ(), "PROVIDER_SQL_TLS_TEST_CHILD=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("TLS subprocess: %v\n%s", err, out)
		}
		return
	}
	dir := t.TempDir()
	t.Setenv("SSL_CERT_FILE", filepath.Join(dir, "ca-bundle.pem"))
	t.Setenv("SSL_CERT_DIR", dir)
	t.Setenv("PGSSLROOTCERT", "")
	t.Setenv("PGSSLCERT", "")
	t.Setenv("PGSSLKEY", "")
	t.Setenv("PGSERVICE", "")
	t.Setenv("HOME", dir)
	caKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: "test CA"}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &caKey.PublicKey, caKey)
	if err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(os.Getenv("SSL_CERT_FILE"), pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: caDER}), 0600); err != nil {
		t.Fatal(err)
	}
	for _, tc := range []struct {
		name, mode        string
		trusted, matching bool
		want              string
		rootFile          bool
	}{
		{"trusted matching", "verify-full", true, true, "TLS accepted", false},
		{"untrusted", "verify-full", false, true, "unknown authority", false},
		{"wrong hostname", "verify-full", true, false, "not 127.0.0.1", false},
		{"CA only permits hostname mismatch", "verify-ca", true, false, "TLS accepted", false},
		{"require ignores system roots", "require", false, false, "TLS accepted", false},
		{"verify-ca rejects untrusted", "verify-ca", false, true, "unknown authority", false},
		{"require with root file verifies CA", "require", false, true, "unknown authority", true},
		{"require with root file skips hostname", "require", true, false, "TLS accepted", true},
	} {
		t.Run(tc.name, func(t *testing.T) {
			if tc.rootFile {
				t.Setenv("PGSSLROOTCERT", os.Getenv("SSL_CERT_FILE"))
			}
			key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			ip := "127.0.0.1"
			if !tc.matching {
				ip = "127.0.0.2"
			}
			leaf := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, IPAddresses: []net.IP{net.ParseIP(ip)}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
			parent, signer := ca, caKey
			if !tc.trusted {
				parent, signer = leaf, key
			}
			der, err := x509.CreateCertificate(rand.Reader, leaf, parent, &key.PublicKey, signer)
			if err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan struct{})
			passwords := make(chan string, 1)
			const token = "db.example.com:5432/?Action=connect&DBUser=test&X-Amz-Signature=a%2Fb+c"
			go func() {
				defer close(done)
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				request := make([]byte, 8)
				if _, err = io.ReadFull(conn, request); err != nil {
					return
				}
				if binary.BigEndian.Uint32(request[4:]) != 80877103 {
					return
				}
				if _, err = conn.Write([]byte("S")); err != nil {
					return
				}
				secure := tls.Server(conn, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: key}}})
				if err = secure.Handshake(); err != nil {
					return
				}
				// Wait for the client's startup message. verify-ca performs its manual
				// certificate check after the TLS handshake and before sending startup.
				length := make([]byte, 4)
				if _, err = io.ReadFull(secure, length); err != nil {
					return
				}
				size := binary.BigEndian.Uint32(length)
				if size < 4 || size > 65536 {
					return
				}
				if _, err = io.CopyN(io.Discard, secure, int64(size-4)); err != nil {
					return
				}
				// RDS PostgreSQL requests the IAM token as a cleartext password inside TLS.
				if _, err = secure.Write([]byte{'R', 0, 0, 0, 8, 0, 0, 0, 3}); err != nil {
					return
				}
				header := make([]byte, 5)
				if _, err = io.ReadFull(secure, header); err != nil || header[0] != 'p' {
					return
				}
				size = binary.BigEndian.Uint32(header[1:])
				if size < 5 || size > 65536 {
					return
				}
				password := make([]byte, size-4)
				if _, err = io.ReadFull(secure, password); err != nil {
					return
				}
				passwords <- string(password)
				// A PostgreSQL startup error proves TLS verification completed. No real
				// PostgreSQL server or database credentials are needed.
				body := []byte("SFATAL\x00C28000\x00MTLS accepted\x00\x00")
				message := make([]byte, 5)
				message[0] = 'E'
				binary.BigEndian.PutUint32(message[1:], uint32(len(body)+4))
				_, _ = secure.Write(append(message, body...))
			}()
			host, port, err := net.SplitHostPort(listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			signed := 0
			db := NewWithPasswordProvider(map[string][]byte{xpv2.CredentialsSecretEndpointKey: []byte(host), xpv2.CredentialsSecretPortKey: []byte(port), xpv2.CredentialsSecretUserKey: []byte("test")}, "postgres", EnsureTLS(tc.mode), func(context.Context) (string, error) { signed++; return token, nil })
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var value int
			err = db.Scan(ctx, xsql.Query{String: "SELECT 1"}, &value)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want error containing %q, got %v", tc.want, err)
			}
			if signed != 1 {
				t.Errorf("expected a freshly generated password, got %d signing calls", signed)
			}
			_ = listener.Close()
			<-done
			if tc.want == "TLS accepted" {
				select {
				case got := <-passwords:
					if got != token+"\x00" {
						t.Errorf("driver altered the token: got %q", got)
					}
				default:
					t.Error("server did not receive the generated token")
				}
			} else if len(passwords) != 0 {
				t.Error("driver sent the token before verifying the server")
			}
		})
	}
}
