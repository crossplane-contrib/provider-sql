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

package usergroup

import (
	"context"
	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/controller"
	"github.com/crossplane/crossplane-runtime/pkg/event"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/resource"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane-contrib/provider-sql/pkg/clients/hana/usergroup"

	"github.com/crossplane-contrib/provider-sql/apis/hana/v1alpha1"
	apisv1alpha1 "github.com/crossplane-contrib/provider-sql/apis/hana/v1alpha1"
)

const (
	errNotUsergroup = "managed resource is not a Usergroup custom resource"
	errTrackPCUsage = "cannot track ProviderConfig usage"
	errGetPC        = "cannot get ProviderConfig"
	errNoSecretRef  = "ProviderConfig does not reference a credentials Secret"
	errGetSecret    = "cannot get credentials Secret"

	errSelectUsergroup = "cannot select usergroup"
	errCreateUsergroup = "cannot create usergroup"
	errUpdateUsergroup = "cannot update usergroup"
	errDropUsergroup   = "cannot drop usergroup"
)

// Setup adds a controller that reconciles Usergroup managed resources.
func Setup(mgr ctrl.Manager, o controller.Options) error {
	name := managed.ControllerName(v1alpha1.UsergroupGroupKind)

	t := resource.NewProviderConfigUsageTracker(mgr.GetClient(), &v1alpha1.ProviderConfigUsage{})
	r := managed.NewReconciler(mgr,
		resource.ManagedKind(v1alpha1.UsergroupGroupVersionKind),
		managed.WithExternalConnecter(&connector{kube: mgr.GetClient(), usage: t, newClient: usergroup.New}),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithPollInterval(o.PollInterval),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))))

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		For(&v1alpha1.Usergroup{}).
		Complete(r)
}

// A connector is expected to produce an ExternalClient when its Connect method
// is called.
type connector struct {
	kube      client.Client
	usage     resource.Tracker
	newClient func(creds map[string][]byte) usergroup.Client
}

// Connect typically produces an ExternalClient by:
// 1. Tracking that the managed resource is using a ProviderConfig.
// 2. Getting the managed resource's ProviderConfig.
// 3. Getting the credentials specified by the ProviderConfig.
// 4. Using the credentials to form a client.
func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) {
	cr, ok := mg.(*v1alpha1.Usergroup)
	if !ok {
		return nil, errors.New(errNotUsergroup)
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
	client usergroup.Client
	kube   client.Client
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.Usergroup)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotUsergroup)
	}

	parameters := &v1alpha1.UsergroupParameters{
		UsergroupName:    cr.Spec.ForProvider.UsergroupName,
		DisableUserAdmin: cr.Spec.ForProvider.DisableUserAdmin,
		Parameters:       cr.Spec.ForProvider.Parameters,
	}

	observed, err := c.client.Read(ctx, parameters)

	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errSelectUsergroup)
	}

	if observed.UsergroupName != parameters.UsergroupName {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}

	cr.Status.AtProvider.UsergroupName = observed.UsergroupName
	cr.Status.AtProvider.DisableUserAdmin = observed.DisableUserAdmin
	cr.Status.AtProvider.Parameters = observed.Parameters

	cr.SetConditions(xpv1.Available())

	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: upToDate(observed, parameters),
	}, nil

}

func upToDate(observed *v1alpha1.UsergroupObservation, desired *v1alpha1.UsergroupParameters) bool {
	if observed.DisableUserAdmin != desired.DisableUserAdmin {
		return false
	}
	if !parametersConfigured(observed.Parameters, desired.Parameters) {
		return false
	}
	return true
}

func parametersConfigured(observed map[string]string, desired map[string]string) bool {
	for key, value := range desired {
		if observed[key] != value {
			return false
		}
	}
	return true
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.Usergroup)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotUsergroup)
	}

	cr.SetConditions(xpv1.Creating())

	parameters := &v1alpha1.UsergroupParameters{
		UsergroupName:      cr.Spec.ForProvider.UsergroupName,
		DisableUserAdmin:   cr.Spec.ForProvider.DisableUserAdmin,
		NoGrantToCreator:   cr.Spec.ForProvider.NoGrantToCreator,
		Parameters:         cr.Spec.ForProvider.Parameters,
		EnableParameterSet: cr.Spec.ForProvider.EnableParameterSet,
	}

	err := c.client.Create(ctx, parameters)

	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, errCreateUsergroup)
	}

	cr.Status.AtProvider.UsergroupName = parameters.UsergroupName
	cr.Status.AtProvider.DisableUserAdmin = true // This is a weird behavior
	cr.Status.AtProvider.Parameters = parameters.Parameters

	return managed.ExternalCreation{
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.Usergroup)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotUsergroup)
	}

	parameters := &v1alpha1.UsergroupParameters{
		UsergroupName:    cr.Spec.ForProvider.UsergroupName,
		DisableUserAdmin: cr.Spec.ForProvider.DisableUserAdmin,
		Parameters:       cr.Spec.ForProvider.Parameters,
	}

	if cr.Status.AtProvider.DisableUserAdmin != parameters.DisableUserAdmin {
		err := c.client.UpdateDisableUserAdmin(ctx, parameters)
		if err != nil {
			return managed.ExternalUpdate{}, errors.Wrap(err, errUpdateUsergroup)
		}
		cr.Status.AtProvider.DisableUserAdmin = parameters.DisableUserAdmin
	}

	observedParameters := cr.Status.AtProvider.Parameters
	desiredParameters := parameters.Parameters

	if !parametersConfigured(observedParameters, desiredParameters) {
		parametersToUpdate := changedParameters(observedParameters, desiredParameters)
		err := c.client.UpdateParameters(ctx, parameters, parametersToUpdate)
		if err != nil {
			return managed.ExternalUpdate{}, errors.Wrap(err, errUpdateUsergroup)
		}
		cr.Status.AtProvider.Parameters = parameters.Parameters
	}

	return managed.ExternalUpdate{}, nil
}

func changedParameters(observed map[string]string, desired map[string]string) map[string]string {
	changed := make(map[string]string)

	for key, value := range desired {
		if observed[key] != value {
			changed[key] = value
		}
	}

	if len(changed) == 0 {
		return nil
	}
	return changed
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) error {
	cr, ok := mg.(*v1alpha1.Usergroup)
	if !ok {
		return errors.New(errNotUsergroup)
	}

	parameters := &v1alpha1.UsergroupParameters{
		UsergroupName: cr.Spec.ForProvider.UsergroupName,
	}

	cr.SetConditions(xpv1.Deleting())

	err := c.client.Delete(ctx, parameters)

	if err != nil {
		return errors.Wrap(err, errDropUsergroup)
	}

	return err
}
