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
	}{
		{"trusted matching", "verify-full", true, true, "TLS accepted"},
		{"untrusted", "verify-full", false, true, "unknown authority"},
		{"wrong hostname", "verify-full", true, false, "not 127.0.0.1"},
		{"CA only permits hostname mismatch", "verify-ca", true, false, "TLS accepted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
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
			db := New(map[string][]byte{xpv2.CredentialsSecretEndpointKey: []byte(host), xpv2.CredentialsSecretPortKey: []byte(port), xpv2.CredentialsSecretUserKey: []byte("test")}, "postgres", EnsureTLS(tc.mode))
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var value int
			err = db.Scan(ctx, xsql.Query{String: "SELECT 1"}, &value)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want error containing %q, got %v", tc.want, err)
			}
			_ = listener.Close()
			<-done
		})
	}
}
