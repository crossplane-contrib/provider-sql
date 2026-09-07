package provider

import (
	"context"

	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"

	"github.com/crossplane-contrib/provider-sql/apis/namespaced/postgresql/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
	provErrors "github.com/crossplane-contrib/provider-sql/pkg/controller/namespaced/errors"
)

const defaultAzurePostgreSQLTokenScope = "https://ossrdbms-aad.database.windows.net/.default"

type ProviderInfo struct {
	ProviderConfigName string
	SecretData         map[string][]byte
	DefaultDatabase    string
	SSLMode            *string
}

type providerConfigDetails struct {
	secretKey       client.ObjectKey
	defaultDatabase string
	sslMode         *string
	keyMapping      map[string]string
	authMode        xsql.AuthenticationMode
	tokenScope      string
}

func GetProviderConfig(ctx context.Context, kube client.Client, mg resource.ModernManaged) (ProviderInfo, error) {
	details, err := getProviderConfigDetails(ctx, kube, mg)
	if err != nil {
		return ProviderInfo{}, err
	}
	if details.secretKey.Name == "" || details.secretKey.Namespace == "" {
		return ProviderInfo{}, provErrors.MissingSecretRefError()
	}

	s := &corev1.Secret{}
	if err := kube.Get(ctx, details.secretKey, s); err != nil {
		return ProviderInfo{}, provErrors.GetSecretError(err)
	}

	return ProviderInfo{
		ProviderConfigName: mg.GetProviderConfigReference().Name,
		SecretData:         xsql.WithAuthentication(xsql.RemapCredentialKeys(s.Data, details.keyMapping), details.authMode, details.tokenScope),
		DefaultDatabase:    details.defaultDatabase,
		SSLMode:            details.sslMode,
	}, nil
}

func getProviderConfigDetails(ctx context.Context, kube client.Client, mg resource.ModernManaged) (providerConfigDetails, error) {
	ref := mg.GetProviderConfigReference()
	switch ref.Kind {
	case v1alpha1.ProviderConfigKind:
		return getNamespacedProviderConfigDetails(ctx, kube, ref.Name, mg.GetNamespace())
	case v1alpha1.ClusterProviderConfigKind:
		return getClusterProviderConfigDetails(ctx, kube, ref.Name)
	default:
		return providerConfigDetails{}, provErrors.InvalidProviderConfigKindError(ref.Kind)
	}
}

func getNamespacedProviderConfigDetails(ctx context.Context, kube client.Client, name, namespace string) (providerConfigDetails, error) {
	config := &v1alpha1.ProviderConfig{}
	if err := kube.Get(ctx, client.ObjectKey{Name: name, Namespace: namespace}, config); err != nil {
		return providerConfigDetails{}, provErrors.GetProviderConfigError(err)
	}
	credentials := config.Spec.Credentials
	return newProviderConfigDetails(
		client.ObjectKey{Name: credentials.ConnectionSecretRef.Name, Namespace: namespace},
		config.Spec.DefaultDatabase,
		config.Spec.SSLMode,
		credentials.SecretKeyMapping,
		credentials.Source,
		credentials.AzureWorkloadIdentity,
	), nil
}

func getClusterProviderConfigDetails(ctx context.Context, kube client.Client, name string) (providerConfigDetails, error) {
	config := &v1alpha1.ClusterProviderConfig{}
	if err := kube.Get(ctx, client.ObjectKey{Name: name}, config); err != nil {
		return providerConfigDetails{}, provErrors.GetClusterProviderConfigError(err)
	}
	credentials := config.Spec.Credentials
	return newProviderConfigDetails(
		client.ObjectKey{Name: credentials.ConnectionSecretRef.Name, Namespace: credentials.ConnectionSecretRef.Namespace},
		config.Spec.DefaultDatabase,
		config.Spec.SSLMode,
		credentials.SecretKeyMapping,
		credentials.Source,
		credentials.AzureWorkloadIdentity,
	), nil
}

func newProviderConfigDetails(secretKey client.ObjectKey, defaultDatabase string, sslMode *string, keyMapping *v1alpha1.SecretKeyMapping, source v1alpha1.PostgreSQLConnectionSource, azureWorkloadIdentity *v1alpha1.AzureWorkloadIdentityCredentials) providerConfigDetails {
	authMode, tokenScope := azureAuthentication(source, azureWorkloadIdentity)
	return providerConfigDetails{
		secretKey:       secretKey,
		defaultDatabase: defaultDatabase,
		sslMode:         sslMode,
		keyMapping:      keyMapping.ToMap(),
		authMode:        authMode,
		tokenScope:      tokenScope,
	}
}

func azureAuthentication(source v1alpha1.PostgreSQLConnectionSource, azureWorkloadIdentity *v1alpha1.AzureWorkloadIdentityCredentials) (xsql.AuthenticationMode, string) {
	if source != v1alpha1.CredentialsSourceAzureWorkloadIdentity {
		return xsql.AuthenticationModePassword, ""
	}
	if azureWorkloadIdentity == nil || azureWorkloadIdentity.TokenScope == "" {
		return xsql.AuthenticationModeAzureWorkloadIdentity, defaultAzurePostgreSQLTokenScope
	}
	return xsql.AuthenticationModeAzureWorkloadIdentity, azureWorkloadIdentity.TokenScope
}
