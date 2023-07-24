/*
Copyright 2022 The Crossplane Authors.

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

package user

import (
	"context"
	"time"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/crossplane-contrib/provider-sql/pkg/clients/hana/user"

	"github.com/pkg/errors"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/pkg/controller"
	"github.com/crossplane/crossplane-runtime/pkg/event"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/resource"

	"github.com/crossplane-contrib/provider-sql/apis/hana/v1alpha1"
	apisv1alpha1 "github.com/crossplane-contrib/provider-sql/apis/hana/v1alpha1"
)

const (
	errNotUser                 = "managed resource is not a User custom resource"
	errTrackPCUsage            = "cannot track ProviderConfig usage"
	errGetPC                   = "cannot get ProviderConfig"
	errNoSecretRef             = "ProviderConfig does not reference a credentials Secret"
	errGetPasswordSecretFailed = "cannot get password secret"
	errGetSecret               = "cannot get credentials Secret"
)

// Setup adds a controller that reconciles User managed resources.
func Setup(mgr ctrl.Manager, o controller.Options) error {
	name := managed.ControllerName(v1alpha1.UserGroupKind)

	t := resource.NewProviderConfigUsageTracker(mgr.GetClient(), &v1alpha1.ProviderConfigUsage{})
	r := managed.NewReconciler(mgr,
		resource.ManagedKind(v1alpha1.UserGroupVersionKind),
		managed.WithExternalConnecter(&connector{kube: mgr.GetClient(), usage: t, newClient: user.New}),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		// managed.WithPollInterval(o.PollInterval),
		managed.WithPollInterval(10*time.Second),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))))

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		For(&v1alpha1.User{}).
		Complete(r)
}

// A connector is expected to produce an ExternalClient when its Connect method
// is called.
type connector struct {
	kube      client.Client
	usage     resource.Tracker
	newClient func(creds map[string][]byte) user.Client
}

// Connect typically produces an ExternalClient by:
// 1. Tracking that the managed resource is using a ProviderConfig.
// 2. Getting the managed resource's ProviderConfig.
// 3. Getting the credentials specified by the ProviderConfig.
// 4. Using the credentials to form a client.
func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) {
	cr, ok := mg.(*v1alpha1.User)
	if !ok {
		return nil, errors.New(errNotUser)
	}

	if err := c.usage.Track(ctx, mg); err != nil {
		return nil, errors.Wrap(err, errTrackPCUsage)
	}

	pc := &apisv1alpha1.ProviderConfig{}
	if err := c.kube.Get(ctx, types.NamespacedName{Name: cr.GetProviderConfigReference().Name}, pc); err != nil {
		return nil, errors.Wrap(err, errGetPC)
	}

	ref := pc.Spec.Credentials.ConnectionSecretRef
	if ref == nil {
		return nil, errors.New(errNoSecretRef)
	}

	s := &corev1.Secret{}
	if err := c.kube.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, s); err != nil {
		return nil, errors.Wrap(err, errGetSecret)
	}

	return &external{
		client: c.newClient(s.Data),
		kube:   c.kube,
	}, nil
}

// An ExternalClient observes, then either creates, updates, or deletes an
// external resource to ensure it reflects the managed resource's desired state.
type external struct {
	client user.Client
	kube   client.Client
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.User)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotUser)
	}

	parameters := &v1alpha1.UserParameters{
		Username:       cr.Spec.ForProvider.Username,
		RestrictedUser: cr.Spec.ForProvider.RestrictedUser,
		Authentication: apisv1alpha1.Authentication{
			Password: apisv1alpha1.Password{
				PasswordSecretRef:        cr.Spec.ForProvider.Authentication.Password.PasswordSecretRef,
				ForceFirstPasswordChange: cr.Spec.ForProvider.Authentication.Password.ForceFirstPasswordChange,
			},
		},
		Parameters: cr.Spec.ForProvider.Parameters,
		Usergroup:  cr.Spec.ForProvider.Usergroup,
	}

	observed, err := c.client.Observe(ctx, parameters)

	if err != nil {
		return managed.ExternalObservation{}, err
	}

	if observed.Username != parameters.Username {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}

	cr.Status.AtProvider.Username = observed.Username
	cr.Status.AtProvider.LastPasswordChangeTime = observed.LastPasswordChangeTime
	cr.Status.AtProvider.CreatedAt = observed.CreatedAt
	cr.Status.AtProvider.Parameters = observed.Parameters
	cr.Status.AtProvider.Usergroup = observed.Usergroup

	cr.SetConditions(xpv1.Available())

	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: upToDate(observed, parameters),
	}, nil
}

func upToDate(observed *v1alpha1.UserObservation, desired *v1alpha1.UserParameters) bool {
	if observed.CreatedAt != observed.LastPasswordChangeTime {
		return false
	}
	if !equalParameterMap(observed.Parameters, desired.Parameters) {
		return false
	}
	if observed.Usergroup != desired.Usergroup {
		return false
	}
	return true
}

func equalParameterMap(map1, map2 map[string]string) bool {
	if len(map1) != len(map2) {
		return false
	}
	for key, value1 := range map1 {
		value2, ok := map2[key]
		if !ok || value1 != value2 {
			return false
		}
	}
	return true
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.User)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotUser)
	}

	cr.SetConditions(xpv1.Creating())

	parameters := &v1alpha1.UserParameters{
		Username:       cr.Spec.ForProvider.Username,
		RestrictedUser: cr.Spec.ForProvider.RestrictedUser,
		Authentication: apisv1alpha1.Authentication{
			Password: apisv1alpha1.Password{
				PasswordSecretRef:        cr.Spec.ForProvider.Authentication.Password.PasswordSecretRef,
				ForceFirstPasswordChange: cr.Spec.ForProvider.Authentication.Password.ForceFirstPasswordChange,
			},
		},
		Parameters: cr.Spec.ForProvider.Parameters,
		Usergroup:  cr.Spec.ForProvider.Usergroup,
	}

	password, pasErr := c.getPassword(ctx, parameters.Authentication.Password.PasswordSecretRef)

	if pasErr != nil {
		return managed.ExternalCreation{}, pasErr
	}

	err := c.client.Create(ctx, parameters, password)

	if err != nil {
		return managed.ExternalCreation{}, err
	}

	cr.Status.AtProvider.Username = parameters.Username
	cr.Status.AtProvider.RestrictedUser = parameters.RestrictedUser
	cr.Status.AtProvider.Parameters = parameters.Parameters
	cr.Status.AtProvider.Usergroup = parameters.Usergroup

	return managed.ExternalCreation{
		ConnectionDetails: managed.ConnectionDetails{
			"user":     []byte(parameters.Username),
			"password": []byte(password),
		},
	}, nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.User)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotUser)
	}

	desired := &v1alpha1.UserParameters{
		Username: cr.Spec.ForProvider.Username,
		Authentication: apisv1alpha1.Authentication{
			Password: apisv1alpha1.Password{
				PasswordSecretRef:        cr.Spec.ForProvider.Authentication.Password.PasswordSecretRef,
				ForceFirstPasswordChange: cr.Spec.ForProvider.Authentication.Password.ForceFirstPasswordChange,
			},
		},
		Parameters: cr.Spec.ForProvider.Parameters,
		Usergroup:  cr.Spec.ForProvider.Usergroup,
	}

	observed := &v1alpha1.UserObservation{
		LastPasswordChangeTime: cr.Status.AtProvider.LastPasswordChangeTime,
		CreatedAt:              cr.Status.AtProvider.CreatedAt,
		Parameters:             cr.Status.AtProvider.Parameters,
		Usergroup:              cr.Status.AtProvider.Usergroup,
	}

	if observed.CreatedAt != observed.LastPasswordChangeTime {
		err := errors.New("Password was changed externally")
		return managed.ExternalUpdate{}, err
	}
	if !equalParameterMap(observed.Parameters, desired.Parameters) {
		parametersToSet := compareMaps(desired.Parameters, observed.Parameters)
		parametersToClear := compareMaps(observed.Parameters, desired.Parameters)

		err := c.client.UpdateParameters(ctx, desired.Username, parametersToSet, parametersToClear)
		if err != nil {
			return managed.ExternalUpdate{}, err
		}
	}
	if observed.Usergroup != desired.Usergroup {
		err := c.client.UpdateUsergroup(ctx, desired.Username, desired.Usergroup)
		if err != nil {
			return managed.ExternalUpdate{}, err
		}
	}

	return managed.ExternalUpdate{}, nil
}

func compareMaps(map1, map2 map[string]string) map[string]string {
	differenceMap := make(map[string]string)

	for key, val := range map1 {
		if map2[key] != val {
			differenceMap[key] = val
		}
	}

	return differenceMap
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) error {
	cr, ok := mg.(*v1alpha1.User)
	if !ok {
		return errors.New(errNotUser)
	}

	parameters := &v1alpha1.UserParameters{
		Username: cr.Spec.ForProvider.Username,
	}

	cr.SetConditions(xpv1.Deleting())

	err := c.client.Delete(ctx, parameters)

	return err
}

func (c *external) getPassword(ctx context.Context, secretRef *xpv1.SecretKeySelector) (newPwd string, err error) {
	if secretRef == nil {
		return "", nil
	}
	nn := types.NamespacedName{
		Name:      secretRef.Name,
		Namespace: secretRef.Namespace,
	}
	s := &corev1.Secret{}
	if err := c.kube.Get(ctx, nn, s); err != nil {
		return "", errors.Wrap(err, errGetPasswordSecretFailed)
	}
	newPwd = string(s.Data[secretRef.Key])

	return newPwd, nil
}
