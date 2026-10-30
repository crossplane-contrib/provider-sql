package provider

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"

	"context"
	"fmt"

	postgresqltls "github.com/crossplane-contrib/provider-sql/pkg/controller/postgresql/tls"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/crossplane-contrib/provider-sql/apis/namespaced/postgresql/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/awsiam"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/postgresql"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
	provErrors "github.com/crossplane-contrib/provider-sql/pkg/controller/namespaced/errors"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ProviderInfo struct {
	PasswordProvider   xsql.PasswordProvider
	ProviderConfigName string
	SecretData         map[string][]byte
	DefaultDatabase    string
	SSLMode            *string
}

// newIAMToken prepares a token provider used for every physical connection.
// Tests replace this factory to avoid AWS access.
var newIAMToken = awsiam.NewTokenProvider

func GetProviderConfig(ctx context.Context, kube client.Client, mg resource.ModernManaged) (ProviderInfo, error) {
	var (
		secretKey       *client.ObjectKey
		defaultDatabase string
		sslMode         *string
		keyMapping      map[string]string
		source          v1alpha1.PostgreSQLConnectionSource
		region          *string
		caRef           *xpv2.SecretKeySelector
		configIdentity  string
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
			return ProviderInfo{}, provErrors.GetProviderConfigError(err)
		}

		secretKey = &client.ObjectKey{
			Name:      providerConfig.Spec.Credentials.ConnectionSecretRef.Name,
			Namespace: mg.GetNamespace(),
		}

		defaultDatabase = providerConfig.Spec.DefaultDatabase
		sslMode = providerConfig.Spec.SSLMode
		keyMapping = providerConfig.Spec.Credentials.SecretKeyMapping.ToMap()
		source = providerConfig.Spec.Credentials.Source
		region = providerConfig.Spec.Credentials.Region
		configIdentity = "namespaced/ProviderConfig/" + providerConfig.Namespace + "/" + providerConfig.Name
		if providerConfig.Spec.TLSConfig != nil {
			caRef = providerConfig.Spec.TLSConfig.CACert.SecretRef.ToSecretKeySelector(providerConfig.Namespace)
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

		defaultDatabase = clusterProviderConfig.Spec.DefaultDatabase
		sslMode = clusterProviderConfig.Spec.SSLMode
		keyMapping = clusterProviderConfig.Spec.Credentials.SecretKeyMapping.ToMap()
		source = clusterProviderConfig.Spec.Credentials.Source
		region = clusterProviderConfig.Spec.Credentials.Region
		configIdentity = "namespaced/ClusterProviderConfig/" + clusterProviderConfig.Name
		if clusterProviderConfig.Spec.TLSConfig != nil {
			caRef = &clusterProviderConfig.Spec.TLSConfig.CACert.SecretRef
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

	secretData := xsql.RemapCredentialKeys(s.Data, keyMapping)

	return configureConnection(ctx, kube, ProviderInfo{
		ProviderConfigName: mg.GetProviderConfigReference().Name,
		SecretData:         secretData,
		DefaultDatabase:    defaultDatabase,
		SSLMode:            sslMode,
	}, source, region, configIdentity, caRef)
}

func configureConnection(ctx context.Context, kube client.Client, info ProviderInfo, source v1alpha1.PostgreSQLConnectionSource, region *string, identity string, caRef *xpv2.SecretKeySelector) (ProviderInfo, error) {
	if source == v1alpha1.CredentialsSourceAWSIAMAuth {
		password, err := newIAMToken(ctx, region, info.SecretData)
		if err != nil {
			return ProviderInfo{}, provErrors.GenerateIAMTokenError(err)
		}
		info.PasswordProvider = password
		mode := postgresql.EnsureTLS(clients.ToString(info.SSLMode))
		info.SSLMode = &mode
	}
	mode, err := postgresqltls.LoadConfig(ctx, kube, identity, clients.ToString(info.SSLMode), string(info.SecretData[xpv2.CredentialsSecretEndpointKey]), caRef)
	if err != nil {
		return ProviderInfo{}, fmt.Errorf("cannot configure PostgreSQL TLS: %w", err)
	}
	if caRef != nil {
		info.SSLMode = &mode
	}
	return info, nil
}
