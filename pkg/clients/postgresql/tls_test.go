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

package postgresql

import (
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"testing"
	"time"
)

func testCertPEM(t *testing.T, name string) ([]byte, *x509.Certificate) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: name},
		DNSNames:              []string{name},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	cert, err := x509.ParseCertificate(der)
	if err != nil {
		t.Fatalf("parse certificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), cert
}

func TestTLSConfig(t *testing.T) {
	pemA, certA := testCertPEM(t, "a.example.com")
	pemB, certB := testCertPEM(t, "b.example.com")
	bundle := append(append([]byte{}, pemA...), pemB...)

	t.Run("VerifyFull", func(t *testing.T) {
		cfg, err := TLSConfig(sslModeVerifyFull, "db.example.rds.amazonaws.com", pemA)
		if err != nil {
			t.Fatalf("TLSConfig: %v", err)
		}
		if cfg.InsecureSkipVerify {
			t.Error("verify-full must leave certificate verification enabled")
		}
		if cfg.ServerName != "db.example.rds.amazonaws.com" {
			t.Errorf("ServerName: want db.example.rds.amazonaws.com, got %q", cfg.ServerName)
		}
		if cfg.VerifyPeerCertificate != nil {
			t.Error("verify-full uses crypto/tls verification, not a custom callback")
		}
	})

	t.Run("VerifyCAChainOnly", func(t *testing.T) {
		cfg, err := TLSConfig(sslModeVerifyCA, "db.example.rds.amazonaws.com", pemA)
		if err != nil {
			t.Fatalf("TLSConfig: %v", err)
		}
		if !cfg.InsecureSkipVerify {
			t.Error("verify-ca skips the hostname check")
		}
		if cfg.VerifyPeerCertificate == nil {
			t.Fatal("verify-ca must check the certificate chain")
		}
		if err := cfg.VerifyPeerCertificate([][]byte{certA.Raw}, nil); err != nil {
			t.Errorf("certificate in the bundle should verify: %v", err)
		}
		if err := cfg.VerifyPeerCertificate([][]byte{certB.Raw}, nil); err == nil {
			t.Error("certificate outside the bundle must fail verification")
		}
	})

	t.Run("BundleLoadsEveryCertificate", func(t *testing.T) {
		cfg, err := TLSConfig(sslModeVerifyCA, "db.example.com", bundle)
		if err != nil {
			t.Fatalf("TLSConfig: %v", err)
		}
		if err := cfg.VerifyPeerCertificate([][]byte{certA.Raw}, nil); err != nil {
			t.Errorf("first certificate in the bundle should verify: %v", err)
		}
		if err := cfg.VerifyPeerCertificate([][]byte{certB.Raw}, nil); err != nil {
			t.Errorf("second certificate in the bundle should verify: %v", err)
		}
	})

	t.Run("NotPEM", func(t *testing.T) {
		if _, err := TLSConfig(sslModeVerifyFull, "db.example.com", []byte("not a certificate")); err == nil {
			t.Fatal("expected an error for a non-PEM secret")
		}
	})
}

func TestUsesCustomCA(t *testing.T) {
	if UsesCustomCA("require") || UsesCustomCA("disable") || UsesCustomCA("") {
		t.Error("require, disable, and an empty mode do not use a custom CA")
	}
	if !UsesCustomCA(sslModeVerifyCA) || !UsesCustomCA(sslModeVerifyFull) {
		t.Error("verify-ca and verify-full use a custom CA")
	}
}
