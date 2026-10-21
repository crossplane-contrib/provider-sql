package provider

import (
	"context"
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/crossplane-contrib/provider-sql/apis/namespaced/mysql/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/awsiam"
	"github.com/crossplane-contrib/provider-sql/pkg/controller/namespaced/mysql/tls"
)

func TestGetProviderConfigAWSIAMAuth(t *testing.T) {
	orig := awsiam.InjectIAM
	awsiam.InjectIAM = func(_ context.Context, _ *string, creds map[string][]byte) error {
		creds[xpv2.CredentialsSecretPasswordKey] = []byte("iam-token")
		return nil
	}
	t.Cleanup(func() { awsiam.InjectIAM = orig })

	conn := map[string][]byte{
		xpv2.CredentialsSecretEndpointKey: []byte("db.example.rds.amazonaws.com"),
		xpv2.CredentialsSecretPortKey:     []byte("3306"),
		xpv2.CredentialsSecretUserKey:     []byte("crossplane_admin"),
	}
	for _, mode := range []string{"true", "skip-verify", "preferred", "custom", "false"} {
		t.Run("ProviderConfig/"+mode, func(t *testing.T) {
			info := getInfo(t, &test.MockClient{
				MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
					switch o := obj.(type) {
					case *v1alpha1.ProviderConfig:
						o.Spec.Credentials.Source = v1alpha1.CredentialsSourceAWSIAMAuth
						o.Spec.Credentials.ConnectionSecretRef = xpv2.LocalSecretReference{Name: "s"}
						o.Spec.TLS = &mode
					case *corev1.Secret:
						o.Data = conn
					}
					return nil
				}),
			}, managed("default", v1alpha1.ProviderConfigKind))
			assertIAM(t, info, mode, tls.ProviderConfigKey("default", "aurora"))
		})

		t.Run("ClusterProviderConfig/"+mode, func(t *testing.T) {
			info := getInfo(t, &test.MockClient{
				MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
					switch o := obj.(type) {
					case *v1alpha1.ClusterProviderConfig:
						o.Spec.Credentials.Source = v1alpha1.CredentialsSourceAWSIAMAuth
						o.Spec.Credentials.ConnectionSecretRef = xpv2.SecretReference{Namespace: "crossplane-system", Name: "s"}
						o.Spec.TLS = &mode
					case *corev1.Secret:
						o.Data = conn
					}
					return nil
				}),
			}, managed("default", v1alpha1.ClusterProviderConfigKind))
			assertIAM(t, info, mode, tls.ClusterProviderConfigKey("aurora"))
		})
	}
}

func TestGetProviderConfigPassword(t *testing.T) {
	disabled := "false"
	info := getInfo(t, &test.MockClient{
		MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
			switch o := obj.(type) {
			case *v1alpha1.ProviderConfig:
				o.Spec.Credentials.Source = v1alpha1.CredentialsSourceMySQLConnectionSecret
				o.Spec.Credentials.ConnectionSecretRef = xpv2.LocalSecretReference{Name: "s"}
				o.Spec.TLS = &disabled
			case *corev1.Secret:
				o.Data = map[string][]byte{
					xpv2.CredentialsSecretEndpointKey: []byte("db.example"),
					xpv2.CredentialsSecretPortKey:     []byte("3306"),
					xpv2.CredentialsSecretUserKey:     []byte("admin"),
					xpv2.CredentialsSecretPasswordKey: []byte("secret"),
				}
			}
			return nil
		}),
	}, managed("default", v1alpha1.ProviderConfigKind))
	if info.Cleartext {
		t.Error("password login enabled cleartext")
	}
	if got := string(info.SecretData[xpv2.CredentialsSecretPasswordKey]); got != "secret" {
		t.Errorf("password: got %q", got)
	}
	if info.TLS == nil || *info.TLS != disabled {
		t.Errorf("tls: got %v", info.TLS)
	}
}

func assertIAM(t *testing.T, info ProviderInfo, mode, registerKey string) {
	t.Helper()
	if !info.Cleartext {
		t.Error("IAM login did not enable cleartext")
	}
	if got := string(info.SecretData[xpv2.CredentialsSecretPasswordKey]); got != "iam-token" {
		t.Errorf("password: got %q", got)
	}
	if info.TLS == nil || *info.TLS != mode {
		t.Errorf("tls: want %s, got %v", mode, info.TLS)
	}
	if info.TLSRegisterKey != registerKey {
		t.Errorf("register key: want %s, got %s", registerKey, info.TLSRegisterKey)
	}
}

func managed(namespace, kind string) *v1alpha1.Database {
	return &v1alpha1.Database{
		ObjectMeta: metav1.ObjectMeta{Namespace: namespace},
		Spec: v1alpha1.DatabaseSpec{
			ManagedResourceSpec: xpv2.ManagedResourceSpec{
				ProviderConfigReference: &xpv2.ProviderConfigReference{
					Kind: kind,
					Name: "aurora",
				},
			},
		},
	}
}

func getInfo(t *testing.T, kube client.Client, mg *v1alpha1.Database) ProviderInfo {
	t.Helper()
	info, err := GetProviderConfig(context.Background(), kube, mg)
	if err != nil {
		t.Fatalf("GetProviderConfig: %v", err)
	}
	return info
}
