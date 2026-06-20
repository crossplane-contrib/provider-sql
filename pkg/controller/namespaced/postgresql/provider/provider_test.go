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
	"testing"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/apis/common"
	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	xpv2 "github.com/crossplane/crossplane-runtime/v2/apis/common/v2"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"

	"github.com/crossplane-contrib/provider-sql/apis/namespaced/postgresql/v1alpha1"
)

// TestGetProviderConfigAWSIAMAuth verifies the centralized IAM injection: when
// the source is AWSIAMAuth the token is injected as the password and sslmode is
// forced to require. This covers all six namespaced PostgreSQL reconcilers,
// which obtain credentials exclusively through GetProviderConfig.
func TestGetProviderConfigAWSIAMAuth(t *testing.T) {
	// Stub the AWS IAM token injection so the test needs no AWS access.
	origInject := injectIAM
	injectIAM = func(_ context.Context, _ *string, creds map[string][]byte) error {
		creds[xpv1.ResourceCredentialsSecretPasswordKey] = []byte("iam-token")
		return nil
	}
	defer func() { injectIAM = origInject }()

	kube := &test.MockClient{
		MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
			switch o := obj.(type) {
			case *v1alpha1.ProviderConfig:
				o.Spec.Credentials.Source = v1alpha1.CredentialsSourceAWSIAMAuth
				o.Spec.Credentials.ConnectionSecretRef = xpv1.LocalSecretReference{Name: "s"}
			case *corev1.Secret:
				o.Data = map[string][]byte{
					xpv1.ResourceCredentialsSecretEndpointKey: []byte("db.example.rds.amazonaws.com"),
					xpv1.ResourceCredentialsSecretPortKey:     []byte("5432"),
					xpv1.ResourceCredentialsSecretUserKey:     []byte("crossplane_admin"),
				}
			}
			return nil
		}),
	}

	mg := &v1alpha1.Database{
		ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
		Spec: v1alpha1.DatabaseSpec{
			ManagedResourceSpec: xpv2.ManagedResourceSpec{
				ProviderConfigReference: &common.ProviderConfigReference{
					Kind: v1alpha1.ProviderConfigKind,
					Name: "example",
				},
			},
		},
	}

	info, err := GetProviderConfig(context.Background(), kube, mg)
	if err != nil {
		t.Fatalf("GetProviderConfig(...): unexpected error: %v", err)
	}
	if info.SSLMode == nil || *info.SSLMode != "require" {
		t.Errorf("expected SSLMode forced to require, got %v", info.SSLMode)
	}
	if got := string(info.SecretData[xpv1.ResourceCredentialsSecretPasswordKey]); got != "iam-token" {
		t.Errorf("expected injected token as password, got %q", got)
	}
}
