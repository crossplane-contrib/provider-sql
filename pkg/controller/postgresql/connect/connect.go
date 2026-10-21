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

// Package connect prepares a PostgreSQL connection for a ProviderConfig.
// When the credentials source is AWSIAMAuth it injects a fresh RDS IAM token.
// When tlsConfig.caCert is set for verify-ca or verify-full, it registers that
// CA bundle with lib/pq. Other sslMode values are returned unchanged.
package connect

import (
	"context"

	"github.com/lib/pq"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/crossplane-contrib/provider-sql/pkg/clients/awsiam"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/postgresql"
)

const (
	errGenerateIAMToken = "cannot generate AWS IAM authentication token"
	errIncompleteCARef  = "tlsConfig.caCert.secretRef requires name, namespace, and key"
	errGetCACert        = "cannot get tlsConfig.caCert secret"
	errRegisterTLS      = "cannot register PostgreSQL TLS configuration"
)

// InjectIAM generates and injects an AWS IAM auth token. Tests replace it.
var InjectIAM = awsiam.Inject

// CASecretRef selects the PEM bundle used as the server CA.
type CASecretRef struct {
	Name      string
	Namespace string
	Key       string
}

// CA returns a secret ref when name is set.
func CA(name, namespace, key string) *CASecretRef {
	if name == "" {
		return nil
	}
	return &CASecretRef{Name: name, Namespace: namespace, Key: key}
}

// Input is everything Prepare needs from a ProviderConfig and its connection secret.
type Input struct {
	Source           string
	Region           *string
	SSLMode          *string
	CA               *CASecretRef
	DefaultNamespace string
	RegisterKey      string
	Creds            map[string][]byte
}

// Prepare injects an IAM token when Source is AWSIAMAuth and, for verify-ca
// and verify-full with a CA secret, registers that bundle. The returned
// string is the sslmode to pass to the PostgreSQL client. It is pqgo-<key>
// when a custom CA was registered, and the configured sslMode otherwise.
// An unset sslMode is returned as an empty string.
//
// An empty CA namespace uses DefaultNamespace. That is how a namespaced
// ProviderConfig resolves a secret in its own namespace.
func Prepare(ctx context.Context, kube client.Client, in Input) (string, error) {
	mode := sslModeOf(in.SSLMode)
	if err := injectIAM(ctx, in); err != nil {
		return "", err
	}
	if in.CA == nil || !postgresql.UsesCustomCA(mode) {
		return mode, nil
	}
	return registerCA(ctx, kube, in, mode)
}

func sslModeOf(mode *string) string {
	if mode == nil {
		return ""
	}
	return *mode
}

func injectIAM(ctx context.Context, in Input) error {
	if in.Source != awsiam.SourceAWSIAMAuth {
		return nil
	}
	return errors.Wrap(InjectIAM(ctx, in.Region, in.Creds), errGenerateIAMToken)
}

func registerCA(ctx context.Context, kube client.Client, in Input, mode string) (string, error) {
	ns, err := caNamespace(in)
	if err != nil {
		return "", err
	}
	pemBytes, err := secretKey(ctx, kube, ns, in.CA.Name, in.CA.Key)
	if err != nil {
		return "", errors.Wrap(err, errGetCACert)
	}
	endpoint := string(in.Creds[xpv2.CredentialsSecretEndpointKey])
	tlsCfg, err := postgresql.TLSConfig(mode, endpoint, pemBytes)
	if err != nil {
		return "", err
	}
	if err := pq.RegisterTLSConfig(in.RegisterKey, tlsCfg); err != nil {
		return "", errors.Wrap(err, errRegisterTLS)
	}
	return "pqgo-" + in.RegisterKey, nil
}

func caNamespace(in Input) (string, error) {
	ns := in.CA.Namespace
	if ns == "" {
		ns = in.DefaultNamespace
	}
	if ns == "" || in.CA.Key == "" || in.RegisterKey == "" {
		return "", errors.New(errIncompleteCARef)
	}
	return ns, nil
}

func secretKey(ctx context.Context, kube client.Client, namespace, name, key string) ([]byte, error) {
	secret := &corev1.Secret{}
	if err := kube.Get(ctx, types.NamespacedName{Namespace: namespace, Name: name}, secret); err != nil {
		return nil, err
	}
	data, ok := secret.Data[key]
	if !ok {
		return nil, errors.Errorf("key %q not found in Secret %s/%s", key, namespace, name)
	}
	return data, nil
}
