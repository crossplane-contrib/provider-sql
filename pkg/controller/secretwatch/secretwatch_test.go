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

package secretwatch

import (
	"context"
	"testing"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/runtime"
	"k8s.io/apimachinery/pkg/types"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/client/fake"
	"sigs.k8s.io/controller-runtime/pkg/reconcile"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/crossplane-contrib/provider-sql/apis/namespaced/postgresql/v1alpha1"
)

func role(ns, name, secret string) *v1alpha1.Role {
	r := &v1alpha1.Role{ObjectMeta: metav1.ObjectMeta{Namespace: ns, Name: name}}
	if secret != "" {
		r.Spec.ForProvider.PasswordSecretRef = &xpv2.LocalSecretKeySelector{
			LocalSecretReference: xpv2.LocalSecretReference{Name: secret},
			Key:                  "password",
		}
	}
	return r
}

func ref(o client.Object) (types.NamespacedName, bool) {
	r := o.(*v1alpha1.Role)
	if r.Spec.ForProvider.PasswordSecretRef == nil {
		return types.NamespacedName{}, false
	}
	return types.NamespacedName{Namespace: r.GetNamespace(), Name: r.Spec.ForProvider.PasswordSecretRef.Name}, true
}

func TestEnqueueReferencing(t *testing.T) {
	s := runtime.NewScheme()
	if err := v1alpha1.SchemeBuilder.AddToScheme(s); err != nil {
		t.Fatal(err)
	}
	c := fake.NewClientBuilder().
		WithScheme(s).
		WithIndex(&v1alpha1.Role{}, indexKey, indexer(ref)).
		WithObjects(
			role("a", "r1", "pw"),
			role("a", "r2", "pw"),
			role("a", "r3", "other"),
			role("a", "r4", ""),
			role("b", "r5", "pw"),
		).
		Build()
	fn := enqueueReferencing(c, logging.NewNopLogger(), &v1alpha1.RoleList{})

	cases := map[string]struct {
		secret *corev1.Secret
		want   []reconcile.Request
	}{
		"ReferencedByTwo": {
			secret: &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "pw"}},
			want: []reconcile.Request{
				{NamespacedName: types.NamespacedName{Namespace: "a", Name: "r1"}},
				{NamespacedName: types.NamespacedName{Namespace: "a", Name: "r2"}},
			},
		},
		"OtherNamespace": {
			secret: &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "b", Name: "pw"}},
			want: []reconcile.Request{
				{NamespacedName: types.NamespacedName{Namespace: "b", Name: "r5"}},
			},
		},
		"Unreferenced": {
			secret: &corev1.Secret{ObjectMeta: metav1.ObjectMeta{Namespace: "a", Name: "conn"}},
			want:   []reconcile.Request{},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := fn(context.Background(), tc.secret)
			sortReqs := cmpopts.SortSlices(func(a, b reconcile.Request) bool { return a.Name < b.Name })
			if diff := cmp.Diff(tc.want, got, sortReqs); diff != "" {
				t.Errorf("enqueueReferencing(...): -want, +got:\n%s", diff)
			}
		})
	}
}
