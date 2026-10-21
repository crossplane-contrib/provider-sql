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

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/crossplane-contrib/provider-sql/apis/namespaced/postgresql/v1alpha1"
)

// TestGetProviderConfigTLS covers both ProviderConfig kinds and ensures IAM
// enforces encryption without downgrading verification or changing password auth.
func TestGetProviderConfigTLS(t *testing.T) {
	origInject := injectIAM
	injectIAM = func(_ context.Context, _ *string, creds map[string][]byte) error {
		creds[xpv2.CredentialsSecretPasswordKey] = []byte("iam-token")
		return nil
	}
	t.Cleanup(func() { injectIAM = origInject })
	for _, kind := range []string{v1alpha1.ProviderConfigKind, v1alpha1.ClusterProviderConfigKind} {
		for _, iam := range []bool{false, true} {
			for _, mode := range []string{"unset", "", "disable", "require", "verify-ca", "verify-full"} {
				t.Run(fmt.Sprintf("%s/IAM=%t/%s", kind, iam, mode), func(t *testing.T) {
					var sslMode *string
					if mode != "unset" {
						sslMode = &mode
					}
					source := v1alpha1.CredentialsSourcePostgreSQLConnectionSecret
					if iam {
						source = v1alpha1.CredentialsSourceAWSIAMAuth
					}
					kube := &test.MockClient{MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
						switch o := obj.(type) {
						case *v1alpha1.ProviderConfig:
							o.Spec.SSLMode = sslMode
							o.Spec.Credentials.Source = source
							o.Spec.Credentials.ConnectionSecretRef = xpv2.LocalSecretReference{Name: "s"}
						case *v1alpha1.ClusterProviderConfig:
							o.Spec.SSLMode = sslMode
							o.Spec.Credentials.Source = source
							o.Spec.Credentials.ConnectionSecretRef = xpv2.SecretReference{Name: "s", Namespace: "default"}
						case *corev1.Secret:
							o.Data = map[string][]byte{
								xpv2.CredentialsSecretEndpointKey: []byte("db.example.rds.amazonaws.com"),
								xpv2.CredentialsSecretPortKey:     []byte("5432"),
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
					if iam && (wantMode == "" || wantMode == "disable") {
						wantMode = "require"
					}
					gotMode := ""
					if info.SSLMode != nil {
						gotMode = *info.SSLMode
					}
					if gotMode != wantMode {
						t.Errorf("SSLMode: want %q, got %q", wantMode, gotMode)
					}
					if !iam && mode == "unset" && info.SSLMode != nil {
						t.Error("password auth should preserve nil SSLMode")
					}
					wantPassword := "password"
					if iam {
						wantPassword = "iam-token"
					}
					if got := string(info.SecretData[xpv2.CredentialsSecretPasswordKey]); got != wantPassword {
						t.Errorf("password: want %q, got %q", wantPassword, got)
					}
				})
			}
		}
	}
}
