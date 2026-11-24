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
	"time"

	legacy "github.com/crossplane-contrib/provider-sql/apis/cluster/postgresql/v1alpha1"
	modern "github.com/crossplane-contrib/provider-sql/apis/namespaced/postgresql/v1alpha1"
	"github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/runtime"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/manager"
)

// Setup removes registrations for deleted configurations or removed selectors.
// A failed API list never removes entries. This runs with the controllers on
// the elected leader, whose lib/pq TLS registry is process-local.
func Setup(mgr ctrl.Manager, _ controller.Options) error {
	return mgr.Add(manager.RunnableFunc(func(ctx context.Context) error {
		ticker := time.NewTicker(5 * time.Minute)
		defer ticker.Stop()
		for {
			select {
			case <-ctx.Done():
				return nil
			case <-ticker.C:
				if err := defaultRegistry.collect(ctx, mgr.GetClient()); err != nil && ctx.Err() == nil {
					mgr.GetLogger().Error(err, "cannot prune PostgreSQL TLS configurations")
				}
			}
		}
	}))
}

func (r *tlsRegistry) collect(ctx context.Context, kube client.Client) error {
	if err := r.lock(ctx); err != nil {
		return err
	}
	empty := len(r.entries) == 0
	started := r.generation
	r.unlock()
	if empty {
		return nil
	}
	// Skip entries loaded after collection started, so a stale list cannot retire
	// a newly registered config that was created while these lists were running.
	live, err := liveConfigurations(ctx, kube)
	if err != nil {
		return err
	}
	if err := r.lock(ctx); err != nil {
		return err
	}
	defer r.unlock()
	for identity, entry := range r.entries {
		if !live[identity] && entry.generation <= started {
			r.forget(identity)
		}
	}
	return nil
}

func liveConfigurations(ctx context.Context, kube client.Client) (map[string]bool, error) {
	live := make(map[string]bool)
	for _, list := range []client.ObjectList{&legacy.ProviderConfigList{}, &modern.ProviderConfigList{}, &modern.ClusterProviderConfigList{}} {
		if err := kube.List(ctx, list); err != nil {
			return nil, err
		}
		objects, err := meta.ExtractList(list)
		if err != nil {
			return nil, err
		}
		for _, object := range objects {
			if identity, hasCA := configurationIdentity(object); hasCA {
				live[identity] = true
			}
		}
	}
	return live, nil
}

func configurationIdentity(object runtime.Object) (string, bool) {
	switch pc := object.(type) {
	case *legacy.ProviderConfig:
		return "cluster/ProviderConfig/" + pc.Name, pc.Spec.TLSConfig != nil
	case *modern.ProviderConfig:
		return "namespaced/ProviderConfig/" + pc.Namespace + "/" + pc.Name, pc.Spec.TLSConfig != nil
	case *modern.ClusterProviderConfig:
		return "namespaced/ClusterProviderConfig/" + pc.Name, pc.Spec.TLSConfig != nil
	default:
		return "", false
	}
}
