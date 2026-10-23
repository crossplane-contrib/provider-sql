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

// Package tls loads PostgreSQL server CA bundles from ProviderConfig Secrets.
package tls

import (
	"context"
	"crypto/sha256"
	"fmt"

	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
	"github.com/lib/pq"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane-contrib/provider-sql/pkg/clients/postgresql"
)

type registration struct {
	name       string
	generation uint64
}

type tlsRegistry struct {
	gate       chan struct{}
	entries    map[string]registration
	generation uint64
}

func newTLSRegistry() *tlsRegistry {
	return &tlsRegistry{gate: make(chan struct{}, 1), entries: make(map[string]registration)}
}

var defaultRegistry = newTLSRegistry()

func (r *tlsRegistry) lock(ctx context.Context) error {
	if err := ctx.Err(); err != nil {
		return err
	}
	select {
	case r.gate <- struct{}{}:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

func (r *tlsRegistry) unlock() { <-r.gate }

// forget is called while holding the registry lock.
func (r *tlsRegistry) forget(identity string) {
	if old, ok := r.entries[identity]; ok {
		_ = pq.RegisterTLSConfig(old.name, nil)
		delete(r.entries, identity)
	}
}

// LoadConfig reads and registers the CA on each reconcile, so Secret rotation
// takes effect without restarting the provider. Identity must include the API
// scope, ProviderConfig kind, namespace and name to keep trust pools separate.
// Without a selector, the driver's existing trust-store behavior is preserved.
func LoadConfig(ctx context.Context, kube client.Client, identity, mode, endpoint string, ref *xpv2.SecretKeySelector) (string, error) {
	return defaultRegistry.load(ctx, kube, identity, mode, endpoint, ref)
}

func (r *tlsRegistry) load(ctx context.Context, kube client.Client, identity, mode, endpoint string, ref *xpv2.SecretKeySelector) (string, error) {
	// Serialize the complete read/parse/register sequence. Otherwise an older
	// Secret read could overwrite a newer bundle from a concurrent reconcile.
	if err := r.lock(ctx); err != nil {
		return "", err
	}
	defer r.unlock()
	if ref == nil {
		r.forget(identity)
		return mode, nil
	}
	if mode == "" {
		mode = "verify-full"
	}
	ca, err := readCA(ctx, kube, ref)
	if err != nil {
		return "", err
	}
	config, err := postgresql.TLSConfig(mode, endpoint, ca)
	if err != nil {
		return "", err
	}
	// Keep a stable registration per configuration, mode and endpoint. Replacing
	// it refreshes this configuration's CA without changing another config's trust.
	name := fmt.Sprintf("provider-sql-postgresql-%x", sha256.Sum256([]byte(identity+"\x00"+mode+"\x00"+endpoint+"\x00"+ref.Namespace+"\x00"+ref.Name+"\x00"+ref.Key)))
	if err := pq.RegisterTLSConfig(name, config); err != nil {
		return "", fmt.Errorf("cannot register PostgreSQL TLS configuration: %w", err)
	}
	if previous, ok := r.entries[identity]; ok && previous.name != name {
		_ = pq.RegisterTLSConfig(previous.name, nil)
	}
	r.generation++
	r.entries[identity] = registration{name: name, generation: r.generation}
	return "pqgo-" + name, nil
}

func readCA(ctx context.Context, kube client.Client, ref *xpv2.SecretKeySelector) ([]byte, error) {
	if ref.Name == "" || ref.Namespace == "" || ref.Key == "" {
		return nil, fmt.Errorf("tlsConfig.caCert.secretRef requires name, namespace and key")
	}
	secret := &corev1.Secret{}
	if err := kube.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, secret); err != nil {
		return nil, fmt.Errorf("cannot get PostgreSQL CA Secret: %w", err)
	}
	ca, ok := secret.Data[ref.Key]
	if !ok {
		return nil, fmt.Errorf("CA Secret %s/%s does not contain key %q", ref.Namespace, ref.Name, ref.Key)
	}
	return ca, nil
}
