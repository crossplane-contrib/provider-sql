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

// Package pgconn builds the connection input for a cluster-scoped PostgreSQL ProviderConfig.
package pgconn

import (
	"github.com/crossplane-contrib/provider-sql/apis/cluster/postgresql/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/controller/postgresql/connect"
)

// Input returns the connect input for pc. creds is the remapped connection secret.
func Input(pc *v1alpha1.ProviderConfig, creds map[string][]byte) connect.Input {
	var ca *connect.CASecretRef
	if pc.Spec.TLSConfig != nil {
		ref := pc.Spec.TLSConfig.CACert.SecretRef
		ca = connect.CA(ref.Name, ref.Namespace, ref.Key)
	}
	return connect.Input{
		Source:      string(pc.Spec.Credentials.Source),
		Region:      pc.Spec.Credentials.Region,
		SSLMode:     pc.Spec.SSLMode,
		CA:          ca,
		RegisterKey: "cluster-" + pc.GetName(),
		Creds:       creds,
	}
}
