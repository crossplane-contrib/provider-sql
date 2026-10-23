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
	"encoding/json"
	"fmt"
	"net/http"
	"net/http/httptest"
	"net/url"
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"github.com/aws/aws-sdk-go-v2/aws"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
)

func TestNewTokenProviderSharesRenewableSDKCredentials(t *testing.T) {
	if os.Getenv("PROVIDER_SQL_ENDPOINT_CREDENTIALS_TEST") != "1" {
		cmd := exec.Command(os.Args[0], "-test.run=^TestNewTokenProviderSharesRenewableSDKCredentials$", "-test.timeout=30s")
		cmd.Env = append(os.Environ(), "PROVIDER_SQL_ENDPOINT_CREDENTIALS_TEST=1")
		if out, err := cmd.CombinedOutput(); err != nil {
			t.Fatalf("SDK credential subprocess: %v\n%s", err, out)
		}
		return
	}
	var requests, version atomic.Int32
	version.Store(1)
	var authorization atomic.Value
	authorization.Store("pod-token-one")
	server := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		requests.Add(1)
		if r.Header.Get("Authorization") != authorization.Load().(string) {
			http.Error(w, "wrong workload token", http.StatusUnauthorized)
			return
		}
		_ = json.NewEncoder(w).Encode(map[string]string{
			"AccessKeyId": fmt.Sprintf("KEY%d", version.Load()), "SecretAccessKey": "synthetic-secret",
			"Token": "synthetic-session", "Expiration": time.Now().Add(time.Hour).UTC().Format(time.RFC3339),
		})
	}))
	defer server.Close()
	tokenFile := filepath.Join(t.TempDir(), "pod-token")
	if err := os.WriteFile(tokenFile, []byte("pod-token-one"), 0600); err != nil {
		t.Fatal(err)
	}
	for name, value := range map[string]string{
		"AWS_CONFIG_FILE": os.DevNull, "AWS_SHARED_CREDENTIALS_FILE": os.DevNull,
		"AWS_PROFILE": "", "AWS_DEFAULT_PROFILE": "", "AWS_REGION": "eu-west-1", "AWS_DEFAULT_REGION": "",
		"AWS_ACCESS_KEY_ID": "", "AWS_ACCESS_KEY": "", "AWS_SECRET_ACCESS_KEY": "", "AWS_SECRET_KEY": "", "AWS_SESSION_TOKEN": "",
		"AWS_EC2_METADATA_DISABLED": "true", "AWS_WEB_IDENTITY_TOKEN_FILE": "", "AWS_ROLE_ARN": "",
		"AWS_CONTAINER_CREDENTIALS_RELATIVE_URI": "", "AWS_CONTAINER_CREDENTIALS_FULL_URI": server.URL,
		"AWS_CONTAINER_AUTHORIZATION_TOKEN": "", "AWS_CONTAINER_AUTHORIZATION_TOKEN_FILE": tokenFile,
	} {
		t.Setenv(name, value)
	}
	details := map[string][]byte{
		xpv2.CredentialsSecretEndpointKey: []byte("db.example.com"),
		xpv2.CredentialsSecretPortKey:     []byte("5432"), xpv2.CredentialsSecretUserKey: []byte("user"),
	}
	initialCtx, cancel := context.WithCancel(context.Background())
	first, err := NewTokenProvider(initialCtx, nil, details)
	cancel()
	if err != nil {
		t.Fatal(err)
	}
	assertToken := func(provider func(context.Context) (string, error), key string) {
		t.Helper()
		token, err := provider(context.Background())
		if err != nil {
			t.Fatal(err)
		}
		parsed, err := url.Parse("https://" + token)
		if err != nil {
			t.Fatal(err)
		}
		if !strings.HasPrefix(parsed.Query().Get("X-Amz-Credential"), key+"/") {
			t.Fatalf("token did not use renewed key %s", key)
		}
	}
	assertToken(first, "KEY1") // The initializer's canceled context must not survive in the SDK provider.
	second, err := NewTokenProvider(context.Background(), nil, details)
	if err != nil {
		t.Fatal(err)
	}
	assertToken(second, "KEY1")
	if got := requests.Load(); got != 1 {
		t.Fatalf("two reconciles fetched AWS credentials %d times", got)
	}

	if err := os.WriteFile(tokenFile, []byte("pod-token-two"), 0600); err != nil {
		t.Fatal(err)
	}
	authorization.Store("pod-token-two")
	version.Store(2)
	cfg, err := defaultConfigCache.get(context.Background())
	if err != nil {
		t.Fatal(err)
	}
	cache, ok := cfg.Credentials.(*aws.CredentialsCache)
	if !ok {
		t.Fatalf("expected SDK credentials cache, got %T", cfg.Credentials)
	}
	cache.Invalidate()
	assertToken(second, "KEY2")
	assertToken(first, "KEY2")
	if got := requests.Load(); got != 2 {
		t.Fatalf("expected one initial retrieval and one renewal, got %d", got)
	}
}
