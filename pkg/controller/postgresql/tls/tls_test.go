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

package tls

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/x509"
	"encoding/pem"
	"math/big"
	"strings"
	"testing"
	"time"

	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
	"github.com/lib/pq"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func testCA(t *testing.T) []byte {
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

func TestLoadConfig(t *testing.T) {
	scheme := runtime.NewScheme()
	if err := corev1.AddToScheme(scheme); err != nil {
		t.Fatal(err)
	}
	kube := fake.NewClientBuilder().WithScheme(scheme).WithObjects(&corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "ca", Namespace: "one"}, Data: map[string][]byte{"bundle": testCA(t), "empty": {}, "bad": []byte("PRIVATE-MATERIAL-MUST-NOT-APPEAR")}}).Build()
	ref := func(ns, name, key string) *xpv2.SecretKeySelector {
		return &xpv2.SecretKeySelector{SecretReference: xpv2.SecretReference{Namespace: ns, Name: name}, Key: key}
	}
	for _, tc := range []struct {
		name, mode string
		ref        *xpv2.SecretKeySelector
		want       string
	}{
		{"missing namespace", "verify-full", ref("", "ca", "bundle"), "requires name, namespace and key"},
		{"missing name", "verify-full", ref("one", "", "bundle"), "requires name, namespace and key"},
		{"missing key", "verify-full", ref("one", "ca", ""), "requires name, namespace and key"},
		{"other namespace cannot borrow secret", "verify-full", ref("two", "ca", "bundle"), "not found"},
		{"missing data key", "verify-full", ref("one", "ca", "absent"), "does not contain key"},
		{"empty PEM", "verify-full", ref("one", "ca", "empty"), "valid PEM certificate"},
		{"invalid PEM", "verify-full", ref("one", "ca", "bad"), "valid PEM certificate"},
		{"require rejected", "require", ref("one", "ca", "bundle"), "requires sslMode verify-ca or verify-full"},
		{"disabled rejected", "disable", ref("one", "ca", "bundle"), "requires sslMode verify-ca or verify-full"},
	} {
		t.Run(tc.name, func(t *testing.T) {
			_, err := LoadConfig(context.Background(), kube, "cluster/ProviderConfig/test", tc.mode, "db.example", tc.ref)
			if err == nil || !strings.Contains(err.Error(), tc.want) {
				t.Fatalf("want %q, got %v", tc.want, err)
			}
			if strings.Contains(err.Error(), "PRIVATE-MATERIAL") {
				t.Fatal("error disclosed Secret contents")
			}
		})
	}
	for _, mode := range []string{"", "require", "disable", "verify-ca", "verify-full"} {
		got, err := LoadConfig(context.Background(), nil, "unused", mode, "db.example", nil)
		if err != nil || got != mode {
			t.Fatalf("omitted selector changed mode %q: %q, %v", mode, got, err)
		}
	}
	modes := map[string]bool{}
	for _, identity := range []string{"cluster/ProviderConfig/same", "namespaced/ClusterProviderConfig/same", "namespaced/ProviderConfig/one/same", "namespaced/ProviderConfig/two/same"} {
		for _, mode := range []string{"verify-full", "verify-ca"} {
			got, err := LoadConfig(context.Background(), kube, identity, mode, "db.example", ref("one", "ca", "bundle"))
			if err != nil {
				t.Fatal(err)
			}
			if modes[got] {
				t.Fatalf("registration collision for %s/%s", identity, mode)
			}
			modes[got] = true
			if _, err := pq.NewConfig("host=db.example sslmode=" + got); err != nil {
				t.Fatalf("registered mode rejected by real driver: %v", err)
			}
			t.Cleanup(func() { _ = pq.RegisterTLSConfig(got, nil) })
		}
	}
	// Rotation replaces a stable per-config registration rather than growing a
	// new global registration on every reconcile or borrowing another config's CA.
	identity := "namespaced/ProviderConfig/one/rotated"
	before, err := LoadConfig(context.Background(), kube, identity, "verify-full", "db.example", ref("one", "ca", "bundle"))
	if err != nil {
		t.Fatal(err)
	}
	secret := &corev1.Secret{}
	if err := kube.Get(context.Background(), client.ObjectKey{Namespace: "one", Name: "ca"}, secret); err != nil {
		t.Fatal(err)
	}
	secret.Data["bundle"] = testCA(t)
	if err := kube.Update(context.Background(), secret); err != nil {
		t.Fatal(err)
	}
	after, err := LoadConfig(context.Background(), kube, identity, "verify-full", "db.example", ref("one", "ca", "bundle"))
	if err != nil || after != before {
		t.Fatalf("rotation: %q -> %q, %v", before, after, err)
	}
	_ = pq.RegisterTLSConfig(after, nil)
	unset, err := LoadConfig(context.Background(), kube, "default-mode", "", "db.example", ref("one", "ca", "bundle"))
	if err != nil {
		t.Fatal(err)
	}
	_ = pq.RegisterTLSConfig(unset, nil)
}
