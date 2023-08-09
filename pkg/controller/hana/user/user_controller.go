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
	"reflect"
	"time"

	"k8s.io/utils/strings/slices"

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

	errSelectUser = "cannot select user"
	errCreateUser = "cannot create user"
	errUpdateUser = "cannot update user"
	errDropUser   = "cannot drop user"
)

// Setup adds a controller that reconciles User managed resources.
func Setup(mgr ctrl.Manager, o controller.Options) error {
	name := managed.ControllerName(v1alpha1.UserGroupKind)

	t := resource.NewProviderConfigUsageTracker(mgr.GetClient(), &v1alpha1.ProviderConfigUsage{})
	r := managed.NewReconciler(mgr,
		resource.ManagedKind(v1alpha1.UserGroupVersionKind),
		managed.WithExternalConnecter(&connector{kube: mgr.GetClient(), usage: t, newClient: user.New}),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithPollInterval(o.PollInterval),
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
		Privileges: cr.Spec.ForProvider.Privileges,
		Roles:      cr.Spec.ForProvider.Roles,
		Parameters: cr.Spec.ForProvider.Parameters,
		Usergroup:  cr.Spec.ForProvider.Usergroup,
	}

	// Append default Privilege
	if !parameters.RestrictedUser && !slices.Contains(parameters.Privileges, "CREATE ANY") {
		parameters.Privileges = append(parameters.Privileges, "CREATE ANY")
	}

	// Append default Role
	if !parameters.RestrictedUser && !slices.Contains(parameters.Roles, "PUBLIC") {
		parameters.Roles = append(parameters.Roles, "PUBLIC")
	}

	observed, err := c.client.Read(ctx, parameters)

	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errSelectUser)
	}

	if observed.Username != parameters.Username {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}

	cr.Status.AtProvider.Username = observed.Username
	cr.Status.AtProvider.LastPasswordChangeTime = observed.LastPasswordChangeTime
	cr.Status.AtProvider.CreatedAt = observed.CreatedAt
	cr.Status.AtProvider.Privileges = observed.Privileges
	cr.Status.AtProvider.Roles = observed.Roles
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
	if !equalArrays(observed.Privileges, desired.Privileges) {
		return false
	}
	if !equalArrays(observed.Roles, desired.Roles) {
		return false
	}
	if observed.Usergroup != desired.Usergroup {
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
		Privileges: cr.Spec.ForProvider.Privileges,
		Roles:      cr.Spec.ForProvider.Roles,
		Parameters: cr.Spec.ForProvider.Parameters,
		Usergroup:  cr.Spec.ForProvider.Usergroup,
	}

	password, pasErr := c.getPassword(ctx, parameters.Authentication.Password.PasswordSecretRef)

	if pasErr != nil {
		return managed.ExternalCreation{}, errors.Wrap(pasErr, errCreateUser)
	}

	err := c.client.Create(ctx, parameters, password)

	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, errCreateUser)
	}

	// Append default Privilege
	if !parameters.RestrictedUser && !slices.Contains(parameters.Privileges, "CREATE ANY") {
		parameters.Privileges = append(parameters.Privileges, "CREATE ANY")
	}

	// Append default Role
	if !parameters.RestrictedUser && !slices.Contains(parameters.Roles, "PUBLIC") {
		parameters.Roles = append(parameters.Roles, "PUBLIC")
	}

	cr.Status.AtProvider.Username = parameters.Username
	cr.Status.AtProvider.RestrictedUser = parameters.RestrictedUser
	cr.Status.AtProvider.Privileges = parameters.Privileges
	cr.Status.AtProvider.Roles = parameters.Roles
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

	desired := buildDesiredParameters(cr)
	observed := buildObservedParameters(cr)

	if passwordChanged(observed.CreatedAt, observed.LastPasswordChangeTime) {
		err := errors.New("Password was changed externally")
		return managed.ExternalUpdate{}, errors.Wrap(err, errUpdateUser)
	}

	err1 := updateRolesOrPrivileges(ctx, c, desired.Username, desired.Privileges, observed.Privileges)
	if err1 != nil {
		return managed.ExternalUpdate{}, errors.Wrap(err1, errUpdateUser)
	}
	cr.Status.AtProvider.Privileges = desired.Privileges

	err2 := updateRolesOrPrivileges(ctx, c, desired.Username, desired.Roles, observed.Roles)
	if err2 != nil {
		return managed.ExternalUpdate{}, errors.Wrap(err2, errUpdateUser)
	}
	cr.Status.AtProvider.Roles = desired.Roles

	if !equalParameterMap(observed.Parameters, desired.Parameters) {
		parametersToSet := compareMaps(desired.Parameters, observed.Parameters)
		parametersToClear := compareMaps(observed.Parameters, desired.Parameters)

		err := c.client.UpdateParameters(ctx, desired.Username, parametersToSet, parametersToClear)
		if err != nil {
			return managed.ExternalUpdate{}, errors.Wrap(err, errUpdateUser)
		}
	}
	if observed.Usergroup != desired.Usergroup {
		err := c.client.UpdateUsergroup(ctx, desired.Username, desired.Usergroup)
		if err != nil {
			return managed.ExternalUpdate{}, errors.Wrap(err, errUpdateUser)
		}
	}

	return managed.ExternalUpdate{}, nil
}

