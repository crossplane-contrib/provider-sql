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
	"fmt"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/hana"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	corev1 "k8s.io/api/core/v1"
	"strings"

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

	errGetSecret  = "cannot get credentials Secret"
	errSelectRole = "cannot select Role"
	errCreateRole = "cannot create Role"
	errDropRole   = "cannot drop Role"
)

// A NoOpService does nothing.
type NoOpService struct{}

var (
	newNoOpService = func(_ []byte) (interface{}, error) { return &NoOpService{}, nil }
)

// Setup adds a controller that reconciles Role managed resources.
func Setup(mgr ctrl.Manager, o controller.Options) error {
	name := managed.ControllerName(v1alpha1.RoleGroupKind)

	t := resource.NewProviderConfigUsageTracker(mgr.GetClient(), &v1alpha1.ProviderConfigUsage{})
	r := managed.NewReconciler(mgr,
		resource.ManagedKind(v1alpha1.RoleGroupVersionKind),
		managed.WithExternalConnecter(&connector{kube: mgr.GetClient(), usage: t, newDB: hana.New}),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithPollInterval(o.PollInterval),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))))

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		For(&v1alpha1.Role{}).
		Complete(r)
}

// A connector is expected to produce an ExternalClient when its Connect method
// is called.
type connector struct {
	kube  client.Client
	usage resource.Tracker
	newDB func(creds map[string][]byte) xsql.DB
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
		db:   c.newDB(s.Data),
		kube: c.kube,
	}, nil
}

// An ExternalClient observes, then either creates, updates, or deletes an
// external resource to ensure it reflects the managed resource's desired state.
type external struct {
	db   xsql.DB
	kube client.Client
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.Role)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotRole)
	}

	// These fmt statements should be removed in the real implementation.
	fmt.Printf("Observing: %+v", cr)

	observed := &v1alpha1.RoleObservation{
		RoleName:   "",
		LdapGroups: nil,
	}

	roleName := strings.ToUpper(cr.Spec.ForProvider.RoleName)

	query := "SELECT ROLE_NAME FROM SYS.ROLES WHERE ROLE_NAME = ?"
	err := c.db.Scan(ctx, xsql.Query{String: query, Parameters: []interface{}{roleName}}, &observed.RoleName)

	if xsql.IsNoRows(err) {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errSelectRole)
	}

	queryLdapGroups := "SELECT ROLE_NAME, LDAP_GROUP_NAME FROM SYS.ROLE_LDAP_GROUPS WHERE ROLE_NAME = ?"

	rows, err := c.db.Query(ctx, xsql.Query{String: queryLdapGroups, Parameters: []interface{}{roleName}})

	for rows.Next() {
		var role, ldapGroup string
		rowErr := rows.Scan(&role, &ldapGroup)
		if rowErr == nil {
			observed.LdapGroups = append(observed.LdapGroups, ldapGroup)
		}
	}

	cr.SetConditions(xpv1.Available())

	return managed.ExternalObservation{
		// Return false when the external resource does not exist. This lets
		// the managed resource reconciler know that it needs to call Create to
		// (re)create the resource, or that it has successfully been deleted.
		ResourceExists: true,

		// Return false when the external resource exists, but it not up to date
		// with the desired managed resource state. This lets the managed
		// resource reconciler know that it needs to call Update.
		ResourceUpToDate: true,

		// Return any details that may be required to connect to the external
		// resource. These will be stored as the connection secret.
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.Role)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotRole)
	}

	fmt.Printf("Creating: %+v", cr)
	cr.SetConditions(xpv1.Creating())

	parameters := &v1alpha1.RoleParameters{
		RoleName:         cr.Spec.ForProvider.RoleName,
		LdapGroups:       cr.Spec.ForProvider.LdapGroups,
		NoGrantToCreator: cr.Spec.ForProvider.NoGrantToCreator,
	}

	query := fmt.Sprintf("CREATE ROLE %s", parameters.RoleName)

	if len(parameters.LdapGroups) > 0 {
		query += " LDAP GROUP"
		for ldapGroup := range parameters.LdapGroups {
			query += fmt.Sprintf(" '%s',", ldapGroup)
		}
		query = strings.TrimSuffix(query, ",")
	}

	if parameters.NoGrantToCreator {
		query += " NO GRANT TO CREATOR"
	}

	err := c.db.Exec(ctx, xsql.Query{String: query})

	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, errCreateRole)
	}

	return managed.ExternalCreation{
		// Optionally return any details that may be required to connect to the
		// external resource. These will be stored as the connection secret.
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.Role)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotRole)
	}

	fmt.Printf("Updating: %+v", cr)

	return managed.ExternalUpdate{
		// Optionally return any details that may be required to connect to the
		// external resource. These will be stored as the connection secret.
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) error {
	cr, ok := mg.(*v1alpha1.Role)
	if !ok {
		return errors.New(errNotRole)
	}

	fmt.Printf("Deleting: %+v", cr)
	cr.SetConditions(xpv1.Deleting())

	query := fmt.Sprintf("DROP ROLE %s", cr.Spec.ForProvider.RoleName)

	err := c.db.Exec(ctx, xsql.Query{String: query})

	if err != nil {
		return errors.Wrap(err, errDropRole)
	}

	return nil
}
