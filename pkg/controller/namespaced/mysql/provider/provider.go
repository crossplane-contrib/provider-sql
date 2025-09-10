package provider

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"

	"context"

	"github.com/crossplane-contrib/provider-sql/apis/namespaced/mysql/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/controller/namespaced/errors"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ProviderInfo struct {
	ProviderConfigName string
	SecretData         map[string][]byte
	TLS                *string
	TLSConfig          *v1alpha1.TLSConfig
}

func GetProviderConfig(ctx context.Context, kube client.Client, mg resource.ModernManaged) (ProviderInfo, error) {
	var (
		secretKey *client.ObjectKey
		tlsMode   *string
		tlsConfig *v1alpha1.TLSConfig
	)

	switch mg.GetProviderConfigReference().Kind {
	case v1alpha1.ProviderConfigKind:
		providerConfig := &v1alpha1.ProviderConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mg.GetProviderConfigReference().Name,
				Namespace: mg.GetNamespace(),
			},
		}

		if err := kube.Get(ctx, client.ObjectKeyFromObject(providerConfig), providerConfig); err != nil {
			return ProviderInfo{}, errors.GetProviderConfigError(err)
		}

		secretKey = &client.ObjectKey{
			Name:      providerConfig.Spec.Credentials.ConnectionSecretRef.Name,
			Namespace: mg.GetNamespace(),
		}
		tlsMode = providerConfig.Spec.TLS
		tlsConfig = providerConfig.Spec.TLSConfig

	case v1alpha1.ClusterProviderConfigKind:
		clusterProviderConfig := &v1alpha1.ClusterProviderConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name: mg.GetProviderConfigReference().Name,
			},
		}

		if err := kube.Get(ctx, client.ObjectKeyFromObject(clusterProviderConfig), clusterProviderConfig); err != nil {
			return ProviderInfo{}, errors.GetClusterProviderConfigError(err)
		}

		secretKey = &client.ObjectKey{
			Name:      clusterProviderConfig.Spec.Credentials.ConnectionSecretRef.Name,
			Namespace: clusterProviderConfig.Spec.Credentials.ConnectionSecretRef.Namespace,
		}
		tlsMode = clusterProviderConfig.Spec.TLS
		tlsConfig = clusterProviderConfig.Spec.TLSConfig

	default:
		return ProviderInfo{}, errors.InvalidProviderConfigKindError(mg.GetProviderConfigReference().Kind)
	}

	if secretKey.Name == "" || secretKey.Namespace == "" {
		return ProviderInfo{}, errors.MissingSecretRefError()
	}

	s := &corev1.Secret{}
	err := kube.Get(ctx, *secretKey, s)
	if err != nil {
		return ProviderInfo{}, errors.GetSecretError(err)
	}

	return ProviderInfo{
		ProviderConfigName: mg.GetProviderConfigReference().Name,
		SecretData:         s.Data,
		TLS:                tlsMode,
		TLSConfig:          tlsConfig,
	}, nil
}
