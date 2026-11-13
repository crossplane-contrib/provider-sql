/*
Copyright 2026 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package mysql

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
	"crypto/x509"
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

// Exercise the real MySQL driver and its TLS negotiation, without AWS or a
// database process. The server returns a recognizable error after receiving
// the authentication packet so the test proves TLS and password injection ran.
func TestIAMTLSNegotiation(t *testing.T) {
	if os.Getenv("PROVIDER_SQL_MYSQL_TLS_CHILD") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestIAMTLSNegotiation$", "-test.v", "-test.timeout=30s")
		cmd.Env = append(os.Environ(), "PROVIDER_SQL_MYSQL_TLS_CHILD=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("TLS subprocess: %v\n%s", err, out)
		}
		return
	}
	dir := t.TempDir()
	t.Setenv("SSL_CERT_FILE", filepath.Join(dir, "ca.pem"))
	t.Setenv("SSL_CERT_DIR", dir)
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	ca := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	caDER, err := x509.CreateCertificate(rand.Reader, ca, ca, &key.PublicKey, key)
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
		{"omitted verifies", "unset", true, true, "TLS accepted"},
		{"omitted rejects untrusted", "unset", false, true, "unknown authority"},
		{"empty rejects untrusted", "", false, true, "unknown authority"},
		{"false rejects untrusted", "false", false, true, "unknown authority"},
		{"preferred rejects untrusted", "preferred", false, true, "unknown authority"},
		{"default rejects hostname mismatch", "unset", true, false, "not 127.0.0.1"},
		{"explicit unverified opt-in", "skip-verify", false, false, "TLS accepted"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			leafKey, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
			if err != nil {
				t.Fatal(err)
			}
			ip := "127.0.0.1"
			if !tc.matching {
				ip = "127.0.0.2"
			}
			leaf := &x509.Certificate{SerialNumber: big.NewInt(2), NotBefore: ca.NotBefore, NotAfter: ca.NotAfter, IPAddresses: []net.IP{net.ParseIP(ip)}, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageServerAuth}, KeyUsage: x509.KeyUsageDigitalSignature}
			parent, signer := ca, key
			if !tc.trusted {
				parent, signer = leaf, leafKey
			}
			der, err := x509.CreateCertificate(rand.Reader, leaf, parent, &leafKey.PublicKey, signer)
			if err != nil {
				t.Fatal(err)
			}
			listener, err := net.Listen("tcp", "127.0.0.1:0")
			if err != nil {
				t.Fatal(err)
			}
			defer listener.Close()
			done := make(chan bool, 1)
			go func() {
				accepted := false
				defer func() { done <- accepted }()
				conn, err := listener.Accept()
				if err != nil {
					return
				}
				defer conn.Close()
				_ = conn.SetDeadline(time.Now().Add(5 * time.Second))
				const capabilities uint32 = 1 | 512 | 2048 | 8192 | 32768 | 524288
				greeting := append([]byte{10}, []byte("8.4-test\x00")...)
				greeting = append(greeting, 1, 0, 0, 0)
				greeting = append(greeting, []byte("12345678\x00")...)
				greeting = append(greeting, byte(capabilities&255), byte((capabilities>>8)&255), 45, 2, 0, byte((capabilities>>16)&255), byte((capabilities>>24)&255), 21)
				greeting = append(greeting, make([]byte, 10)...)
				greeting = append(greeting, []byte("123456789012\x00mysql_clear_password\x00")...)
				if writeMySQLTestPacket(conn, 0, greeting) != nil {
					return
				}
				request, err := readMySQLTestPacket(conn)
				if err != nil || len(request) != 32 {
					return
				}
				if binary.LittleEndian.Uint32(request)&2048 == 0 {
					return
				}
				secure := tls.Server(conn, &tls.Config{MinVersion: tls.VersionTLS12, Certificates: []tls.Certificate{{Certificate: [][]byte{der}, PrivateKey: leafKey}}})
				if secure.Handshake() != nil {
					return
				}
				auth, err := readMySQLTestPacket(secure)
				if err != nil {
					return
				}
				accepted = strings.Contains(string(auth), "fresh-token")
				_ = writeMySQLTestPacket(secure, 3, append([]byte{255, 0x15, 0x04, '#', '2', '8', '0', '0', '0'}, []byte("TLS accepted")...))
			}()
			host, port, err := net.SplitHostPort(listener.Addr().String())
			if err != nil {
				t.Fatal(err)
			}
			var mode *string
			if tc.mode != "unset" {
				mode = &tc.mode
			}
			signed := 0
			db := NewWithPasswordProvider(map[string][]byte{xpv2.CredentialsSecretEndpointKey: []byte(host), xpv2.CredentialsSecretPortKey: []byte(port), xpv2.CredentialsSecretUserKey: []byte("iam-user")}, EnsureTLS(mode), nil, true, func(context.Context) (string, error) { signed++; return "fresh-token", nil })
			ctx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
			defer cancel()
			var value int
			err = db.Scan(ctx, xsql.Query{String: "SELECT 1"}, &value)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Errorf("want %q, got %v", tc.want, err)
			}
			_ = listener.Close()
			accepted := <-done
			if tc.want == "TLS accepted" && !accepted {
				t.Error("fresh password did not reach authentication packet")
			}
			if signed != 1 {
				t.Errorf("expected one signing call, got %d", signed)
			}
		})
	}
}

func writeMySQLTestPacket(w io.Writer, seq byte, body []byte) error {
	header := []byte{byte(len(body)), byte(len(body) >> 8), byte(len(body) >> 16), seq}
	_, err := w.Write(append(header, body...))
	return err
}
func readMySQLTestPacket(r io.Reader) ([]byte, error) {
	var header [4]byte
	if _, err := io.ReadFull(r, header[:]); err != nil {
		return nil, err
	}
	n := int(header[0]) | int(header[1])<<8 | int(header[2])<<16
	body := make([]byte, n)
	_, err := io.ReadFull(r, body)
	return body, err
}
