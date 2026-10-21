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

package awsiam

import (
	"context"
	"testing"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/rds/auth"
	"github.com/google/go-cmp/cmp"
	"github.com/pkg/errors"

	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
)

// builderCall records what a stub TokenBuilder was invoked with, so a test can
// assert on it afterwards.
type builderCall struct {
	endpoint string
	region   string
	dbUser   string
	called   int
}

// recordingBuilder returns a TokenBuilder that records its inputs and returns
// the supplied token/err. This is the "fake AWS": it never calls AWS, so the
// tests are deterministic and need no credentials or network.
func recordingBuilder(token string, err error, rec *builderCall) TokenBuilder {
	return func(_ context.Context, endpoint, region, dbUser string, _ aws.CredentialsProvider, _ ...func(*auth.BuildAuthTokenOptions)) (string, error) {
		rec.endpoint, rec.region, rec.dbUser = endpoint, region, dbUser
		rec.called++
		return token, err
	}
}

func TestResolveRegion(t *testing.T) {
	ptr := func(s string) *string { return &s }

	cases := map[string]struct {
		specRegion *string
		creds      map[string][]byte
		cfgRegion  string
		want       string
	}{
		"SpecFieldWins": {
			specRegion: ptr("eu-west-1"),
			creds:      map[string][]byte{regionKey: []byte("us-east-1")},
			cfgRegion:  "ap-south-1",
			want:       "eu-west-1",
		},
		"SecretWhenSpecNil": {
			specRegion: nil,
			creds:      map[string][]byte{regionKey: []byte("us-east-1")},
			cfgRegion:  "ap-south-1",
			want:       "us-east-1",
		},
		"SecretWhenSpecEmpty": {
			specRegion: ptr(""),
			creds:      map[string][]byte{regionKey: []byte("us-east-1")},
			cfgRegion:  "ap-south-1",
			want:       "us-east-1",
		},
		"ConfigWhenNothingElse": {
			specRegion: nil,
			creds:      nil,
			cfgRegion:  "ap-south-1",
			want:       "ap-south-1",
		},
		"EmptyWhenAllEmpty": {
			specRegion: nil,
			creds:      nil,
			cfgRegion:  "",
			want:       "",
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if got := ResolveRegion(tc.specRegion, tc.creds, tc.cfgRegion); got != tc.want {
				t.Errorf("ResolveRegion(...): want %q, got %q", tc.want, got)
			}
		})
	}
}

func TestInjectToken(t *testing.T) {
	ctx := context.Background()
	errBoom := errors.New("boom")

	// The stub builder ignores the credentials argument, so any value works.
	var noCreds aws.CredentialsProvider

	// fullCreds is a connection-secret map with everything InjectToken needs.
	fullCreds := func() map[string][]byte {
		return map[string][]byte{
			xpv2.CredentialsSecretEndpointKey: []byte("db.example.rds.amazonaws.com"),
			xpv2.CredentialsSecretPortKey:     []byte("5432"),
			xpv2.CredentialsSecretUserKey:     []byte("crossplane_admin"),
		}
	}

	t.Run("Success", func(t *testing.T) {
		creds := fullCreds()
		rec := &builderCall{}

		err := InjectToken(ctx, creds, "eu-west-1", noCreds, recordingBuilder("FAKE_TOKEN", nil, rec))
		if err != nil {
			t.Fatalf("InjectToken(...): unexpected error: %v", err)
		}

		if got := string(creds[xpv2.CredentialsSecretPasswordKey]); got != "FAKE_TOKEN" {
			t.Errorf("password: want %q, got %q", "FAKE_TOKEN", got)
		}
		if rec.endpoint != "db.example.rds.amazonaws.com:5432" {
			t.Errorf("endpoint passed to builder: want host:port, got %q", rec.endpoint)
		}
		if rec.region != "eu-west-1" {
			t.Errorf("region passed to builder: want %q, got %q", "eu-west-1", rec.region)
		}
		if rec.dbUser != "crossplane_admin" {
			t.Errorf("dbUser passed to builder: want %q, got %q", "crossplane_admin", rec.dbUser)
		}
	})

	t.Run("SecondCallBuildsAgain", func(t *testing.T) {
		creds := fullCreds()
		rec := &builderCall{}
		build := recordingBuilder("FAKE_TOKEN", nil, rec)

		if err := InjectToken(ctx, creds, "eu-west-1", noCreds, build); err != nil {
			t.Fatalf("first InjectToken(...): %v", err)
		}
		if err := InjectToken(ctx, creds, "eu-west-1", noCreds, build); err != nil {
			t.Fatalf("second InjectToken(...): %v", err)
		}
		if rec.called != 2 {
			t.Errorf("token builder calls: want 2, got %d", rec.called)
		}
	})

	t.Run("MissingUsername", func(t *testing.T) {
		creds := fullCreds()
		delete(creds, xpv2.CredentialsSecretUserKey)
		rec := &builderCall{}

		err := InjectToken(ctx, creds, "eu-west-1", noCreds, recordingBuilder("x", nil, rec))
		if diff := cmp.Diff(errors.New(errMissingConnDetails), err, test.EquateErrors()); diff != "" {
			t.Errorf("InjectToken(...): -want error, +got error:\n%s", diff)
		}
		if rec.called != 0 {
			t.Error("builder must not be called when connection details are incomplete")
		}
	})

	t.Run("EmptyRegion", func(t *testing.T) {
		creds := fullCreds()
		rec := &builderCall{}

		err := InjectToken(ctx, creds, "", noCreds, recordingBuilder("x", nil, rec))
		if diff := cmp.Diff(errors.New(errNoRegion), err, test.EquateErrors()); diff != "" {
			t.Errorf("InjectToken(...): -want error, +got error:\n%s", diff)
		}
		if rec.called != 0 {
			t.Error("builder must not be called when region is empty")
		}
	})

	t.Run("BuilderError", func(t *testing.T) {
		creds := fullCreds()
		rec := &builderCall{}

		err := InjectToken(ctx, creds, "eu-west-1", noCreds, recordingBuilder("", errBoom, rec))
		if diff := cmp.Diff(errors.Wrap(errBoom, errBuildToken), err, test.EquateErrors()); diff != "" {
			t.Errorf("InjectToken(...): -want error, +got error:\n%s", diff)
		}
		if _, ok := creds[xpv2.CredentialsSecretPasswordKey]; ok {
			t.Error("password must not be set when token generation fails")
		}
	})
}