func buildDesiredParameters(cr *v1alpha1.User) *v1alpha1.UserParameters {
	desired := &v1alpha1.UserParameters{
		Username:       cr.Spec.ForProvider.Username,
		RestrictedUser: cr.Spec.ForProvider.RestrictedUser,
		Authentication: apisv1alpha1.Authentication{
			Password: apisv1alpha1.Password{
				PasswordSecretRef:        cr.Spec.ForProvider.Authentication.Password.PasswordSecretRef,
				ForceFirstPasswordChange: cr.Spec.ForProvider.Authentication.Password.ForceFirstPasswordChange,
			},
		},
		Privileges: cr.Spec.ForProvider.Privileges,
		Roles:      cr.Spec.ForProvider.Roles,
		Parameters: cr.Spec.ForProvider.Parameters,
		Usergroup:  cr.Spec.ForProvider.Usergroup,
	}
	// Append default Privilege
	if !desired.RestrictedUser && !slices.Contains(desired.Privileges, "CREATE ANY") {
		desired.Privileges = append(desired.Privileges, "CREATE ANY")
	}
	// Append default Role
	if !desired.RestrictedUser && !slices.Contains(desired.Roles, "PUBLIC") {
		desired.Roles = append(desired.Roles, "PUBLIC")
	}
	return desired
}

func buildObservedParameters(cr *v1alpha1.User) *v1alpha1.UserObservation {
	observed := &v1alpha1.UserObservation{
		LastPasswordChangeTime: cr.Status.AtProvider.LastPasswordChangeTime,
		CreatedAt:              cr.Status.AtProvider.CreatedAt,
		Parameters:             cr.Status.AtProvider.Parameters,
		Privileges:             cr.Status.AtProvider.Privileges,
		Roles:                  cr.Status.AtProvider.Roles,
		Usergroup:              cr.Status.AtProvider.Usergroup,
	}
	return observed
}

func passwordChanged(created, changed string) bool {
	changedTime, err1 := time.Parse(time.RFC3339, changed)
	createdTime, err2 := time.Parse(time.RFC3339, created)
	if err1 != nil || err2 != nil {
		return true
	}
	if changedTime.After(createdTime.Add(3 * time.Second)) {
		return true
	}
	return false
}

func updateRolesOrPrivileges(ctx context.Context, c *external, username string, desired, observed []string) error {
	if !equalArrays(observed, desired) {
		toGrant := stringArrayDifference(desired, observed)
		toRevoke := stringArrayDifference(observed, desired)
		err := c.client.UpdateRolesOrPrivileges(ctx, username, toGrant, toRevoke)
		if err != nil {
			return err
		}
	}
	return nil
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

	if err != nil {
		return errors.Wrap(err, errDropUser)
	}

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
