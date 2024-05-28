package tls

import (
	"context"
	"crypto/tls"
	"crypto/x509"
	"fmt"
	"github.com/crossplane-contrib/provider-sql/apis/mysql/v1alpha1"
	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/go-sql-driver/mysql"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"log"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

func LoadConfig(ctx context.Context, kube client.Client, tlsMode *string, tlsConfig *v1alpha1.TLSConfig) error {
	if tlsMode == nil || *tlsMode != "custom" {
		if tlsConfig != nil {
			return fmt.Errorf("tlsConfig is allowed only when tls=custom")
		}
		return nil
	}

	if tlsConfig == nil ||
		tlsConfig.CACert.SecretRef.Name == "" ||
		tlsConfig.CACert.SecretRef.Key == "" ||
		tlsConfig.ClientCert.SecretRef.Name == "" ||
		tlsConfig.ClientCert.SecretRef.Key == "" ||
		tlsConfig.ClientKey.SecretRef.Name == "" ||
		tlsConfig.ClientKey.SecretRef.Key == "" {
		return fmt.Errorf("tlsConfig is required when tls=custom")
	}

	caCertData, err := getSecret(ctx, kube, tlsConfig.CACert.SecretRef)
	if err != nil {
		return fmt.Errorf("cannot get CA certificate: %w", err)
	}

	rootCertPool := x509.NewCertPool()
	if ok := rootCertPool.AppendCertsFromPEM(caCertData); !ok {
		log.Fatal("Failed to append PEM.")
	}

	clientCertData, err := getSecret(ctx, kube, tlsConfig.ClientCert.SecretRef)
	if err != nil {
		return fmt.Errorf("cannot get client certificate: %w", err)
	}

	clientKeyData, err := getSecret(ctx, kube, tlsConfig.ClientKey.SecretRef)
	if err != nil {
		return fmt.Errorf("cannot get client key: %w", err)
	}

	clientCert, err := tls.X509KeyPair(clientCertData, clientKeyData)
	if err != nil {
		return errors.Wrap(err, "cannot make client certificate")
	}

	if err := mysql.RegisterTLSConfig("custom", &tls.Config{
		RootCAs:      rootCertPool,
		Certificates: []tls.Certificate{clientCert},
	}); err != nil {
		return errors.Wrap(err, "cannot register custom TLS config")
	}

	return nil
}

func getSecret(ctx context.Context, kube client.Client, selector xpv1.SecretKeySelector) ([]byte, error) {
	secret := &corev1.Secret{}
	if err := kube.Get(ctx, types.NamespacedName{
		Namespace: selector.Namespace,
		Name:      selector.Name}, secret); err != nil {
		return nil, fmt.Errorf("cannot get Secret %q in namespace %q: %w", selector.Name, selector.Namespace, err)
	}

	data, ok := secret.Data[selector.Key]
	if !ok {
		return nil, fmt.Errorf("key %q not found in Secret %q", selector.Key, selector.Name)
	}

	return data, nil
}
