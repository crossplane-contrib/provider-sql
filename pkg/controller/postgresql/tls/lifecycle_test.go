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

package tls

import (
	"context"
	"errors"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	legacy "github.com/crossplane-contrib/provider-sql/apis/cluster/postgresql/v1alpha1"
	modern "github.com/crossplane-contrib/provider-sql/apis/namespaced/postgresql/v1alpha1"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"
	"github.com/lib/pq"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
)

func lifecycleClient(t *testing.T, objects ...client.Object) client.Client {
	t.Helper()
	scheme := runtime.NewScheme()
	for _, add := range []func(*runtime.Scheme) error{corev1.AddToScheme, legacy.SchemeBuilder.AddToScheme, modern.SchemeBuilder.AddToScheme} {
		if err := add(scheme); err != nil {
			t.Fatal(err)
		}
	}
	objects = append(objects, &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: "ca", Namespace: "trust"}, Data: map[string][]byte{"bundle": testCA(t)}})
	return fake.NewClientBuilder().WithScheme(scheme).WithObjects(objects...).Build()
}

func lifecycleRef() *xpv2.SecretKeySelector {
	return &xpv2.SecretKeySelector{SecretReference: xpv2.SecretReference{Name: "ca", Namespace: "trust"}, Key: "bundle"}
}

func assertRegistered(t *testing.T, mode string, want bool) {
	t.Helper()
	_, err := pq.NewConfig("host=db.example sslmode=" + mode)
	if (err == nil) != want {
		t.Fatalf("registration %s: want registered=%t, got %v", mode, want, err)
	}
}

func TestRegistrationRetirement(t *testing.T) {
	r := newTLSRegistry()
	kube := lifecycleClient(t)
	ctx := context.Background()
	identity := "cluster/ProviderConfig/retirement"
	first, err := r.load(ctx, kube, identity, "verify-full", "old.example", lifecycleRef())
	if err != nil {
		t.Fatal(err)
	}
	next, err := r.load(ctx, kube, identity, "verify-ca", "new.example", lifecycleRef())
	if err != nil {
		t.Fatal(err)
	}
	assertRegistered(t, first, false)
	assertRegistered(t, next, true)
	mode, err := r.load(ctx, kube, identity, "verify-full", "new.example", nil)
	if err != nil || mode != "verify-full" {
		t.Fatalf("removed selector: %q, %v", mode, err)
	}
	assertRegistered(t, next, false)
	if len(r.entries) != 0 {
		t.Fatal("retired registration retained")
	}
}

func TestCollectDeletedConfigurations(t *testing.T) {
	objects := []client.Object{
		&legacy.ProviderConfig{ObjectMeta: metav1.ObjectMeta{Name: "live"}, Spec: legacy.ProviderConfigSpec{TLSConfig: &legacy.TLSConfig{}}},
		&modern.ProviderConfig{ObjectMeta: metav1.ObjectMeta{Name: "live", Namespace: "one"}, Spec: modern.ProviderConfigSpec{TLSConfig: &modern.TLSConfig{}}},
		&modern.ClusterProviderConfig{ObjectMeta: metav1.ObjectMeta{Name: "live"}, Spec: modern.ClusterProviderConfigSpec{TLSConfig: &modern.ClusterTLSConfig{}}},
		&legacy.ProviderConfig{ObjectMeta: metav1.ObjectMeta{Name: "removed-selector"}},
	}
	kube := lifecycleClient(t, objects...)
	r := newTLSRegistry()
	ctx := context.Background()
	modes := map[string]string{}
	for _, id := range []string{"cluster/ProviderConfig/live", "namespaced/ProviderConfig/one/live", "namespaced/ClusterProviderConfig/live", "cluster/ProviderConfig/deleted", "cluster/ProviderConfig/removed-selector"} {
		mode, err := r.load(ctx, kube, id, "verify-full", "db.example", lifecycleRef())
		if err != nil {
			t.Fatal(err)
		}
		modes[id] = mode
	}
	if err := r.collect(ctx, kube); err != nil {
		t.Fatal(err)
	}
	for id, mode := range modes {
		want := id == "cluster/ProviderConfig/live" || id == "namespaced/ProviderConfig/one/live" || id == "namespaced/ClusterProviderConfig/live"
		assertRegistered(t, mode, want)
	}
	if len(r.entries) != 3 {
		t.Fatalf("want three live configurations, got %d", len(r.entries))
	}
	failed := &test.MockClient{MockList: func(context.Context, client.ObjectList, ...client.ListOption) error {
		return errors.New("list unavailable")
	}}
	if err := r.collect(ctx, failed); err == nil {
		t.Fatal("expected list failure")
	}
	if len(r.entries) != 3 {
		t.Fatal("failed list removed live configurations")
	}
	for id := range r.entries {
		r.forget(id)
	}
}

