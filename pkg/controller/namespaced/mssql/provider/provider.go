package provider

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"

	"context"

	"github.com/crossplane-contrib/provider-sql/apis/namespaced/mssql/v1alpha1"
	provErrors "github.com/crossplane-contrib/provider-sql/pkg/controller/namespaced/errors"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ProviderInfo struct {
	ProviderConfigName string
	SecretData         map[string][]byte
}

func GetProviderConfig(ctx context.Context, kube client.Client, mg resource.ModernManaged) (ProviderInfo, error) {
	var secretKey *client.ObjectKey

	switch mg.GetProviderConfigReference().Kind {
	case v1alpha1.ProviderConfigKind:
		providerConfig := &v1alpha1.ProviderConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name:      mg.GetProviderConfigReference().Name,
				Namespace: mg.GetNamespace(),
			},
		}

		if err := kube.Get(ctx, client.ObjectKeyFromObject(providerConfig), providerConfig); err != nil {
			return ProviderInfo{}, provErrors.GetProviderConfigError(err)
		}

		secretKey = &client.ObjectKey{
			Name:      providerConfig.Spec.Credentials.ConnectionSecretRef.Name,
			Namespace: mg.GetNamespace(),
		}

	case v1alpha1.ClusterProviderConfigKind:
		clusterProviderConfig := &v1alpha1.ClusterProviderConfig{
			ObjectMeta: metav1.ObjectMeta{
				Name: mg.GetProviderConfigReference().Name,
			},
		}

		if err := kube.Get(ctx, client.ObjectKeyFromObject(clusterProviderConfig), clusterProviderConfig); err != nil {
			return ProviderInfo{}, provErrors.GetClusterProviderConfigError(err)
		}

		secretKey = &client.ObjectKey{
			Name:      clusterProviderConfig.Spec.Credentials.ConnectionSecretRef.Name,
			Namespace: clusterProviderConfig.Spec.Credentials.ConnectionSecretRef.Namespace,
		}

	default:
		return ProviderInfo{}, provErrors.InvalidProviderConfigKindError(mg.GetProviderConfigReference().Kind)
	}

	if secretKey.Name == "" || secretKey.Namespace == "" {
		return ProviderInfo{}, provErrors.MissingSecretRefError()
	}

	s := &corev1.Secret{}
	err := kube.Get(ctx, *secretKey, s)
	if err != nil {
		return ProviderInfo{}, provErrors.GetSecretError(err)
	}

	return ProviderInfo{
		ProviderConfigName: mg.GetProviderConfigReference().Name,
		SecretData:         s.Data,
	}, nil
}
