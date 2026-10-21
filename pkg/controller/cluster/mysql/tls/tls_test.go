package tls

import (
	"context"
	"crypto/ecdsa"
	"crypto/elliptic"
	"crypto/rand"
	"crypto/tls"
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

	"github.com/crossplane-contrib/provider-sql/apis/cluster/mysql/v1alpha1"
)

const testConfigName = "aurora"

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
	got, err := LoadConfig(context.Background(), nil, "unused", nil, nil)
	if err != nil || got != nil {
		t.Errorf("nil mode: got %v err %v", got, err)
	}
}

func TestLoadConfigCAOnly(t *testing.T) {
	mode := tlsModeCustom
	key := RegisterKey(testConfigName)
	got, err := LoadConfig(context.Background(), secretClient(t, certPEM(t)), key, &mode, caOnly())
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || *got != key {
		t.Fatalf("register key: got %v", got)
	}
}

func TestLoadConfigClientPair(t *testing.T) {
	der, key := certificate(t)
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	rawKey, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	clientKey := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: rawKey})
	ca := certPEM(t)
	mode := tlsModeCustom
	cfg := caOnly()
	cfg.ClientCert.SecretRef = xpv2.SecretKeySelector{
		SecretReference: xpv2.SecretReference{Name: "tls", Namespace: "ns"},
		Key:             "client.pem",
	}
	cfg.ClientKey.SecretRef = xpv2.SecretKeySelector{
		SecretReference: xpv2.SecretReference{Name: "tls", Namespace: "ns"},
		Key:             "client-key.pem",
	}
	kube := &test.MockClient{
		MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
			secret, ok := obj.(*corev1.Secret)
			if !ok {
				t.Fatalf("unexpected %T", obj)
			}
			secret.Data = map[string][]byte{
				"global-bundle.pem": ca,
				"client.pem":        cert,
				"client-key.pem":    clientKey,
			}
			return nil
		}),
	}
	got, err := LoadConfig(context.Background(), kube, RegisterKey(testConfigName), &mode, cfg)
	if err != nil {
		t.Fatal(err)
	}
	if got == nil || *got != RegisterKey(testConfigName) {
		t.Fatalf("register key: got %v", got)
	}
}

func TestClientConfigVerifying(t *testing.T) {
	pool := x509.NewCertPool()
	if !pool.AppendCertsFromPEM(certPEM(t)) {
		t.Fatal("pem")
	}
	cfg := clientConfig(pool, nil, false)
	if cfg.InsecureSkipVerify || cfg.ServerName != "" || cfg.RootCAs == nil || len(cfg.Certificates) != 0 {
		t.Fatalf("verifying config: insecure=%v server=%q certs=%d", cfg.InsecureSkipVerify, cfg.ServerName, len(cfg.Certificates))
	}
	if cfg.MinVersion != tls.VersionTLS12 {
		t.Fatalf("min version %d", cfg.MinVersion)
	}

	der, key := certificate(t)
	cert := pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
	keyDER, err := x509.MarshalPKCS8PrivateKey(key)
	if err != nil {
		t.Fatal(err)
	}
	keyBytes := pem.EncodeToMemory(&pem.Block{Type: "PRIVATE KEY", Bytes: keyDER})
	pair, err := tls.X509KeyPair(cert, keyBytes)
	if err != nil {
		t.Fatal(err)
	}
	withClient := clientConfig(pool, []tls.Certificate{pair}, false)
	if len(withClient.Certificates) != 1 || withClient.ServerName != "" {
		t.Fatalf("client cert config: certs=%d server=%q", len(withClient.Certificates), withClient.ServerName)
	}
}

func TestLoadConfigClientCertWithoutKey(t *testing.T) {
	mode := tlsModeCustom
	cfg := caOnly()
	cfg.ClientCert.SecretRef = xpv2.SecretKeySelector{
		SecretReference: xpv2.SecretReference{Name: "tls", Namespace: "ns"},
		Key:             "client.pem",
	}
	_, err := LoadConfig(context.Background(), nil, RegisterKey(testConfigName), &mode, cfg)
	if err == nil || !strings.Contains(err.Error(), "must be set together") {
		t.Fatalf("got %v", err)
	}
}

func TestLoadConfigNonPEM(t *testing.T) {
	mode := tlsModeCustom
	_, err := LoadConfig(context.Background(), secretClient(t, []byte("not a certificate")), RegisterKey(testConfigName), &mode, caOnly())
	if err == nil || !strings.Contains(err.Error(), "cannot parse CA bundle") {
		t.Fatalf("got %v", err)
	}
}

func caOnly() *v1alpha1.TLSConfig {
	return &v1alpha1.TLSConfig{
		CACert: v1alpha1.TLSSecret{SecretRef: xpv2.SecretKeySelector{
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
	der, _ := certificate(t)
	return pem.EncodeToMemory(&pem.Block{Type: "CERTIFICATE", Bytes: der})
}

func certificate(t *testing.T) ([]byte, *ecdsa.PrivateKey) {
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
	return der, key
}
