package provider

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"

	"context"

	"github.com/crossplane-contrib/provider-sql/apis/namespaced/postgresql/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
	provErrors "github.com/crossplane-contrib/provider-sql/pkg/controller/namespaced/errors"
	"github.com/crossplane-contrib/provider-sql/pkg/controller/postgresql/connect"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ProviderInfo struct {
	ProviderConfigName string
	SecretData         map[string][]byte
	DefaultDatabase    string
	SSLMode            *string
}

type resolvedConfig struct {
	secretKey        *client.ObjectKey
	defaultDatabase  string
	sslMode          *string
	keyMapping       map[string]string
	source           string
	region           *string
	ca               *connect.CASecretRef
	defaultNamespace string
	registerKey      string
}

func GetProviderConfig(ctx context.Context, kube client.Client, mg resource.ModernManaged) (ProviderInfo, error) {
	resolved, err := resolveProviderConfig(ctx, kube, mg)
	if err != nil {
		return ProviderInfo{}, err
	}

	secretKey := resolved.secretKey
	if secretKey.Name == "" || secretKey.Namespace == "" {
		return ProviderInfo{}, provErrors.MissingSecretRefError()
	}

	s := &corev1.Secret{}
	err = kube.Get(ctx, *secretKey, s)
	if err != nil {
		return ProviderInfo{}, provErrors.GetSecretError(err)
	}

	secretData := xsql.RemapCredentialKeys(s.Data, resolved.keyMapping)
	effectiveSSL, err := connect.Prepare(ctx, kube, connect.Input{
		Source:           resolved.source,
		Region:           resolved.region,
		SSLMode:          resolved.sslMode,
		CA:               resolved.ca,
		DefaultNamespace: resolved.defaultNamespace,
		RegisterKey:      resolved.registerKey,
		Creds:            secretData,
	})
	if err != nil {
		return ProviderInfo{}, err
	}
	var sslMode *string
	if effectiveSSL != "" {
		sslMode = &effectiveSSL
	}

	return ProviderInfo{
		ProviderConfigName: mg.GetProviderConfigReference().Name,
		SecretData:         secretData,
		DefaultDatabase:    resolved.defaultDatabase,
		SSLMode:            sslMode,
	}, nil
}

func resolveProviderConfig(ctx context.Context, kube client.Client, mg resource.ModernManaged) (resolvedConfig, error) {
	switch mg.GetProviderConfigReference().Kind {
	case v1alpha1.ProviderConfigKind:
		return resolveNamespaced(ctx, kube, mg)
	case v1alpha1.ClusterProviderConfigKind:
		return resolveCluster(ctx, kube, mg)
	default:
		return resolvedConfig{}, provErrors.InvalidProviderConfigKindError(mg.GetProviderConfigReference().Kind)
	}
}

func resolveNamespaced(ctx context.Context, kube client.Client, mg resource.ModernManaged) (resolvedConfig, error) {
	providerConfig := &v1alpha1.ProviderConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name:      mg.GetProviderConfigReference().Name,
			Namespace: mg.GetNamespace(),
		},
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(providerConfig), providerConfig); err != nil {
		return resolvedConfig{}, provErrors.GetProviderConfigError(err)
	}

	resolved := resolvedConfig{
		secretKey: &client.ObjectKey{
			Name:      providerConfig.Spec.Credentials.ConnectionSecretRef.Name,
			Namespace: mg.GetNamespace(),
		},
		defaultDatabase:  providerConfig.Spec.DefaultDatabase,
		sslMode:          providerConfig.Spec.SSLMode,
		keyMapping:       providerConfig.Spec.Credentials.SecretKeyMapping.ToMap(),
		source:           string(providerConfig.Spec.Credentials.Source),
		region:           providerConfig.Spec.Credentials.Region,
		defaultNamespace: mg.GetNamespace(),
		registerKey:      "ns-" + mg.GetNamespace() + "-" + providerConfig.GetName(),
	}
	if providerConfig.Spec.TLSConfig != nil {
		ref := providerConfig.Spec.TLSConfig.CACert.SecretRef
		resolved.ca = connect.CA(ref.Name, ref.Namespace, ref.Key)
	}
	return resolved, nil
}

func resolveCluster(ctx context.Context, kube client.Client, mg resource.ModernManaged) (resolvedConfig, error) {
	providerConfig := &v1alpha1.ClusterProviderConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: mg.GetProviderConfigReference().Name,
		},
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(providerConfig), providerConfig); err != nil {
		return resolvedConfig{}, provErrors.GetClusterProviderConfigError(err)
	}

	resolved := resolvedConfig{
		secretKey: &client.ObjectKey{
			Name:      providerConfig.Spec.Credentials.ConnectionSecretRef.Name,
			Namespace: providerConfig.Spec.Credentials.ConnectionSecretRef.Namespace,
		},
		defaultDatabase: providerConfig.Spec.DefaultDatabase,
		sslMode:         providerConfig.Spec.SSLMode,
		keyMapping:      providerConfig.Spec.Credentials.SecretKeyMapping.ToMap(),
		source:          string(providerConfig.Spec.Credentials.Source),
		region:          providerConfig.Spec.Credentials.Region,
		registerKey:     "cpc-" + providerConfig.GetName(),
	}
	if providerConfig.Spec.TLSConfig != nil {
		ref := providerConfig.Spec.TLSConfig.CACert.SecretRef
		resolved.ca = connect.CA(ref.Name, ref.Namespace, ref.Key)
	}
	return resolved, nil
}
