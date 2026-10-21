package tls

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

	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/test"

	namespacedv1alpha1 "github.com/crossplane-contrib/provider-sql/apis/namespaced/mysql/v1alpha1"
)

const (
	testNamespace  = "default"
	testConfigName = "aurora"
)

func TestRegisterKeys(t *testing.T) {
	if got := ProviderConfigKey(testNamespace, testConfigName); got != "ns-default-aurora" {
		t.Errorf("namespaced key: %s", got)
	}
	if got := ClusterProviderConfigKey(testConfigName); got != "cpc-aurora" {
		t.Errorf("cluster key: %s", got)
	}
}

func TestLoadConfigPassthrough(t *testing.T) {
	for _, mode := range []string{"true", "skip-verify", "preferred", "false"} {
		got, err := LoadConfig(context.Background(), nil, "unused", &mode, nil)
		if err != nil {
			t.Fatalf("mode %s: %v", mode, err)
		}
		if got == nil || *got != mode {
			t.Errorf("mode %s: got %v", mode, got)
		}
	}
}

func TestLoadConfigCAOnly(t *testing.T) {
	mode := tlsModeCustom
	key := ProviderConfigKey(testNamespace, testConfigName)
	got, err := LoadConfig(context.Background(), secretClient(t, certPEM(t)), key, &mode, caOnly())
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || *got != key {
		t.Fatalf("register key: got %v", got)
	}
	cfg := clientConfig(poolFrom(t, certPEM(t)), nil, false)
	if cfg.InsecureSkipVerify || cfg.ServerName != "" || cfg.RootCAs == nil || len(cfg.Certificates) != 0 {
		t.Fatalf("verifying config: insecure=%v server=%q certs=%d", cfg.InsecureSkipVerify, cfg.ServerName, len(cfg.Certificates))
	}
}

func TestLoadConfigClientCertWithoutKey(t *testing.T) {
	mode := tlsModeCustom
	cfg := caOnly()
	cfg.ClientCert.SecretRef = xpv2.SecretKeySelector{
		SecretReference: xpv2.SecretReference{Name: "tls", Namespace: "ns"},
		Key:             "client.pem",
	}
	_, err := LoadConfig(context.Background(), nil, ProviderConfigKey(testNamespace, testConfigName), &mode, cfg)
	if err == nil || !strings.Contains(err.Error(), "must be set together") {
		t.Fatalf("got %v", err)
	}
}

func TestLoadConfigNonPEM(t *testing.T) {
	mode := tlsModeCustom
	_, err := LoadConfig(context.Background(), secretClient(t, []byte("not a certificate")), ProviderConfigKey(testNamespace, testConfigName), &mode, caOnly())
	if err == nil || !strings.Contains(err.Error(), "cannot parse CA bundle") {
		t.Fatalf("got %v", err)
	}
}

func poolFrom(t *testing.T, pemBytes []byte) *x509.CertPool {
	t.Helper()
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(pemBytes) {
		t.Fatal("pem")
	}
	return pool
}

func caOnly() *namespacedv1alpha1.TLSConfig {
	return &namespacedv1alpha1.TLSConfig{
		CACert: namespacedv1alpha1.TLSSecret{SecretRef: xpv2.SecretKeySelector{
			SecretReference: xpv2.SecretReference{Name: "amazon-rds-ca", Namespace: "crossplane-system"},
			Key:             "global-bundle.pem",
		}},
	}
}

func secretClient(t *testing.T, pemBytes []byte) client.Client {
	t.Helper()
	return &test.MockClient{
		MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
			secret, ok := obj.(*corev1.Secret)
			if !ok {
				t.Fatalf("unexpected %T", obj)
			}
			secret.Data = map[string][]byte{"global-bundle.pem": pemBytes}
			return nil
		}),
	}
}

func certPEM(t *testing.T) []byte {
	t.Helper()
	key, err := ecdsa.GenerateKey(elliptic.P256(), rand.Reader)
	if err != nil {
		t.Fatal(err)
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
		t.Fatal(err)
	}
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}
