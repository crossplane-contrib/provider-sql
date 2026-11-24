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
	"encoding/pem"
	"fmt"
	"math/big"
	"strings"
	"testing"
	"time"

	"github.com/crossplane-contrib/provider-sql/apis/namespaced/postgresql/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
	"github.com/lib/pq"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func TestCASecretNamespaces(t *testing.T) {
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	cert := &x509.Certificate{SerialNumber: big.NewInt(1), NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign}
	der, err := x509.CreateCertificate(rand.Reader, cert, cert, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	bundle := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	original := newIAMToken
	newIAMToken = func(context.Context, *string, map[string][]byte) (xsql.PasswordProvider, error) {
		return func(context.Context) (string, error) { return "fresh-token", nil }, nil
	}
	t.Cleanup(func() { newIAMToken = original })
	modes := map[string]bool{}
	for _, kind := range []string{v1alpha1.ProviderConfigKind, v1alpha1.ClusterProviderConfigKind} {
		for _, namespace := range []string{"one", "two"} {
			for _, iam := range []bool{false, true} {
				t.Run(fmt.Sprintf("%s/%s/IAM=%t", kind, namespace, iam), func(t *testing.T) {
					source := v1alpha1.CredentialsSourcePostgreSQLConnectionSecret
					if iam {
						source = v1alpha1.CredentialsSourceAWSIAMAuth
					}
					caNamespace := namespace
					if kind == v1alpha1.ClusterProviderConfigKind {
						caNamespace = "trust"
					}
					selected := false
					mode := "verify-full"
					kube := &test.MockClient{MockGet: func(_ context.Context, key client.ObjectKey, obj client.Object) error {
						switch o := obj.(type) {
						case *v1alpha1.ProviderConfig:
							o.Spec.SSLMode = &mode
							o.Spec.Credentials.Source = source
							o.Spec.Credentials.ConnectionSecretRef.Name = "connection"
							o.Spec.TLSConfig = &v1alpha1.TLSConfig{CACert: v1alpha1.TLSSecret{SecretRef: xpv2.LocalSecretKeySelector{LocalSecretReference: xpv2.LocalSecretReference{Name: "ca"}, Key: "bundle"}}}
						case *v1alpha1.ClusterProviderConfig:
							o.Spec.SSLMode = &mode
							o.Spec.Credentials.Source = source
							o.Spec.Credentials.ConnectionSecretRef = xpv2.SecretReference{Name: "connection", Namespace: "credentials"}
							o.Spec.TLSConfig = &v1alpha1.ClusterTLSConfig{CACert: v1alpha1.ClusterTLSSecret{SecretRef: xpv2.SecretKeySelector{SecretReference: xpv2.SecretReference{Name: "ca", Namespace: "trust"}, Key: "bundle"}}}
						case *corev1.Secret:
							if key.Name == "ca" {
								if key.Namespace != caNamespace {
									return fmt.Errorf("unexpected CA namespace %s", key.Namespace)
								}
								selected = true
								o.Data = map[string][]byte{"bundle": bundle}
							} else {
								o.Data = map[string][]byte{"endpoint": []byte("db.example"), "port": []byte("5432"), "username": []byte("user"), "password": []byte("static-password")}
							}
						}
						return nil
					}}
					mg := &v1alpha1.Database{ObjectMeta: metav1.ObjectMeta{Namespace: namespace}, Spec: v1alpha1.DatabaseSpec{ManagedResourceSpec: xpv2.ManagedResourceSpec{ProviderConfigReference: &xpv2.ProviderConfigReference{Kind: kind, Name: "same"}}}}
					info, err := GetProviderConfig(context.Background(), kube, mg)
					if err != nil {
						t.Fatal(err)
					}
					if !selected || info.SSLMode == nil || !strings.HasPrefix(*info.SSLMode, "pqgo-provider-sql-postgresql-") {
						t.Fatalf("CA was not selected: %+v", info)
					}
					if (info.PasswordProvider != nil) != iam {
						t.Fatal("CA selector changed authentication source")
					}
					if string(info.SecretData["password"]) != "static-password" {
						t.Fatal("CA selection mutated password Secret")
					}
					if !iam {
						identity := kind + "/" + namespace
						if kind == v1alpha1.ClusterProviderConfigKind {
							identity = kind
						}
						if kind == v1alpha1.ProviderConfigKind && modes[*info.SSLMode] {
							t.Fatalf("namespace collision for %s", identity)
						}
						modes[*info.SSLMode] = true
					}
					t.Cleanup(func() { _ = pq.RegisterTLSConfig(*info.SSLMode, nil) })
				})
			}
		}
	}
}
