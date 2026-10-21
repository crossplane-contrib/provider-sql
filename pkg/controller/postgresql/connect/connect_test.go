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

package connect

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"crypto/x509/pkix"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
)

func certPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatalf("generate key: %v", err)
	}
	tmpl := &x509.Certificate{
		SerialNumber:          big.NewInt(1),
		Subject:               pkix.Name{CommonName: "rds.example.com"},
		NotBefore:             time.Now().Add(-time.Hour),
		NotAfter:              time.Now().Add(time.Hour),
		IsCA:                  true,
		BasicConstraintsValid: true,
		KeyUsage:              x509.KeyUsageCertSign,
	}
	der, err := x509.CreateCertificate(rand.Reader, tmpl, tmpl, &key.PublicKey, key)
	if err != nil {
		t.Fatalf("create certificate: %v", err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func creds() map[string][]byte {
	return map[string][]byte{
		xpv2.CredentialsSecretEndpointKey: []byte("db.example.rds.amazonaws.com"),
		xpv2.CredentialsSecretPortKey:     []byte("5432"),
		xpv2.CredentialsSecretUserKey:     []byte("crossplane_admin"),
	}
}

func TestPrepareSSLModePassthrough(t *testing.T) {
	orig := InjectIAM
	InjectIAM = func(_ context.Context, _ *string, c map[string][]byte) error {
		c[xpv2.CredentialsSecretPasswordKey] = []byte("iam-token")
		return nil
	}
	t.Cleanup(func() { InjectIAM = orig })

	kube := &test.MockClient{MockGet: test.NewMockGetFn(errors.New("secret must not be read"))}
	for _, mode := range []string{"disable", "require", "verify-ca", "verify-full"} {
		got, err := Prepare(context.Background(), kube, Input{
			Source:  "AWSIAMAuth",
			SSLMode: &mode,
			Creds:   creds(),
		})
		if err != nil {
			t.Fatalf("Prepare(%s): %v", mode, err)
		}
		if got != mode {
			t.Errorf("Prepare(%s): want %q, got %q", mode, mode, got)
		}
	}

	got, err := Prepare(context.Background(), kube, Input{Source: "AWSIAMAuth", Creds: creds()})
	if err != nil {
		t.Fatalf("Prepare(unset): %v", err)
	}
	if got != "" {
		t.Errorf("unset sslMode: want empty, got %q", got)
	}
	if len(creds()[xpv2.CredentialsSecretPasswordKey]) != 0 {
		t.Fatal("fixture creds must start without a password")
	}
}

func TestPrepareRegistersCA(t *testing.T) {
	pemBytes := certPEM(t)
	kube := &test.MockClient{
		MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
			secret, ok := obj.(*corev1.Secret)
			if !ok {
				t.Fatalf("got %T, want Secret", obj)
			}
			secret.Data = map[string][]byte{"global-bundle.pem": pemBytes}
			return nil
		}),
	}
	mode := "verify-full"
	got, err := Prepare(context.Background(), kube, Input{
		SSLMode:          &mode,
		CA:               CA("amazon-rds-ca", "", "global-bundle.pem"),
		DefaultNamespace: "crossplane-system",
		RegisterKey:      "ns-crossplane-system-aurora",
		Creds:            creds(),
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if got != "pqgo-ns-crossplane-system-aurora" {
		t.Errorf("sslmode: want pqgo-ns-crossplane-system-aurora, got %q", got)
	}
}

func TestPrepareRequireIgnoresCA(t *testing.T) {
	kube := &test.MockClient{MockGet: test.NewMockGetFn(errors.New("secret must not be read"))}
	mode := "require"
	got, err := Prepare(context.Background(), kube, Input{
		SSLMode:     &mode,
		CA:          CA("amazon-rds-ca", "ns", "global-bundle.pem"),
		RegisterKey: "cluster-aurora",
		Creds:       creds(),
	})
	if err != nil {
		t.Fatalf("Prepare: %v", err)
	}
	if got != "require" {
		t.Errorf("sslmode: want require, got %q", got)
	}
}

func TestPrepareRejectsNonPEM(t *testing.T) {
	kube := &test.MockClient{
		MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
			obj.(*corev1.Secret).Data = map[string][]byte{"ca.pem": []byte("nope")}
			return nil
		}),
	}
	mode := "verify-full"
	_, err := Prepare(context.Background(), kube, Input{
		SSLMode:     &mode,
		CA:          CA("amazon-rds-ca", "ns", "ca.pem"),
		RegisterKey: "cluster-aurora",
		Creds:       creds(),
	})
	if err == nil || !strings.Contains(err.Error(), "cannot parse CA bundle") {
		t.Fatalf("want a CA parse error, got %v", err)
	}
}
