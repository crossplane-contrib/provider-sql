package provider

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"

	"github.com/crossplane-contrib/provider-sql/apis/namespaced/mysql/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/mysql"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
	"github.com/crossplane-contrib/provider-sql/pkg/controller/namespaced/errors"
	"github.com/crossplane-contrib/provider-sql/pkg/controller/namespaced/mysql/tls"
)

type ProviderInfo struct {
	ProviderConfigName string
	TLSRegisterKey     string
	SecretData         map[string][]byte
	TLS                *string
	TLSConfig          *v1alpha1.TLSConfig
	// Cleartext is true when the credentials source is AWS IAM auth. The
	// MySQL client then sets allowCleartextPasswords so the driver will send
	// the token. Password logins leave it false.
	Cleartext bool
}

type resolvedConfig struct {
	secretKey   client.ObjectKey
	tlsMode     *string
	tlsConfig   *v1alpha1.TLSConfig
	keyMapping  map[string]string
	source      string
	region      *string
	registerKey string
}

func GetProviderConfig(ctx context.Context, kube client.Client, mg resource.ModernManaged) (ProviderInfo, error) {
	resolved, err := resolveProviderConfig(ctx, kube, mg)
	if err != nil {
		return ProviderInfo{}, err
	}
	data, cleartext, err := loadCredentials(ctx, kube, resolved)
	if err != nil {
		return ProviderInfo{}, err
	}
	return ProviderInfo{
		ProviderConfigName: mg.GetProviderConfigReference().Name,
		TLSRegisterKey:     resolved.registerKey,
		SecretData:         data,
		TLS:                resolved.tlsMode,
		TLSConfig:          resolved.tlsConfig,
		Cleartext:          cleartext,
	}, nil
}

func resolveProviderConfig(ctx context.Context, kube client.Client, mg resource.ModernManaged) (resolvedConfig, error) {
	switch mg.GetProviderConfigReference().Kind {
	case v1alpha1.ProviderConfigKind:
		return resolveNamespaced(ctx, kube, mg)
	case v1alpha1.ClusterProviderConfigKind:
		return resolveCluster(ctx, kube, mg)
	default:
		return resolvedConfig{}, errors.InvalidProviderConfigKindError(mg.GetProviderConfigReference().Kind)
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
		return resolvedConfig{}, errors.GetProviderConfigError(err)
	}
	return resolvedConfig{
		secretKey: client.ObjectKey{
			Name:      providerConfig.Spec.Credentials.ConnectionSecretRef.Name,
			Namespace: mg.GetNamespace(),
		},
		tlsMode:     providerConfig.Spec.TLS,
		tlsConfig:   providerConfig.Spec.TLSConfig,
		keyMapping:  providerConfig.Spec.Credentials.SecretKeyMapping.ToMap(),
		source:      string(providerConfig.Spec.Credentials.Source),
		region:      providerConfig.Spec.Credentials.Region,
		registerKey: tls.ProviderConfigKey(mg.GetNamespace(), providerConfig.GetName()),
	}, nil
}

func resolveCluster(ctx context.Context, kube client.Client, mg resource.ModernManaged) (resolvedConfig, error) {
	clusterProviderConfig := &v1alpha1.ClusterProviderConfig{
		ObjectMeta: metav1.ObjectMeta{
			Name: mg.GetProviderConfigReference().Name,
		},
	}
	if err := kube.Get(ctx, client.ObjectKeyFromObject(clusterProviderConfig), clusterProviderConfig); err != nil {
		return resolvedConfig{}, errors.GetClusterProviderConfigError(err)
	}
	return resolvedConfig{
		secretKey: client.ObjectKey{
			Name:      clusterProviderConfig.Spec.Credentials.ConnectionSecretRef.Name,
			Namespace: clusterProviderConfig.Spec.Credentials.ConnectionSecretRef.Namespace,
		},
		tlsMode:     clusterProviderConfig.Spec.TLS,
		tlsConfig:   clusterProviderConfig.Spec.TLSConfig,
		keyMapping:  clusterProviderConfig.Spec.Credentials.SecretKeyMapping.ToMap(),
		source:      string(clusterProviderConfig.Spec.Credentials.Source),
		region:      clusterProviderConfig.Spec.Credentials.Region,
		registerKey: tls.ClusterProviderConfigKey(clusterProviderConfig.GetName()),
	}, nil
}

func loadCredentials(ctx context.Context, kube client.Client, resolved resolvedConfig) (map[string][]byte, bool, error) {
	if resolved.secretKey.Name == "" || resolved.secretKey.Namespace == "" {
		return nil, false, errors.MissingSecretRefError()
	}
	s := &corev1.Secret{}
	if err := kube.Get(ctx, resolved.secretKey, s); err != nil {
		return nil, false, errors.GetSecretError(err)
	}
	data := xsql.RemapCredentialKeys(s.Data, resolved.keyMapping)
	cleartext, err := mysql.ApplyIAM(ctx, resolved.source, resolved.region, data)
	if err != nil {
		return nil, false, err
	}
	return data, cleartext, nil
}
