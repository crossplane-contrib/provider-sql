package tls

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"

	namespacedv1alpha1 "github.com/crossplane-contrib/provider-sql/apis/namespaced/mysql/v1alpha1"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
	"github.com/go-sql-driver/mysql"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

const (
	tlsModeCustom = "custom"

	errCARequired = "tlsConfig.caCert name, namespace, and key are required when tls=custom"
	errClientPair = "clientCert and clientKey must be set together"
	errParseCA    = "cannot parse CA bundle from tlsConfig.caCert"
)

// ProviderConfigKey is the go-sql-driver TLS config name for a namespaced
// ProviderConfig. The namespace is part of the key so two namespaces do not
// share one trust pool. The driver rejects the reserved names true, false,
// skip-verify, and preferred.
func ProviderConfigKey(namespace, name string) string {
	return "ns-" + namespace + "-" + name
}

// ClusterProviderConfigKey is the go-sql-driver TLS config name for a
// ClusterProviderConfig.
func ClusterProviderConfigKey(name string) string {
	return "cpc-" + name
}

// LoadConfig registers tls=custom and returns the name to put in the DSN.
// Any other mode, including an explicit "false", is returned unchanged.
// registerKey is the name passed to mysql.RegisterTLSConfig.
func LoadConfig(
	ctx context.Context,
	kube client.Client,
	registerKey string,
	tlsMode *string,
	tlsConfig *namespacedv1alpha1.TLSConfig,
) (*string, error) {
	if tlsMode == nil || *tlsMode != tlsModeCustom {
		if tlsConfig != nil {
			return nil, fmt.Errorf("tlsConfig is allowed only when tls=custom")
		}
		return tlsMode, nil
	}

	if err := validateTLSConfig(tlsConfig); err != nil {
		return nil, err
	}
	if err := registerTLS(ctx, kube, registerKey, tlsConfig); err != nil {
		return nil, err
	}
	return &registerKey, nil
}

func validateTLSConfig(cfg *namespacedv1alpha1.TLSConfig) error {
	if cfg == nil || !secretRefComplete(cfg.CACert.SecretRef) {
		return errors.New(errCARequired)
	}
	if secretRefEmpty(cfg.ClientCert.SecretRef) && secretRefEmpty(cfg.ClientKey.SecretRef) {
		return nil
	}
	if !secretRefComplete(cfg.ClientCert.SecretRef) || !secretRefComplete(cfg.ClientKey.SecretRef) {
		return errors.New(errClientPair)
	}
	return nil
}

func secretRefComplete(sel xpv2.SecretKeySelector) bool {
	return sel.Name != "" && sel.Namespace != "" && sel.Key != ""
}

func secretRefEmpty(sel xpv2.SecretKeySelector) bool {
	return sel.Name == "" && sel.Namespace == "" && sel.Key == ""
}

func registerTLS(ctx context.Context, kube client.Client, tlsName string, cfg *namespacedv1alpha1.TLSConfig) error {
	if cfg == nil {
		return nil
	}

	caCert, err := getSecret(ctx, kube, cfg.CACert.SecretRef)
	if err != nil {
		return fmt.Errorf("cannot get CA certificate: %w", err)
	}

	pool := x509.NewCertPool()
	if ok := pool.AppendCertsFromPEM(caCert); !ok {
		return errors.New(errParseCA)
	}

	var certs []tls.Certificate
	if secretRefComplete(cfg.ClientCert.SecretRef) {
		keyPair, err := getClientKeyPair(ctx, kube, cfg)
		if err != nil {
			return err
		}
		certs = []tls.Certificate{keyPair}
	}

	// ServerName stays empty. go-sql-driver sets it from the endpoint when
	// verification is on, which is the hostname check.
	return mysql.RegisterTLSConfig(tlsName, clientConfig(pool, certs, cfg.InsecureSkipVerify))
}

func clientConfig(pool *x509.CertPool, certs []tls.Certificate, insecure bool) *tls.Config {
	return &tls.Config{
		MinVersion:         tls.VersionTLS12,
		RootCAs:            pool,
		Certificates:       certs,
		InsecureSkipVerify: insecure, //nolint:gosec // Set only when tlsConfig.insecureSkipVerify is true.
	}
}

func getClientKeyPair(ctx context.Context, kube client.Client, cfg *namespacedv1alpha1.TLSConfig) (tls.Certificate, error) {
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
