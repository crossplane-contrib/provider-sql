/*
Copyright 2020 The Crossplane Authors.

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

package role

import (
	"context"
	"time"

	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/pkg/errors"

	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/crossplane-contrib/provider-sql/apis/namespaced/postgresql/v1alpha1"
)

func (c *external) getPassword(ctx context.Context, role *v1alpha1.Role) (newPwd string, changed bool, err error) {
	if role.Spec.ForProvider.PasswordSecretRef != nil {
		nn := types.NamespacedName{
			Name:      role.Spec.ForProvider.PasswordSecretRef.Name,
			Namespace: role.Namespace,
		}
		s := &corev1.Secret{}
		if err := c.kube.Get(ctx, nn, s); err != nil {
			return "", false, errors.Wrap(err, errGetPasswordSecretFailed)
		}
		newPwd = string(s.Data[role.Spec.ForProvider.PasswordSecretRef.Key])

		if role.Spec.WriteConnectionSecretToReference == nil {
			return newPwd, false, nil
		}

		nn = types.NamespacedName{
			Name:      role.Spec.WriteConnectionSecretToReference.Name,
			Namespace: role.Namespace,
		}
		s = &corev1.Secret{}
		// the output secret may not exist yet, so we can skip returning an
		// error if the error is NotFound
		if err := c.kube.Get(ctx, nn, s); resource.IgnoreNotFound(err) != nil {
			return "", false, err
		}
		// if newPwd was set to some value, compare value in output secret with
		// newPwd
		changed = newPwd != "" && newPwd != string(s.Data[xpv2.CredentialsSecretPasswordKey])

		return newPwd, changed, nil
	}

	shouldReset, err := c.shouldResetPassword(ctx, role)
	return "", shouldReset, err
}

// freshRole re-reads role directly from the API server, bypassing the informer cache. role's
// status, as fetched by the managed reconciler's cached client, can be stale immediately after
// a prior reconcile wrote it - e.g. right after recording LastPasswordChange - if that write
// hasn't yet propagated to the cache. shouldResetPassword calls this to confirm a reset is
// still needed before acting on a cached status field that would trigger one, so a stale read
// doesn't cause a second, unnecessary password change.
func (c *external) freshRole(ctx context.Context, role *v1alpha1.Role) (*v1alpha1.Role, error) {
	fresh := &v1alpha1.Role{}
	if err := c.apiReader.Get(ctx, types.NamespacedName{Name: role.GetName(), Namespace: role.Namespace}, fresh); err != nil {
		return nil, errors.Wrap(err, errGetRoleFailed)
	}
	return fresh, nil
}

// rotationDue reports whether trigger asks for a rotation that hasn't happened yet: it must
// be later than the last password change, and its time must have come. A trigger in the
// future therefore schedules a rotation, and once it fires LastPasswordChange moves past the
// trigger, so each trigger value rotates at most once.
func rotationDue(trigger, last *metav1.Time, now time.Time) bool {
	if trigger == nil || last == nil {
		return false
	}
	return trigger.After(last.Time) && !trigger.After(now)
}

// shouldResetPassword returns true when a password change is needed for the non-BYOP path.
// When LastPasswordChange is set, only a due PasswordRotationTrigger forces a reset (see
// rotationDue). When LastPasswordChange is nil the connection secret decides: a populated
// secret means Create already ran, while an absent or empty secret means the role was
// restored out-of-band and needs a fresh password. Without a connection secret reference
// there is nowhere to publish a regenerated password, so no reset is attempted. Either way the
// decision is made from the cached role first, and only a reset is confirmed against a fresh
// read, see freshRole.
func (c *external) shouldResetPassword(ctx context.Context, role *v1alpha1.Role) (bool, error) {
	if role.Status.AtProvider.LastPasswordChange != nil {
		trigger := role.Spec.ForProvider.PasswordRotationTrigger
		now := time.Now()
		if !rotationDue(trigger, role.Status.AtProvider.LastPasswordChange, now) {
			return false, nil
		}
		fresh, err := c.freshRole(ctx, role)
		if err != nil {
			return false, err
		}
		return rotationDue(trigger, fresh.Status.AtProvider.LastPasswordChange, now), nil
	}
	if role.Spec.WriteConnectionSecretToReference == nil {
		return false, nil
	}

	nn := types.NamespacedName{
		Name:      role.Spec.WriteConnectionSecretToReference.Name,
		Namespace: role.Namespace,
	}
	s := &corev1.Secret{}
	if err := c.kube.Get(ctx, nn, s); err != nil {
		if resource.IgnoreNotFound(err) != nil {
			return false, errors.Wrap(err, errGetConnectionSecretFailed)
		}
	} else if len(s.Data[xpv2.CredentialsSecretPasswordKey]) > 0 {
		return false, nil
	}

	fresh, err := c.freshRole(ctx, role)
	if err != nil {
		return false, err
	}
	return fresh.Status.AtProvider.LastPasswordChange == nil, nil
}