type waitingContext struct {
	context.Context
	waiting chan struct{}
	once    sync.Once
}

func (c *waitingContext) Done() <-chan struct{} {
	c.once.Do(func() { close(c.waiting) })
	return c.Context.Done()
}

func TestReloadWaiterCancellation(t *testing.T) {
	r := newTLSRegistry()
	if err := r.lock(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer r.unlock()
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()
	observed := &waitingContext{Context: ctx, waiting: make(chan struct{})}
	done := make(chan error, 1)
	go func() {
		_, err := r.load(observed, nil, "waiting", "verify-full", "db.example", lifecycleRef())
		done <- err
	}()
	select {
	case <-observed.waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("caller did not wait on registry lock")
	}
	cancel()
	select {
	case err := <-done:
		if !errors.Is(err, context.Canceled) {
			t.Fatalf("want cancellation, got %v", err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("cancelled caller remained blocked")
	}
}

type blockedListClient struct {
	client.Client
	started, release chan struct{}
	once             sync.Once
}

func (c *blockedListClient) List(ctx context.Context, list client.ObjectList, opts ...client.ListOption) error {
	c.once.Do(func() { close(c.started); <-c.release })
	return c.Client.List(ctx, list, opts...)
}

func TestCollectDoesNotRetireConcurrentRegistration(t *testing.T) {
	r := newTLSRegistry()
	kube := lifecycleClient(t)
	ctx := context.Background()
	old, err := r.load(ctx, kube, "cluster/ProviderConfig/old", "verify-full", "db.example", lifecycleRef())
	if err != nil {
		t.Fatal(err)
	}
	blocked := &blockedListClient{Client: kube, started: make(chan struct{}), release: make(chan struct{})}
	done := make(chan error, 1)
	var release sync.Once
	defer release.Do(func() { close(blocked.release) })
	go func() { done <- r.collect(ctx, blocked) }()
	select {
	case <-blocked.started:
	case <-time.After(5 * time.Second):
		t.Fatal("collector did not start listing")
	}
	fresh, err := r.load(ctx, kube, "cluster/ProviderConfig/fresh", "verify-full", "db.example", lifecycleRef())
	if err != nil {
		t.Fatal(err)
	}
	release.Do(func() { close(blocked.release) })
	select {
	case err := <-done:
		if err != nil {
			t.Fatal(err)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("collector did not finish")
	}
	assertRegistered(t, old, false)
	assertRegistered(t, fresh, true)
	if err := r.collect(ctx, kube); err != nil {
		t.Fatal(err)
	}
	assertRegistered(t, fresh, false)
}

func TestSecretReadsSerialized(t *testing.T) {
	r := newTLSRegistry()
	firstCA, secondCA := testCA(t), testCA(t)
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	var reads atomic.Int32
	kube := &test.MockClient{MockGet: func(_ context.Context, _ client.ObjectKey, object client.Object) error {
		ca := secondCA
		if reads.Add(1) == 1 {
			close(started)
			<-release
			ca = firstCA
		}
		object.(*corev1.Secret).Data = map[string][]byte{"bundle": ca}
		return nil
	}}
	type result struct {
		mode string
		err  error
	}
	results := make(chan result, 2)
	load := func(ctx context.Context) {
		mode, err := r.load(ctx, kube, "cluster/ProviderConfig/serialized", "verify-full", "db.example", lifecycleRef())
		results <- result{mode, err}
	}
	go load(context.Background())
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("first read did not start")
	}
	second := &waitingContext{Context: context.Background(), waiting: make(chan struct{})}
	go load(second)
	select {
	case <-second.waiting:
	case <-time.After(5 * time.Second):
		t.Fatal("second reader did not reach lock")
	}
	if reads.Load() != 1 {
		t.Fatal("second Secret read overlapped the first registration")
	}
	releaseOnce.Do(func() { close(release) })
	var previous string
	for range 2 {
		select {
		case result := <-results:
			if result.err != nil {
				t.Fatal(result.err)
			}
			if previous != "" && result.mode != previous {
				t.Fatal("CA rotation changed registration identity")
			}
			previous = result.mode
		case <-time.After(5 * time.Second):
			t.Fatal("serialized reload did not complete")
		}
	}
	if reads.Load() != 2 {
		t.Fatalf("want two ordered reads, got %d", reads.Load())
	}
	r.forget("cluster/ProviderConfig/serialized")
}

func TestSelectorChangesFailClosed(t *testing.T) {
	for _, field := range []string{"namespace", "name", "key"} {
		t.Run(field, func(t *testing.T) {
			r := newTLSRegistry()
			ref := lifecycleRef()
			switch field {
			case "namespace":
				ref.Namespace = "other-trust"
			case "name":
				ref.Name = "other-ca"
			case "key":
				ref.Key = "other-bundle"
			}
			kube := lifecycleClient(t)
			secret := &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Name: ref.Name, Namespace: ref.Namespace}, Data: map[string][]byte{ref.Key: testCA(t)}}
			if field == "key" {
				if err := kube.Get(context.Background(), client.ObjectKeyFromObject(secret), secret); err != nil {
					t.Fatal(err)
				}
				secret.Data[ref.Key] = testCA(t)
				if err := kube.Update(context.Background(), secret); err != nil {
					t.Fatal(err)
				}
			} else if err := kube.Create(context.Background(), secret); err != nil {
				t.Fatal(err)
			}
			identity := "cluster/ProviderConfig/selector-" + field
			old, err := r.load(context.Background(), kube, identity, "verify-full", "db.example", lifecycleRef())
			if err != nil {
				t.Fatal(err)
			}
			next, err := r.load(context.Background(), kube, identity, "verify-full", "db.example", ref)
			if err != nil {
				t.Fatal(err)
			}
			if next == old {
				t.Fatal("different CA selectors reused the same trust registration")
			}
			assertRegistered(t, next, true)
			// An in-flight reconcile with the previous selector must not replace
			// the new selector's trust pool under an otherwise unchanged name.
			if _, err := r.load(context.Background(), kube, identity, "verify-full", "db.example", lifecycleRef()); err != nil {
				t.Fatal(err)
			}
			assertRegistered(t, next, false)
			r.forget(identity)
		})
	}
}

func TestNoCAReconcileBypassesBusyRegistry(t *testing.T) {
	r := newTLSRegistry()
	if err := r.lock(context.Background()); err != nil {
		t.Fatal(err)
	}
	defer r.unlock()
	ctx, cancel := context.WithTimeout(context.Background(), time.Second)
	defer cancel()
	mode, err := r.load(ctx, nil, "unused", "require", "db.example", nil)
	if err != nil || mode != "require" {
		t.Fatalf("no-CA reconcile waited behind the registry: mode=%q err=%v", mode, err)
	}
}

func TestNoCAReconcilePreservesCancellation(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	_, err := newTLSRegistry().load(ctx, nil, "unused", "verify-full", "db.example", nil)
	if !errors.Is(err, context.Canceled) {
		t.Fatalf("want cancellation, got %v", err)
	}
}

func TestSelectorRemovalWaitsForFirstCARead(t *testing.T) {
	r := newTLSRegistry()
	started, release := make(chan struct{}), make(chan struct{})
	var releaseOnce sync.Once
	defer releaseOnce.Do(func() { close(release) })
	ca := testCA(t)
	kube := &test.MockClient{MockGet: func(_ context.Context, _ client.ObjectKey, object client.Object) error {
		close(started)
		<-release
		object.(*corev1.Secret).Data = map[string][]byte{"bundle": ca}
		return nil
	}}
	type result struct {
		mode string
		err  error
	}
	loaded := make(chan result, 1)
	go func() {
		mode, err := r.load(context.Background(), kube, "first-load", "verify-full", "db.example", lifecycleRef())
		loaded <- result{mode, err}
	}()
	select {
	case <-started:
	case <-time.After(5 * time.Second):
		t.Fatal("CA read did not start")
	}
	waiting := &waitingContext{Context: context.Background(), waiting: make(chan struct{})}
	removed := make(chan result, 1)
	go func() {
		mode, err := r.load(waiting, nil, "first-load", "verify-full", "db.example", nil)
		removed <- result{mode, err}
	}()
	select {
	case <-waiting.waiting:
	case result := <-removed:
		t.Fatalf("selector removal bypassed an in-progress CA read: %+v", result)
	case <-time.After(5 * time.Second):
		t.Fatal("selector removal did not reach the gate")
	}
	releaseOnce.Do(func() { close(release) })
	first := <-loaded
	if first.err != nil {
		t.Fatal(first.err)
	}
	next := <-removed
	if next.err != nil || next.mode != "verify-full" {
		t.Fatalf("selector removal: %+v", next)
	}
	assertRegistered(t, first.mode, false)
	if len(r.entries) != 0 {
		t.Fatal("removed selector retained its registration")
	}
}
