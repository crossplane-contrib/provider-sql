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

package provider

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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/crossplane-contrib/provider-sql/apis/namespaced/postgresql/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/controller/postgresql/connect"
)

func TestGetProviderConfigAWSIAMAuth(t *testing.T) {
	orig := connect.InjectIAM
	connect.InjectIAM = func(_ context.Context, _ *string, creds map[string][]byte) error {
		creds[xpv2.CredentialsSecretPasswordKey] = []byte("iam-token")
		return nil
	}
	t.Cleanup(func() { connect.InjectIAM = orig })

	conn := map[string][]byte{
		xpv2.CredentialsSecretEndpointKey: []byte("db.example.rds.amazonaws.com"),
		xpv2.CredentialsSecretPortKey:     []byte("5432"),
		xpv2.CredentialsSecretUserKey:     []byte("crossplane_admin"),
	}

	for _, mode := range []string{"disable", "require", "verify-ca", "verify-full"} {
		t.Run("ProviderConfig/"+mode, func(t *testing.T) {
			info := getInfo(t, &test.MockClient{
				MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
					switch o := obj.(type) {
					case *v1alpha1.ProviderConfig:
						o.Spec.Credentials.Source = v1alpha1.CredentialsSourceAWSIAMAuth
						o.Spec.Credentials.ConnectionSecretRef = xpv2.LocalSecretReference{Name: "s"}
						o.Spec.SSLMode = ptr(mode)
					case *corev1.Secret:
						o.Data = conn
					}
					return nil
				}),
			}, providerConfig("default", v1alpha1.ProviderConfigKind))
			if got := string(info.SecretData[xpv2.CredentialsSecretPasswordKey]); got != "iam-token" {
				t.Errorf("password: want iam-token, got %q", got)
			}
			if info.SSLMode == nil || *info.SSLMode != mode {
				t.Errorf("sslMode: want %s, got %v", mode, info.SSLMode)
			}
		})

		t.Run("ClusterProviderConfig/"+mode, func(t *testing.T) {
			info := getInfo(t, &test.MockClient{
				MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
					switch o := obj.(type) {
					case *v1alpha1.ClusterProviderConfig:
						o.Spec.Credentials.Source = v1alpha1.CredentialsSourceAWSIAMAuth
						o.Spec.Credentials.ConnectionSecretRef = xpv2.SecretReference{Namespace: "crossplane-system", Name: "s"}
						o.Spec.SSLMode = ptr(mode)
					case *corev1.Secret:
						o.Data = conn
					}
					return nil
				}),
			}, providerConfig("default", v1alpha1.ClusterProviderConfigKind))
			if got := string(info.SecretData[xpv2.CredentialsSecretPasswordKey]); got != "iam-token" {
				t.Errorf("password: want iam-token, got %q", got)
			}
			if info.SSLMode == nil || *info.SSLMode != mode {
				t.Errorf("sslMode: want %s, got %v", mode, info.SSLMode)
			}
		})
	}
}

func TestGetProviderConfigCA(t *testing.T) {
	pemBytes := certPEM(t)
	secrets := 0
	info := getInfo(t, &test.MockClient{
		MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
			switch o := obj.(type) {
			case *v1alpha1.ProviderConfig:
				o.Spec.Credentials.Source = v1alpha1.CredentialsSourcePostgreSQLConnectionSecret
				o.Spec.Credentials.ConnectionSecretRef = xpv2.LocalSecretReference{Name: "s"}
				o.Spec.SSLMode = ptr("verify-full")
				o.Spec.TLSConfig = &v1alpha1.TLSConfig{CACert: v1alpha1.TLSSecret{SecretRef: v1alpha1.TLSSecretRef{
					Name: "amazon-rds-ca",
					Key:  "global-bundle.pem",
				}}}
			case *corev1.Secret:
				secrets++
				if secrets == 1 {
					o.Data = map[string][]byte{
						xpv2.CredentialsSecretEndpointKey: []byte("db.example.rds.amazonaws.com"),
						xpv2.CredentialsSecretPortKey:     []byte("5432"),
						xpv2.CredentialsSecretUserKey:     []byte("crossplane_admin"),
						xpv2.CredentialsSecretPasswordKey: []byte("secret"),
					}
					return nil
				}
				o.Data = map[string][]byte{"global-bundle.pem": pemBytes}
			}
			return nil
		}),
	}, providerConfig("default", v1alpha1.ProviderConfigKind))
	if info.SSLMode == nil || !strings.HasPrefix(*info.SSLMode, "pqgo-") {
		t.Errorf("sslMode: want pqgo-..., got %v", info.SSLMode)
	}
}

func providerConfig(namespace, kind string) *v1alpha1.Database {
	return &v1alpha1.Database{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace},
		Spec: v1alpha1.DatabaseSpec{
			ManagedResourceSpec: xpv2.ManagedResourceSpec{
				ProviderConfigReference: &xpv2.ProviderConfigReference{
					Kind: kind,
					Name: "aurora",
				},
			},
		},
	}
}

func getInfo(t *testing.T, kube client.Client, mg *v1alpha1.Database) ProviderInfo {
	t.Helper()
	info, err := GetProviderConfig(context.Background(), kube, mg)
	if err != nil {
		t.Fatalf("GetProviderConfig: %v", err)
	}
	return info
}

func ptr(s string) *string { return &s }

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
