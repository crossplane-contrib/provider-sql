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

package awsiam

import (
	"context"
	"errors"
	"fmt"
	"net/url"
	"os"
	"os/exec"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	"github.com/aws/aws-sdk-go-v2/feature/rds/auth"

	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
)

func TestAWSConfigCache(t *testing.T) {
	var calls atomic.Int32
	want := errors.New("temporary config failure")
	cache := awsConfigCache{load: func(context.Context) (aws.Config, error) {
		if calls.Add(1) == 1 {
			return aws.Config{}, want
		}
		return aws.Config{Region: "eu-west-1"}, nil
	}}
	if _, err := cache.get(context.Background()); !errors.Is(err, want) {
		t.Fatalf("expected first load failure, got %v", err)
	}
	var workers sync.WaitGroup
	for range 20 {
		workers.Go(func() {
			cfg, err := cache.get(context.Background())
			if err != nil || cfg.Region != "eu-west-1" {
				t.Errorf("cached config: region=%q error=%v", cfg.Region, err)
			}
		})
	}
	workers.Wait()
	if got := calls.Load(); got != 2 {
		t.Fatalf("expected one failure then one shared successful load, got %d", got)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := cache.get(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("canceled config request: %v", err)
	}
}

type configWaitContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *configWaitContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestAWSConfigCacheCanceledWaiter(t *testing.T) {
	started, release := make(chan struct{}), make(chan struct{})
	cache := awsConfigCache{load: func(context.Context) (aws.Config, error) {
		close(started)
		<-release
		return aws.Config{Region: "eu-west-1"}, nil
	}}
	loaded := make(chan error, 1)
	go func() { _, err := cache.get(context.Background()); loaded <- err }()
	<-started
	t.Cleanup(func() { close(release); <-loaded })
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	waiter := &configWaitContext{Context: ctx, waiting: make(chan struct{})}
	result := make(chan error, 1)
	go func() { _, err := cache.get(waiter); result <- err }()
	select {
	case <-waiter.waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("second caller did not wait on the active config load")
	}
	cancel()
	select {
	case err := <-result:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("waiting caller returned %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("canceled caller remained blocked on another caller's config load")
	}
}

func TestNewTokenProviderWithSDKEnvironment(t *testing.T) {
	// Keep the process-wide cache and the developer's AWS configuration isolated.
	if os.Getenv("PROVIDER_SQL_AWS_CONFIG_TEST") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestNewTokenProviderWithSDKEnvironment$", "-test.timeout=30s")
		cmd.Env = append(os.Environ(), "PROVIDER_SQL_AWS_CONFIG_TEST=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("SDK config subprocess: %v\n%s", err, out)
		}
		return
	}
	for name, value := range map[string]string{
		"AWS_CONFIG_FILE": os.DevNull, "AWS_SHARED_CREDENTIALS_FILE": os.DevNull,
		"AWS_PROFILE": "", "AWS_DEFAULT_PROFILE": "",
		"AWS_ACCESS_KEY_ID": "SYNTHETICACCESS", "AWS_SECRET_ACCESS_KEY": "synthetic-secret",
		"AWS_SESSION_TOKEN": "", "AWS_REGION": "eu-west-1", "AWS_DEFAULT_REGION": "",
		"AWS_EC2_METADATA_DISABLED": "true", "AWS_WEB_IDENTITY_TOKEN_FILE": "", "AWS_ROLE_ARN": "",
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI": "", "AWS_CONTAINER_CREDENTIALS_FULL_URI": "",
	} {
		t.Setenv(name, value)
	}
	creds := map[string][]byte{
		xpv2.CredentialsSecretEndpointKey: []byte("db.example.com"),
		xpv2.CredentialsSecretPortKey:     []byte("5432"),
		xpv2.CredentialsSecretUserKey:     []byte("user"),
	}
	for _, region := range []string{"", "eu-central-1"} {
		provider, err := NewTokenProvider(context.Background(), &region, creds)
		if err != nil {
			t.Fatal(err)
		}
		token, err := provider(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := url.Parse("https://" + token)
		if err != nil {
			t.Fatal(err)
		}
		wantRegion := region
		if wantRegion == "" {
			wantRegion = "eu-west-1"
		}
		if parsed.Query().Get("DBUser") != "user" || !strings.Contains(parsed.Query().Get("X-Amz-Credential"), "/"+wantRegion+"/rds-db/") {
			t.Fatal("SDK token did not use the configured user and region")
		}
	}
}

// builderCall records what a stub TokenBuilder was invoked with, so a test can
// assert on it afterwards.
type builderCall struct {
	endpoint string
	region   string
	dbUser   string
	called   bool
}

// recordingBuilder returns a TokenBuilder that records its inputs and returns
// the supplied token/err. This is the "fake AWS": it never calls AWS, so the
// tests are deterministic and need no credentials or network.
func recordingBuilder(token string, err error, rec *builderCall) TokenBuilder {
	return func(_ context.Context, endpoint, region, dbUser string, _ aws.CredentialsProvider, _ ...func(*auth.BuildAuthTokenOptions)) (string, error) {
		rec.endpoint, rec.region, rec.dbUser, rec.called = endpoint, region, dbUser, true
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

func TestTokenProvider(t *testing.T) {
	creds := map[string][]byte{xpv2.CredentialsSecretEndpointKey: []byte("db.example.com"), xpv2.CredentialsSecretPortKey: []byte("5432"), xpv2.CredentialsSecretUserKey: []byte("admin")}
	now := time.Unix(0, 0)
	calls := 0
	build := func(ctx context.Context, host, region, user string, _ aws.CredentialsProvider, _ ...func(*auth.BuildAuthTokenOptions)) (string, error) {
		if host != "db.example.com:5432" || region != "eu-west-1" || user != "admin" {
			t.Error("incorrect signing inputs")
		}
		calls++
		return fmt.Sprintf("token-%d", now.Unix()), nil
	}
	provider, err := tokenProvider(creds, "eu-west-1", nil, build)
	if err != nil {
		t.Fatal(err)
	}
	if calls != 0 {
		t.Fatal("must not sign a token before a physical connection needs it")
	}
	first, err := provider(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	now = now.Add(20 * time.Minute)
	// The closure captures immutable connection data, not the mutable Secret map.
	creds[xpv2.CredentialsSecretEndpointKey][0] = 'X'
	next, err := provider(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if first == next || calls != 2 {
		t.Fatal("later connection reused the initial token")
	}
	if _, ok := creds[xpv2.CredentialsSecretPasswordKey]; ok {
		t.Error("token must not be written into the Secret map")
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := provider(ctx); err != context.Canceled {
		t.Fatalf("expected cancellation, got %v", err)
	}
	if calls != 2 {
		t.Error("cancellation must prevent signing")
	}
}

func TestTokenProviderValidationAndErrors(t *testing.T) {
	for _, missing := range []string{"endpoint", "port", "username", "region"} {
		t.Run(missing, func(t *testing.T) {
			creds := map[string][]byte{"endpoint": []byte("db"), "port": []byte("3306"), "username": []byte("user")}
			region := "eu-west-1"
			if missing == "region" {
				region = ""
			} else {
				delete(creds, missing)
			}
			if _, err := tokenProvider(creds, region, nil, nil); err == nil {
				t.Error("expected validation failure")
			}
		})
	}
	rec := &builderCall{}
	provider, err := tokenProvider(map[string][]byte{"endpoint": []byte("2001:db8::1"), "port": []byte("5432"), "username": []byte("user")}, "eu-west-1", nil, recordingBuilder("", fmt.Errorf("signing failed"), rec))
	if err != nil {
		t.Fatal(err)
	}
	if _, err := provider(context.Background()); err == nil {
		t.Error("signing error was swallowed")
	}
	if rec.endpoint != "[2001:db8::1]:5432" {
		t.Fatalf("invalid IPv6 host/port: %q", rec.endpoint)
	}
}

func TestTokenProviderRefreshesAWSCredentials(t *testing.T) {
	calls := 0
	fail := false
	boom := errors.New("AWS credentials unavailable")
	cache := aws.NewCredentialsCache(aws.CredentialsProviderFunc(func(context.Context) (aws.Credentials, error) {
		if fail {
			return aws.Credentials{}, boom
		}
		calls++
		return aws.Credentials{AccessKeyID: fmt.Sprintf("TESTKEY%d", calls), SecretAccessKey: "test-secret", SessionToken: "test-session", CanExpire: true, Expires: time.Now().Add(time.Hour)}, nil
	}))
	provider, err := tokenProvider(map[string][]byte{"endpoint": []byte("db.example.com"), "port": []byte("3306"), "username": []byte("user")}, "eu-west-1", cache, auth.BuildAuthToken)
	if err != nil {
		t.Fatal(err)
	}
	first, err := provider(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(first, "TESTKEY1") {
		t.Fatal("token did not use initial AWS credentials")
	}
	cache.Invalidate()
	second, err := provider(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(second, "TESTKEY2") {
		t.Fatal("token reused captured AWS credentials")
	}
	fail = true
	cache.Invalidate()
	if _, err := provider(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("credential error was lost: %v", err)
	}
	fail = false
	if _, err := provider(context.Background()); err != nil {
		t.Fatalf("provider failed to recover: %v", err)
	}
}
