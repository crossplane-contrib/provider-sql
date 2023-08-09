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

package role

import (
	"context"
	"reflect"
	"strings"
	"time"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	corev1 "k8s.io/api/core/v1"

	"github.com/crossplane-contrib/provider-sql/pkg/clients/hana/role"

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
	errNotRole      = "managed resource is not a Role custom resource"
	errTrackPCUsage = "cannot track ProviderConfig usage"
	errGetPC        = "cannot get ProviderConfig"
	errNoSecretRef  = "ProviderConfig does not reference a credentials Secret"
	errGetSecret    = "cannot get credentials Secret"

	errSelectRole = "cannot select role"
	errCreateRole = "cannot create role"
	errUpdateRole = "cannot update role"
	errDropRole   = "cannot drop role"
)

// Setup adds a controller that reconciles Role managed resources.
func Setup(mgr ctrl.Manager, o controller.Options) error {
	name := managed.ControllerName(v1alpha1.RoleGroupKind)

	t := resource.NewProviderConfigUsageTracker(mgr.GetClient(), &v1alpha1.ProviderConfigUsage{})
	r := managed.NewReconciler(mgr,
		resource.ManagedKind(v1alpha1.RoleGroupVersionKind),
		managed.WithExternalConnecter(&connector{kube: mgr.GetClient(), usage: t, newClient: role.New}),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		// managed.WithPollInterval(o.PollInterval),
		managed.WithPollInterval(10*time.Second),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))))

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		For(&v1alpha1.Role{}).
		Complete(r)
}

// A connector is expected to produce an ExternalClient when its Connect method
// is called.
type connector struct {
	kube      client.Client
	usage     resource.Tracker
	newClient func(creds map[string][]byte) role.Client
}

// Connect typically produces an ExternalClient by:
// 1. Tracking that the managed resource is using a ProviderConfig.
// 2. Getting the managed resource's ProviderConfig.
// 3. Getting the credentials specified by the ProviderConfig.
// 4. Using the credentials to form a client.
func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) {
	cr, ok := mg.(*v1alpha1.Role)
	if !ok {
		return nil, errors.New(errNotRole)
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
	client role.Client
	kube   client.Client
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.Role)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotRole)
	}

	parameters := &v1alpha1.RoleParameters{
		RoleName:   strings.ToUpper(cr.Spec.ForProvider.RoleName),
		Schema:     strings.ToUpper(cr.Spec.ForProvider.Schema),
		Privileges: arrayToUpper(cr.Spec.ForProvider.Privileges),
		LdapGroups: arrayToUpper(cr.Spec.ForProvider.LdapGroups),
	}

	observed, err := c.client.Read(ctx, parameters)

	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errSelectRole)
	}

	if observed.RoleName != parameters.RoleName {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}

	cr.Status.AtProvider.RoleName = observed.RoleName
	cr.Status.AtProvider.Schema = observed.Schema
	cr.Status.AtProvider.Privileges = observed.Privileges
	cr.Status.AtProvider.LdapGroups = observed.LdapGroups

	cr.SetConditions(xpv1.Available())

	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: upToDate(observed, parameters),
	}, nil
}

func upToDate(observed *v1alpha1.RoleObservation, desired *v1alpha1.RoleParameters) bool {
	if observed.Schema != desired.Schema {
		return false
	}
	if !equalArrays(observed.Privileges, desired.Privileges) {
		return false
	}
	if !equalArrays(observed.LdapGroups, desired.LdapGroups) {
		return false
	}
	return true
}

func equalArrays(arr1, arr2 []string) bool {
	if len(arr1) != len(arr2) {
		return false
	}

	set1 := arrayToSet(arr1)
	set2 := arrayToSet(arr2)

	return reflect.DeepEqual(set1, set2)
}

func arrayToSet(arr []string) map[string]bool {
	set := make(map[string]bool)
	for _, item := range arr {
		set[item] = true
	}
	return set
}

func arrayToUpper(arr []string) []string {
	upperArr := make([]string, len(arr))
	for i, item := range arr {
		upperArr[i] = strings.ToUpper(item)
	}
	return upperArr
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.Role)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotRole)
	}

	cr.SetConditions(xpv1.Creating())

	parameters := &v1alpha1.RoleParameters{
		RoleName:         cr.Spec.ForProvider.RoleName,
		Schema:           cr.Spec.ForProvider.Schema,
		Privileges:       cr.Spec.ForProvider.Privileges,
		LdapGroups:       cr.Spec.ForProvider.LdapGroups,
		NoGrantToCreator: cr.Spec.ForProvider.NoGrantToCreator,
	}

	err := c.client.Create(ctx, parameters)

	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, errCreateRole)
	}

	cr.Status.AtProvider.RoleName = parameters.RoleName
	cr.Status.AtProvider.Schema = parameters.Schema
	cr.Status.AtProvider.Privileges = parameters.Privileges
	cr.Status.AtProvider.LdapGroups = parameters.LdapGroups

	return managed.ExternalCreation{
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.Role)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotRole)
	}

	parameters := &v1alpha1.RoleParameters{
		RoleName:   strings.ToUpper(cr.Spec.ForProvider.RoleName),
		Schema:     strings.ToUpper(cr.Spec.ForProvider.Schema),
		Privileges: arrayToUpper(cr.Spec.ForProvider.Privileges),
		LdapGroups: arrayToUpper(cr.Spec.ForProvider.LdapGroups),
	}

	observedLdapGroups := cr.Status.AtProvider.LdapGroups
	desiredLdapGroups := parameters.LdapGroups

	observedPrivileges := cr.Status.AtProvider.Privileges
	desiredPrivileges := parameters.Privileges

	if !equalArrays(observedLdapGroups, desiredLdapGroups) {
		groupsToAdd := stringArrayDifference(desiredLdapGroups, observedLdapGroups)
		groupsToRemove := stringArrayDifference(observedLdapGroups, desiredLdapGroups)
		err := c.client.UpdateLdapGroups(ctx, parameters, groupsToAdd, groupsToRemove)
		if err != nil {
			return managed.ExternalUpdate{}, errors.Wrap(err, errUpdateRole)
		}
		cr.Status.AtProvider.LdapGroups = parameters.LdapGroups
	}

	if !equalArrays(observedPrivileges, desiredPrivileges) {
		privilegesToAdd := stringArrayDifference(desiredPrivileges, observedPrivileges)
		privilegesToRemove := stringArrayDifference(observedPrivileges, desiredPrivileges)
		err := c.client.UpdatePrivileges(ctx, parameters, privilegesToAdd, privilegesToRemove)
		if err != nil {
			return managed.ExternalUpdate{}, errors.Wrap(err, errUpdateRole)
		}
		cr.Status.AtProvider.Privileges = parameters.Privileges
	}

	return managed.ExternalUpdate{}, nil
}

func stringArrayDifference(arr1, arr2 []string) []string {
	set := make(map[string]bool)

	for _, item := range arr2 {
		set[item] = true
	}

	var difference []string

	for _, item := range arr1 {
		if _, found := set[item]; !found {
			difference = append(difference, item)
		}
	}

	return difference
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) error {
	cr, ok := mg.(*v1alpha1.Role)
	if !ok {
		return errors.New(errNotRole)
	}

	parameters := &v1alpha1.RoleParameters{
		RoleName: cr.Spec.ForProvider.RoleName,
		Schema:   cr.Spec.ForProvider.Schema,
	}

	cr.SetConditions(xpv1.Deleting())

	err := c.client.Delete(ctx, parameters)

	if err != nil {
		return errors.Wrap(err, errDropRole)
	}

	return err
}
