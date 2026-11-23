/*
Copyright 2026 The Crossplane Authors.

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

// Package connect prepares PostgreSQL connections for cluster reconcilers.
package connect

import (
	"context"
	"fmt"

	"github.com/crossplane-contrib/provider-sql/apis/cluster/postgresql/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/awsiam"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/postgresql"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
	postgresqltls "github.com/crossplane-contrib/provider-sql/pkg/controller/postgresql/tls"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var newIAMToken = awsiam.NewTokenProvider

// Configure preserves password-auth defaults and prepares IAM signing and CA trust.
func Configure(ctx context.Context, kube client.Client, pc *v1alpha1.ProviderConfig, secretData map[string][]byte) (string, xsql.PasswordProvider, error) {
	mode := clients.ToString(pc.Spec.SSLMode)
	var password xsql.PasswordProvider
	if pc.Spec.Credentials.Source == v1alpha1.CredentialsSourceAWSIAMAuth {
		var err error
		password, err = newIAMToken(ctx, pc.Spec.Credentials.Region, secretData)
		if err != nil {
			return "", nil, fmt.Errorf("cannot generate AWS IAM authentication token: %w", err)
		}
		mode = postgresql.EnsureTLS(mode)
	}
	var caRef *xpv2.SecretKeySelector
	if pc.Spec.TLSConfig != nil {
		caRef = &pc.Spec.TLSConfig.CACert.SecretRef
	}
	mode, err := postgresqltls.LoadConfig(ctx, kube, "cluster/ProviderConfig/"+pc.Name, mode, string(secretData[xpv2.CredentialsSecretEndpointKey]), caRef)
	if err != nil {
		return "", nil, fmt.Errorf("cannot configure PostgreSQL TLS: %w", err)
	}
	return mode, password, nil
}
