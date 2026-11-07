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

// Package connect prepares MySQL connections for cluster reconcilers.
package connect

import (
	"context"
	"fmt"

	"github.com/crossplane-contrib/provider-sql/apis/cluster/mysql/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/awsiam"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/mysql"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
	"github.com/crossplane-contrib/provider-sql/pkg/controller/cluster/mysql/tls"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

var newIAMToken = awsiam.NewTokenProvider

// Configure preserves password-auth defaults and prepares IAM signing and CA trust.
func Configure(ctx context.Context, kube client.Client, pc *v1alpha1.ProviderConfig, secretData map[string][]byte) (*string, bool, xsql.PasswordProvider, error) {
	mode, err := tls.LoadConfig(ctx, kube, pc.Name, pc.Spec.TLS, pc.Spec.TLSConfig)
	if err != nil {
		return nil, false, nil, fmt.Errorf("cannot load TLS config: %w", err)
	}
	var password xsql.PasswordProvider
	iam := pc.Spec.Credentials.Source == v1alpha1.CredentialsSourceAWSIAMAuth
	if iam {
		password, err = newIAMToken(ctx, pc.Spec.Credentials.Region, secretData)
		if err != nil {
			return nil, false, nil, fmt.Errorf("cannot generate AWS IAM authentication token: %w", err)
		}
		mode = mysql.EnsureTLS(mode)
	}
	return mode, iam, password, nil
}
