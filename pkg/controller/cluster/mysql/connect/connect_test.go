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
	"encoding/pem"
	"errors"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/crossplane-contrib/provider-sql/apis/cluster/mysql/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
	driver "github.com/go-sql-driver/mysql"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func TestConfigureModesAndAuthentication(t *testing.T) {
	original := newIAMToken
	t.Cleanup(func() { newIAMToken = original })
	calls := 0
	newIAMToken = func(_ context.Context, region *string, _ map[string][]byte) (xsql.PasswordProvider, error) {
		calls++
		if region == nil || *region != "eu-central-1" {
			t.Fatal("region was not passed through")
		}
		tokens := 0
		return func(context.Context) (string, error) { tokens++; return fmt.Sprintf("token-%d", tokens), nil }, nil
	}
	for _, iam := range []bool{false, true} {
		for _, tc := range []struct{ mode, iamMode string }{{"unset", "true"}, {"", "true"}, {"false", "true"}, {"preferred", "true"}, {"true", "true"}, {"skip-verify", "skip-verify"}} {
			t.Run(fmt.Sprintf("IAM=%t/%s", iam, tc.mode), func(t *testing.T) {
				calls = 0
				region := "eu-central-1"
				pc := &v1alpha1.ProviderConfig{ObjectMeta: metav1.ObjectMeta{Name: strings.ReplaceAll(t.Name(), "/", "-")}}
				pc.Spec.Credentials.Region = &region
				pc.Spec.Credentials.Source = v1alpha1.CredentialsSourceMySQLConnectionSecret
				if iam {
					pc.Spec.Credentials.Source = v1alpha1.CredentialsSourceAWSIAMAuth
				}
				if tc.mode != "unset" {
					pc.Spec.TLS = &tc.mode
				}
				creds := map[string][]byte{xpv2.CredentialsSecretPasswordKey: []byte("stored-password")}
				mode, cleartext, password, err := Configure(context.Background(), nil, pc, creds)
				if err != nil {
					t.Fatal(err)
				}
				want := tc.mode
				if want == "unset" {
					want = ""
				}
				if iam {
					want = tc.iamMode
				}
				got := ""
				if mode != nil {
					got = *mode
				}
				if cleartext != iam {
					t.Fatal("cleartext flag does not match IAM")
				}
				if !iam && tc.mode == "unset" && mode != nil {
					t.Fatal("password auth changed the nil TLS mode")
				}
				if got != want {
					t.Fatalf("mode: got %q, want %q", got, want)
				}
				if iam {
					if calls != 1 || password == nil {
						t.Fatal("IAM factory was not used exactly once")
					}
					for i := 1; i <= 2; i++ {
						token, err := password(context.Background())
						if err != nil || token != fmt.Sprintf("token-%d", i) {
							t.Fatalf("fresh password: %q, %v", token, err)
						}
					}
				} else if calls != 0 || password != nil {
					t.Fatal("password authentication invoked IAM signing")
				}
				if string(creds[xpv2.CredentialsSecretPasswordKey]) != "stored-password" {
					t.Fatal("configuration rewrote stored credentials")
				}
			})
		}
	}
}

func TestConfigureIAMError(t *testing.T) {
	original := newIAMToken
	t.Cleanup(func() { newIAMToken = original })
	boom := errors.New("signer unavailable")
	newIAMToken = func(context.Context, *string, map[string][]byte) (xsql.PasswordProvider, error) { return nil, boom }
	pc := &v1alpha1.ProviderConfig{}
	pc.Spec.Credentials.Source = v1alpha1.CredentialsSourceAWSIAMAuth
	_, _, _, err := Configure(context.Background(), nil, pc, nil)
	if !errors.Is(err, boom) || !strings.Contains(err.Error(), "cannot generate AWS IAM authentication token") {
		t.Fatalf("IAM error: %v", err)
	}
}

func testBundle(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func TestConfigureCAOnlyAndTLSFailure(t *testing.T) {
	original := newIAMToken
	t.Cleanup(func() { newIAMToken = original })
	newIAMToken = func(context.Context, *string, map[string][]byte) (xsql.PasswordProvider, error) {
		return func(context.Context) (string, error) { return "token", nil }, nil
	}
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "ca", Namespace: "trust"}, Data: map[string][]byte{"bundle": testBundle(t)}}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(secret).Build()
	mode := "custom"
	pc := &v1alpha1.ProviderConfig{ObjectMeta: metav1.ObjectMeta{Name: "helper-ca"}}
	pc.Spec.TLS = &mode
	pc.Spec.Credentials.Source = v1alpha1.CredentialsSourceAWSIAMAuth
	pc.Spec.TLSConfig = &v1alpha1.TLSConfig{CACert: v1alpha1.TLSSecret{SecretRef: xpv2.SecretKeySelector{SecretReference: xpv2.SecretReference{Name: "ca", Namespace: "trust"}, Key: "bundle"}}}
	name, cleartext, password, err := Configure(context.Background(), kube, pc, nil)
	if err != nil || name == nil || !cleartext || password == nil {
		t.Fatalf("CA-only IAM config: %v", err)
	}
	t.Cleanup(func() { driver.DeregisterTLSConfig(*name) })
	next, _, _, err := Configure(context.Background(), kube, pc, nil)
	if err != nil || *next != *name {
		t.Fatalf("registration changed on reload: %v", err)
	}
	secret.Data["bundle"] = []byte("not a certificate")
	if err := kube.Update(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
	if _, _, _, err := Configure(context.Background(), kube, pc, nil); err == nil || !strings.Contains(err.Error(), "cannot load TLS config") {
		t.Fatalf("TLS error: %v", err)
	}
}
