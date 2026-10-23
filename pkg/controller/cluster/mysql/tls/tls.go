package tls

import (
	"context"
	"crypto/sha256"
	"crypto/tls"
	"crypto/x509"
	"fmt"

	"github.com/crossplane-contrib/provider-sql/apis/cluster/mysql/v1alpha1"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
	"github.com/go-sql-driver/mysql"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

// LoadConfig loads the TLS configuration when tls mode is set to custom and
// returns the tls name of registered configuration.
func LoadConfig(ctx context.Context, kube client.Client, providerConfigName string, mode *string, cfg *v1alpha1.TLSConfig) (*string, error) {
	if mode == nil || *mode != "custom" {
		if cfg != nil {
			return nil, fmt.Errorf("tlsConfig is allowed only when tls=custom")
		}
		return mode, nil
	}

	if err := validateTLSConfig(cfg); err != nil {
		return nil, err
	}

	tlsName := fmt.Sprintf("custom-cluster-%x", sha256.Sum256([]byte(providerConfigName)))
	err := registerTLS(ctx, kube, tlsName, cfg)
	if err != nil {
		return nil, err
	}
	return &tlsName, nil
}

func validateTLSConfig(cfg *v1alpha1.TLSConfig) error {
	if cfg == nil || cfg.CACert.SecretRef.Name == "" || cfg.CACert.SecretRef.Key == "" {
		return fmt.Errorf("tlsConfig.caCert is required when tls=custom")
	}
	cert, key := cfg.ClientCert.SecretRef, cfg.ClientKey.SecretRef
	hasCert := cert != (xpv2.SecretKeySelector{})
	hasKey := key != (xpv2.SecretKeySelector{})
	if hasCert != hasKey || (hasCert && (cert.Name == "" || cert.Key == "" || key.Name == "" || key.Key == "")) {
		return fmt.Errorf("tlsConfig.clientCert and clientKey must be provided together with complete secret references")
	}
	return nil
}

func registerTLS(ctx context.Context, kube client.Client, tlsName string, cfg *v1alpha1.TLSConfig) error {
	if cfg == nil {
		return nil
	}

	caCert, err := getSecret(ctx, kube, cfg.CACert.SecretRef)
	if err != nil {
		return fmt.Errorf("cannot get CA certificate: %w", err)
	}

	pool := x509.NewCertPool()
	if ok := pool.AppendCertsFromPEM(caCert); !ok {
		return fmt.Errorf("cannot append CA certificate to pool")
	}

	config := &tls.Config{
		MinVersion:         tls.VersionTLS12,
		RootCAs:            pool,
		InsecureSkipVerify: cfg.InsecureSkipVerify, //nolint:gosec // Explicit operator setting retained for compatibility.
	}
	if cfg.ClientCert.SecretRef.Name != "" {
		keyPair, err := getClientKeyPair(ctx, kube, cfg)
		if err != nil {
			return err
		}
		config.Certificates = []tls.Certificate{keyPair}
	}
	return mysql.RegisterTLSConfig(tlsName, config)
}

func getClientKeyPair(ctx context.Context, kube client.Client, cfg *v1alpha1.TLSConfig) (tls.Certificate, error) {
	cert, err := getSecret(ctx, kube, cfg.ClientCert.SecretRef)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("cannot get client certificate: %w", err)
	}

	key, err := getSecret(ctx, kube, cfg.ClientKey.SecretRef)
	if err != nil {
		return tls.Certificate{}, fmt.Errorf("cannot get client key: %w", err)
	}

	keyPair, err := tls.X509KeyPair(cert, key)
	if err != nil {
		return tls.Certificate{}, errors.Wrap(err, "cannot make client certificate")
	}
	return keyPair, nil
}

func getSecret(ctx context.Context, kube client.Client, sel xpv2.SecretKeySelector) ([]byte, error) {
	secret := &corev1.Secret{}
	if err := kube.Get(ctx, types.NamespacedName{
		Namespace: sel.Namespace,
		Name:      sel.Name}, secret); err != nil {
		return nil, fmt.Errorf("cannot get Secret %q in namespace %q: %w", sel.Name, sel.Namespace, err)
	}

	data, ok := secret.Data[sel.Key]
	if !ok {
		return nil, fmt.Errorf("key %q not found in Secret %q", sel.Key, sel.Name)
	}

	return data, nil
}
