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
// injects them in place of a static database password. It lets provider-sql
// connect to Aurora/RDS using an IAM identity (EKS Pod Identity or IRSA)
// instead of a password stored in a Kubernetes Secret.
package awsiam

import (
	"context"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/config"
	"github.com/aws/aws-sdk-go-v2/feature/rds/auth"
	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
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
// InjectToken takes it as a parameter so production code passes the real
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

// InjectToken generates an RDS IAM authentication token and writes it into creds
// as the password. It reads the endpoint, port and username already present in
// creds (populated from the connection secret) and combines endpoint and port
// into the "host:port" form the AWS SDK requires. awsCreds are the AWS
// credentials used to sign the token; build is the token generator
// (auth.BuildAuthToken in production).
//
// On success the password entry of creds holds the token; the database client
// then uses it exactly as it would a static password.
func InjectToken(ctx context.Context, creds map[string][]byte, region string,
	awsCreds aws.CredentialsProvider, build TokenBuilder) error {
	endpoint := string(creds[xpv1.ResourceCredentialsSecretEndpointKey])
	port := string(creds[xpv1.ResourceCredentialsSecretPortKey])
	username := string(creds[xpv1.ResourceCredentialsSecretUserKey])

	if endpoint == "" || port == "" || username == "" {
		return errors.New(errMissingConnDetails)
	}
	if region == "" {
		return errors.New(errNoRegion)
	}

	token, err := build(ctx, endpoint+":"+port, region, username, awsCreds)
	if err != nil {
		return errors.Wrap(err, errBuildToken)
	}

	creds[xpv1.ResourceCredentialsSecretPasswordKey] = []byte(token)
	return nil
}

// Inject loads AWS configuration from the environment, resolves the region
// (ProviderConfig field > secret "region" key > environment) and injects an RDS
// IAM authentication token into creds as the password. It is the entry point a
// reconciler's Connect() calls when the credentials source is AWS IAM auth.
func Inject(ctx context.Context, specRegion *string, creds map[string][]byte) error {
	cfg, err := config.LoadDefaultConfig(ctx)
	if err != nil {
		return errors.Wrap(err, errLoadConfig)
	}
	region := ResolveRegion(specRegion, creds, cfg.Region)
	return InjectToken(ctx, creds, region, cfg.Credentials, auth.BuildAuthToken)
}
