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

	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"

	"github.com/pkg/errors"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"

	"github.com/crossplane-contrib/provider-sql/apis/cluster/postgresql/v1alpha1"
)

func (c *external) getPassword(ctx context.Context, role *v1alpha1.Role) (newPwd string, changed bool, err error) {
	if role.Spec.ForProvider.PasswordSecretRef != nil {
		nn := types.NamespacedName{
			Name:      role.Spec.ForProvider.PasswordSecretRef.Name,
			Namespace: role.Spec.ForProvider.PasswordSecretRef.Namespace,
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
			Namespace: role.Spec.WriteConnectionSecretToReference.Namespace,
		}
		s = &corev1.Secret{}
		// the output secret may not exist yet, so we can skip returning an
		// error if the error is NotFound
		if err := c.kube.Get(ctx, nn, s); resource.IgnoreNotFound(err) != nil {
			return "", false, err
		}
		// if newPwd was set to some value, compare value in output secret with
		// newPwd
		changed = newPwd != "" && newPwd != string(s.Data[xpv1.ResourceCredentialsSecretPasswordKey])

		return newPwd, changed, nil
	}

	return "", c.shouldResetPassword(role), nil
}

// shouldResetPassword returns true when a password change is needed for the non-BYOP path.
// PasswordRotationTrigger fires when the trigger time is after LastPasswordChange.
// PasswordReset fires once when LastPasswordChange is nil (e.g. after a database restore).
func (c *external) shouldResetPassword(role *v1alpha1.Role) bool {
	if role.Spec.ForProvider.PasswordSecretRef != nil {
		return false
	}
	if role.Spec.ForProvider.PasswordRotationTrigger != nil {
		last := role.Status.AtProvider.LastPasswordChange
		if last == nil {
			return true
		}
		return role.Spec.ForProvider.PasswordRotationTrigger.After(last.Time)
	}
	if role.Spec.ForProvider.PasswordReset == nil || !*role.Spec.ForProvider.PasswordReset {
		return false
	}
	return role.Status.AtProvider.LastPasswordChange == nil
}
