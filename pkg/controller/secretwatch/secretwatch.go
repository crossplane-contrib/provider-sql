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

// Package secretwatch reconciles managed resources when the Secret referenced
// by their passwordSecretRef changes.
package secretwatch

import (
	"context"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/api/meta"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/handler"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
)

const (
	indexKey = "spec.forProvider.passwordSecretRef"

	errIndex = "cannot index managed resources by passwordSecretRef"
	errList  = "cannot list managed resources referencing secret"
)

// SecretRefFn returns the Secret referenced by the passwordSecretRef of obj,
// and false if obj references none.
type SecretRefFn func(obj client.Object) (types.NamespacedName, bool)

// PasswordSecretRef indexes objects of the type of obj by the Secret their
// passwordSecretRef points at, and returns a handler for Secret events that
// enqueues every object in list referencing the changed Secret.
func PasswordSecretRef(mgr ctrl.Manager, log logging.Logger, obj client.Object, list client.ObjectList, ref SecretRefFn) (handler.EventHandler, error) {
	if err := mgr.GetFieldIndexer().IndexField(context.Background(), obj, indexKey, indexer(ref)); err != nil {
		return nil, errors.Wrap(err, errIndex)
	}
	return handler.EnqueueRequestsFromMapFunc(enqueueReferencing(mgr.GetClient(), log, list)), nil
}

func indexer(ref SecretRefFn) client.IndexerFunc {
	return func(obj client.Object) []string {
		nn, ok := ref(obj)
		if !ok {
			return nil
		}
		return []string{nn.String()}
	}
}

func enqueueReferencing(c client.Reader, log logging.Logger, list client.ObjectList) handler.MapFunc {
	return func(ctx context.Context, s client.Object) []reconcile.Request {
		key := types.NamespacedName{Namespace: s.GetNamespace(), Name: s.GetName()}.String()
		l := list.DeepCopyObject().(client.ObjectList)
		if err := c.List(ctx, l, client.MatchingFields{indexKey: key}); err != nil {
			// The next poll still picks up the change.
			log.Info(errList, "secret", key, "error", err)
			return nil
		}
		items, err := meta.ExtractList(l)
		if err != nil {
			log.Info(errList, "secret", key, "error", err)
			return nil
		}
		reqs := make([]reconcile.Request, 0, len(items))
		for _, i := range items {
			o, ok := i.(client.Object)
			if !ok {
				continue
			}
			reqs = append(reqs, reconcile.Request{NamespacedName: types.NamespacedName{Namespace: o.GetNamespace(), Name: o.GetName()}})
		}
		return reqs
	}
}
