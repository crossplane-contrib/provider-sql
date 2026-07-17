package provider

import (
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"

	"context"

	"github.com/crossplane-contrib/provider-sql/apis/namespaced/mysql/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/awsiam"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/mysql"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
	"github.com/crossplane-contrib/provider-sql/pkg/controller/namespaced/errors"

	"sigs.k8s.io/controller-runtime/pkg/client"
)

type ProviderInfo struct {
	ProviderConfigName string
	SecretData         map[string][]byte
	TLS                *string
	TLSConfig          *v1alpha1.TLSConfig
	// Cleartext is true when the connection uses AWS IAM auth, requiring the
	// MySQL client to set allowCleartextPasswords=true.
	Cleartext bool
}

// injectIAM generates and injects an AWS IAM auth token. It is a package
// variable so tests can replace it with a stub.
var injectIAM = awsiam.Inject

func GetProviderConfig(ctx context.Context, kube client.Client, mg resource.ModernManaged) (ProviderInfo, error) {
	var (
		secretKey  *client.ObjectKey
		tlsMode    *string
		tlsConfig  *v1alpha1.TLSConfig
		keyMapping map[string]string
		source     v1alpha1.MySQLConnectionSecretSource
		region     *string
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
		keyMapping = providerConfig.Spec.Credentials.SecretKeyMapping.ToMap()
		source = providerConfig.Spec.Credentials.Source
		region = providerConfig.Spec.Credentials.Region

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
		keyMapping = clusterProviderConfig.Spec.Credentials.SecretKeyMapping.ToMap()
		source = clusterProviderConfig.Spec.Credentials.Source
		region = clusterProviderConfig.Spec.Credentials.Region

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

	secretData := xsql.RemapCredentialKeys(s.Data, keyMapping)

	cleartext := false
	if source == v1alpha1.CredentialsSourceAWSIAMAuth {
		if err := injectIAM(ctx, region, secretData); err != nil {
			return ProviderInfo{}, errors.GenerateIAMTokenError(err)
		}
		cleartext = true
		// IAM auth requires TLS. The operator supplies the RDS CA (e.g. via
		// trust-manager or a mounted secret); honour a verifying tls mode if set,
		// otherwise fall back to an encrypted (unverified) connection.
		tlsMode = mysql.EnsureTLS(tlsMode)
	}

	return ProviderInfo{
		ProviderConfigName: mg.GetProviderConfigReference().Name,
		SecretData:         secretData,
		TLS:                tlsMode,
		TLSConfig:          tlsConfig,
		Cleartext:          cleartext,
	}, nil
}
