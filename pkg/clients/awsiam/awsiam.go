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

// Package awsiam generates short-lived AWS RDS IAM authentication tokens and
// supplies them in place of a static database password. It lets provider-sql
// connect to Aurora/RDS using an IAM identity (EKS Pod Identity or IRSA)
// instead of a password stored in a Kubernetes Secret.
package awsiam

import (
	"context"
	"net"
	"sync"

	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/rds/auth"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
	"github.com/pkg/errors"
)

// regionKey is the optional connection-secret key used to resolve the AWS
// region when it is not set on the ProviderConfig.
const regionKey = "region"

const (
	errMissingConnDetails = "connection secret must contain endpoint, port and username for AWS IAM authentication"
	errNoRegion           = "AWS region could not be resolved: set spec.credentials.region, a \"region\" key in the connection secret, or the controller's AWS region (e.g. AWS_REGION)"
	errBuildToken         = "cannot generate AWS RDS IAM authentication token"
	errLoadConfig         = "cannot load AWS configuration"
)

// TokenBuilder has the same signature as auth.BuildAuthToken from the AWS SDK.
// The token provider takes it as a parameter so production code passes the real
// auth.BuildAuthToken while tests pass a stub that returns a fixed token
// without calling AWS.
type TokenBuilder func(ctx context.Context, endpoint, region, dbUser string,
	creds aws.CredentialsProvider, optFns ...func(*auth.BuildAuthTokenOptions)) (string, error)

// ResolveRegion picks the AWS region to sign the token with, in priority order:
// the ProviderConfig field, then a "region" key in the connection secret, then
// the region the AWS SDK discovered from the environment (cfgRegion). The result
// may be empty if none of the three is set; callers must treat that as an error,
// because the SDK does not reject an empty region at token-generation time.
func ResolveRegion(specRegion *string, creds map[string][]byte, cfgRegion string) string {
	if specRegion != nil && *specRegion != "" {
		return *specRegion
	}
	if r := string(creds[regionKey]); r != "" {
		return r
	}
	return cfgRegion
}

type awsConfigCache struct {
	mu      sync.Mutex
	config  *aws.Config
	loading chan struct{}
	load    func(context.Context) (aws.Config, error)
}

func (c *awsConfigCache) get(ctx context.Context) (aws.Config, error) {
	for {
		if err := ctx.Err(); err != nil {
			return aws.Config{}, err
		}
		c.mu.Lock()
		if c.config != nil {
			cfg := *c.config
			c.mu.Unlock()
			return cfg, nil
		}
		if c.loading != nil {
			ready := c.loading
			c.mu.Unlock()
			select {
			case <-ctx.Done():
				return aws.Config{}, ctx.Err()
			case <-ready:
				continue
			}
		}
		c.loading = make(chan struct{})
		c.mu.Unlock()

		cfg, err := c.load(ctx)
		c.mu.Lock()
		if err == nil {
			c.config = &cfg
		}
		close(c.loading)
		c.loading = nil
		c.mu.Unlock()
		return cfg, err
	}
}

var defaultConfigCache = awsConfigCache{load: func(ctx context.Context) (aws.Config, error) {
	return config.LoadDefaultConfig(ctx)
}}

// NewTokenProvider reuses the process's AWS configuration and signs a new token for
// every physical connection. The SDK credential provider refreshes temporary
// AWS credentials as needed. No token is retained in a long-lived DSN.
func NewTokenProvider(ctx context.Context, specRegion *string, creds map[string][]byte) (xsql.PasswordProvider, error) {
	cfg, err := defaultConfigCache.get(ctx)
	if err != nil {
		return nil, errors.Wrap(err, errLoadConfig)
	}
	return tokenProvider(creds, ResolveRegion(specRegion, creds, cfg.Region), cfg.Credentials, auth.BuildAuthToken)
}

func tokenProvider(creds map[string][]byte, region string, awsCreds aws.CredentialsProvider, build TokenBuilder) (xsql.PasswordProvider, error) {
	endpoint := string(creds[xpv2.CredentialsSecretEndpointKey])
	port := string(creds[xpv2.CredentialsSecretPortKey])
	username := string(creds[xpv2.CredentialsSecretUserKey])
	if endpoint == "" || port == "" || username == "" {
		return nil, errors.New(errMissingConnDetails)
	}
	if region == "" {
		return nil, errors.New(errNoRegion)
	}
	address := net.JoinHostPort(endpoint, port)
	return func(ctx context.Context) (string, error) {
		if err := ctx.Err(); err != nil {
			return "", err
		}
		token, err := build(ctx, address, region, username, awsCreds)
		if err != nil {
			return "", errors.Wrap(err, errBuildToken)
		}
		return token, nil
	}, nil
}
