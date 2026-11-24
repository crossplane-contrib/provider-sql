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

package mysql_test

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

	cluster "github.com/crossplane-contrib/provider-sql/apis/cluster/mysql/v1alpha1"
	modern "github.com/crossplane-contrib/provider-sql/apis/namespaced/mysql/v1alpha1"
	clusterTLS "github.com/crossplane-contrib/provider-sql/pkg/controller/cluster/mysql/tls"
	modernTLS "github.com/crossplane-contrib/provider-sql/pkg/controller/namespaced/mysql/tls"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
	driverMySQL "github.com/go-sql-driver/mysql"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func testCA(t *testing.T, name string) ([]byte, []byte) {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
	}
	template := &x509.Certificate{SerialNumber: big.NewInt(1), Subject: pkix.Name{CommonName: name}, NotBefore: time.Now().Add(-time.Hour), NotAfter: time.Now().Add(time.Hour), IsCA: true, BasicConstraintsValid: true, KeyUsage: x509.KeyUsageCertSign | x509.KeyUsageDigitalSignature, ExtKeyUsage: []x509.ExtKeyUsage{x509.ExtKeyUsageClientAuth}}
	der, err := x509.CreateCertificate(rand.Reader, template, template, &key.PublicKey, key)
	if err != nil {
		t.Fatal(err)
	}
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der}), pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
}

func TestCustomTLSCAOnlyAndClientPair(t *testing.T) {
	ca1, key1 := testCA(t, "database-one")
	ca2, _ := testCA(t, "database-two")
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "one", Namespace: "certs"}, Data: map[string][]byte{"ca": ca1, "cert": ca1, "key": key1, "bad": []byte("invalid PEM")}},
		&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "two", Namespace: "certs"}, Data: map[string][]byte{"ca": ca2}},
	).Build()
	ref := func(name, key string) xpv2.SecretKeySelector {
		return xpv2.SecretKeySelector{SecretReference: xpv2.SecretReference{Name: name, Namespace: "certs"}, Key: key}
	}
	type loadFunc func(context.Context, client.Client, string, *string, *cluster.TLSConfig) (*string, error)
	loaders := map[string]loadFunc{
		"cluster": clusterTLS.LoadConfig,
		"namespaced": func(ctx context.Context, k client.Client, name string, mode *string, cfg *cluster.TLSConfig) (*string, error) {
			var next *modern.TLSConfig
			if cfg != nil {
				next = &modern.TLSConfig{CACert: modern.TLSSecret{SecretRef: cfg.CACert.SecretRef}, ClientCert: modern.TLSSecret{SecretRef: cfg.ClientCert.SecretRef}, ClientKey: modern.TLSSecret{SecretRef: cfg.ClientKey.SecretRef}, InsecureSkipVerify: cfg.InsecureSkipVerify}
			}
			return modernTLS.LoadConfig(ctx, k, name, mode, next)
		},
	}
	for scope, load := range loaders {
		t.Run(scope, func(t *testing.T) {
			mode := "custom"
			caOnly := &cluster.TLSConfig{CACert: cluster.TLSSecret{SecretRef: ref("one", "ca")}}
			name, err := load(context.Background(), kube, "ProviderConfig/ns-one/same", &mode, caOnly)
			if err != nil {
				t.Fatal(err)
			}
			defer driverMySQL.DeregisterTLSConfig(*name)
			// Loading another database must not replace the first database's CA pool.
			other, err := load(context.Background(), kube, "ProviderConfig/ns-two/same", &mode, &cluster.TLSConfig{CACert: cluster.TLSSecret{SecretRef: ref("two", "ca")}})
			if err != nil {
				t.Fatal(err)
			}
			defer driverMySQL.DeregisterTLSConfig(*other)
			if *name == *other {
				t.Fatal("distinct ProviderConfigs share a TLS registration")
			}
			for _, tc := range []struct {
				name string
				ca   []byte
			}{{*name, ca1}, {*other, ca2}} {
				cfg, err := driverMySQL.ParseDSN("user:token@tcp(db.example.com:3306)/?tls=" + tc.name)
				if err != nil {
					t.Fatal(err)
				}
				pool := x509.NewCertPool()
				pool.AppendCertsFromPEM(tc.ca)
				if !cfg.TLS.RootCAs.Equal(pool) {
					t.Fatal("incorrect per-database CA pool")
				}
				if cfg.TLS.InsecureSkipVerify || len(cfg.TLS.Certificates) != 0 || cfg.TLS.ServerName != "db.example.com" {
					t.Fatal("CA-only configuration must verify hostname without a client certificate")
				}
			}
			pair := *caOnly
			pair.ClientCert.SecretRef = ref("one", "cert")
			pair.ClientKey.SecretRef = ref("one", "key")
			name, err = load(context.Background(), kube, "mutual-tls", &mode, &pair)
			if err != nil {
				t.Fatal(err)
			}
			defer driverMySQL.DeregisterTLSConfig(*name)
			cfg, err := driverMySQL.ParseDSN("user:password@tcp(db.example.com:3306)/?tls=" + *name)
			if err != nil {
				t.Fatal(err)
			}
			if len(cfg.TLS.Certificates) != 1 {
				t.Fatal("existing mutual TLS configuration lost its client certificate")
			}
			for _, tc := range []struct {
				name string
				cfg  *cluster.TLSConfig
				want string
			}{
				{"missing config", nil, "caCert"},
				{"missing CA", &cluster.TLSConfig{}, "caCert"},
				{"cert without key", &cluster.TLSConfig{CACert: caOnly.CACert, ClientCert: pair.ClientCert}, "together"},
				{"key without cert", &cluster.TLSConfig{CACert: caOnly.CACert, ClientKey: pair.ClientKey}, "together"},
				{"partial reference", &cluster.TLSConfig{CACert: caOnly.CACert, ClientCert: pair.ClientCert, ClientKey: cluster.TLSSecret{SecretRef: ref("one", "")}}, "complete"},
				{"missing secret", &cluster.TLSConfig{CACert: cluster.TLSSecret{SecretRef: ref("missing", "ca")}}, "cannot get CA"},
				{"missing key", &cluster.TLSConfig{CACert: cluster.TLSSecret{SecretRef: ref("one", "missing")}}, "cannot get CA"},
				{"invalid CA", &cluster.TLSConfig{CACert: cluster.TLSSecret{SecretRef: ref("one", "bad")}}, "cannot append"},
				{"invalid client key", &cluster.TLSConfig{CACert: caOnly.CACert, ClientCert: pair.ClientCert, ClientKey: cluster.TLSSecret{SecretRef: ref("one", "bad")}}, "cannot make client"},
			} {
				t.Run(tc.name, func(t *testing.T) {
					if _, err := load(context.Background(), kube, tc.name, &mode, tc.cfg); err == nil || !strings.Contains(err.Error(), tc.want) {
						t.Fatalf("want error containing %q, got %v", tc.want, err)
					}
				})
			}
			plain := "true"
			if _, err := load(context.Background(), kube, "invalid-mode", &plain, caOnly); err == nil {
				t.Error("tlsConfig must still require custom mode")
			}
		})
	}
}
