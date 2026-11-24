/*
Copyright 2024 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package provider

import (
	"context"
	"fmt"
	"testing"

	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/crossplane-contrib/provider-sql/apis/namespaced/mysql/v1alpha1"
)

// TestGetProviderConfigTLS covers both ProviderConfig kinds and ensures IAM
// enforces encryption without downgrading verification or changing password auth.
func TestGetProviderConfigTLS(t *testing.T) {
	origInject := newIAMToken
	newIAMToken = func(_ context.Context, _ *string, creds map[string][]byte) (xsql.PasswordProvider, error) {
		return func(context.Context) (string, error) { return "iam-token", nil }, nil
	}
	t.Cleanup(func() { newIAMToken = origInject })
	for _, kind := range []string{v1alpha1.ProviderConfigKind, v1alpha1.ClusterProviderConfigKind} {
		for _, iam := range []bool{false, true} {
			for _, mode := range []string{"unset", "", "false", "preferred", "true", "skip-verify", "custom"} {
				t.Run(fmt.Sprintf("%s/IAM=%t/%s", kind, iam, mode), func(t *testing.T) {
					var sslMode *string
					if mode != "unset" {
						sslMode = &mode
					}
					source := v1alpha1.CredentialsSourceMySQLConnectionSecret
					if iam {
						source = v1alpha1.CredentialsSourceAWSIAMAuth
					}
					kube := &test.MockClient{MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
						switch o := obj.(type) {
						case *v1alpha1.ProviderConfig:
							o.Spec.TLS = sslMode
							o.Spec.Credentials.Source = source
							o.Spec.Credentials.ConnectionSecretRef = xpv2.LocalSecretReference{Name: "s"}
						case *v1alpha1.ClusterProviderConfig:
							o.Spec.TLS = sslMode
							o.Spec.Credentials.Source = source
							o.Spec.Credentials.ConnectionSecretRef = xpv2.SecretReference{Name: "s", Namespace: "default"}
						case *corev1.Secret:
							o.Data = map[string][]byte{
								xpv2.CredentialsSecretEndpointKey: []byte("db.example.rds.amazonaws.com"),
								xpv2.CredentialsSecretPortKey:     []byte("3306"),
								xpv2.CredentialsSecretUserKey:     []byte("crossplane_admin"),
								xpv2.CredentialsSecretPasswordKey: []byte("password"),
							}
						}
						return nil
					})}
					mg := &v1alpha1.Database{
						ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
						Spec: v1alpha1.DatabaseSpec{ManagedResourceSpec: xpv2.ManagedResourceSpec{
							ProviderConfigReference: &xpv2.ProviderConfigReference{Kind: kind, Name: "example"},
						}},
					}
					info, err := GetProviderConfig(context.Background(), kube, mg)
					if err != nil {
						t.Fatal(err)
					}
					wantMode := mode
					if mode == "unset" {
						wantMode = ""
					}
					if iam && (wantMode == "" || wantMode == "false" || wantMode == "preferred") {
						wantMode = "true"
					}
					gotMode := ""
					if info.TLS != nil {
						gotMode = *info.TLS
					}
					if gotMode != wantMode {
						t.Errorf("TLS: want %q, got %q", wantMode, gotMode)
					}
					if info.Cleartext != iam {
						t.Error("cleartext plugin flag must match IAM source")
					}
					if !iam && mode == "unset" && info.TLS != nil {
						t.Error("password auth should preserve nil TLS")
					}
					if iam {
						if info.PasswordProvider == nil {
							t.Fatal("missing per-connection token provider")
						}
						if got, err := info.PasswordProvider(context.Background()); err != nil || got != "iam-token" {
							t.Errorf("token: %q, %v", got, err)
						}
					} else if info.PasswordProvider != nil {
						t.Error("password auth must not use IAM provider")
					}
					if got := string(info.SecretData[xpv2.CredentialsSecretPasswordKey]); got != "password" {
						t.Errorf("Secret password changed: %q", got)
					}
				})
			}
		}
	}
}

func TestTLSRegistrationIdentityUsesConfigurationScope(t *testing.T) {
	for _, tc := range []struct{ kind, namespace, want string }{
		{v1alpha1.ClusterProviderConfigKind, "one", "ClusterProviderConfig/shared"},
		{v1alpha1.ClusterProviderConfigKind, "two", "ClusterProviderConfig/shared"},
		{v1alpha1.ProviderConfigKind, "one", "ProviderConfig/one/shared"},
		{v1alpha1.ProviderConfigKind, "two", "ProviderConfig/two/shared"},
	} {
		t.Run(tc.kind+"/"+tc.namespace, func(t *testing.T) {
			kube := &test.MockClient{MockGet: test.NewMockGetFn(nil, func(object client.Object) error {
				switch pc := object.(type) {
				case *v1alpha1.ProviderConfig:
					pc.Spec.Credentials.Source = v1alpha1.CredentialsSourceMySQLConnectionSecret
					pc.Spec.Credentials.ConnectionSecretRef = xpv2.LocalSecretReference{Name: "connection"}
				case *v1alpha1.ClusterProviderConfig:
					pc.Spec.Credentials.Source = v1alpha1.CredentialsSourceMySQLConnectionSecret
					pc.Spec.Credentials.ConnectionSecretRef = xpv2.SecretReference{Name: "connection", Namespace: "trust"}
				case *corev1.Secret:
					pc.Data = map[string][]byte{}
				}
				return nil
			})}
			mg := &v1alpha1.Database{ObjectMeta: metav1.ObjectMeta{Namespace: tc.namespace}, Spec: v1alpha1.DatabaseSpec{ManagedResourceSpec: xpv2.ManagedResourceSpec{ProviderConfigReference: &xpv2.ProviderConfigReference{Kind: tc.kind, Name: "shared"}}}}
			info, err := GetProviderConfig(context.Background(), kube, mg)
			if err != nil {
				t.Fatal(err)
			}
			if info.ProviderConfigName != tc.want {
				t.Fatalf("registration identity: got %q, want %q", info.ProviderConfigName, tc.want)
			}
		})
	}
}
