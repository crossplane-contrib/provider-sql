/*
Copyright 2021 The Crossplane Authors.

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

package grant

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	xpcontroller "github.com/crossplane/crossplane-runtime/pkg/controller"
	"github.com/crossplane/crossplane-runtime/pkg/event"
	"github.com/crossplane/crossplane-runtime/pkg/feature"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/resource"

	"github.com/crossplane-contrib/provider-sql/apis/mssql/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/mssql"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
)

const (
	errTrackPCUsage = "cannot track ProviderConfig usage"
	errGetPC        = "cannot get ProviderConfig"
	errNoSecretRef  = "ProviderConfig does not reference a credentials Secret"
	errGetSecret    = "cannot get credentials Secret"

	errNotGrant        = "managed resource is not a Grant custom resource"
	errGrant           = "cannot grant"
	errRevoke          = "cannot revoke"
	errCannotGetGrants = "cannot get current grants"

	maxConcurrency = 5
)

// Setup adds a controller that reconciles Grant managed resources.
func Setup(mgr ctrl.Manager, o xpcontroller.Options) error {
	name := managed.ControllerName(v1alpha1.GrantGroupKind)

	t := resource.NewProviderConfigUsageTracker(mgr.GetClient(), &v1alpha1.ProviderConfigUsage{})
	reconcilerOptions := []managed.ReconcilerOption{
		managed.WithExternalConnecter(&connector{kube: mgr.GetClient(), usage: t, newClient: mssql.New}),
		managed.WithReferenceResolver(managed.NewAPISimpleReferenceResolver(mgr.GetClient())),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithPollInterval(o.PollInterval),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))),
	}
	if o.Features.Enabled(feature.EnableBetaManagementPolicies) {
		reconcilerOptions = append(reconcilerOptions, managed.WithManagementPolicies())
	}
	r := managed.NewReconciler(mgr,
		resource.ManagedKind(v1alpha1.GrantGroupVersionKind),
		reconcilerOptions...,
	)
	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		For(&v1alpha1.Grant{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: maxConcurrency,
		}).
		Complete(r)
}

type connector struct {
	kube      client.Client
	usage     resource.Tracker
	newClient func(creds map[string][]byte, database string) xsql.DB
}

func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) {
	cr, ok := mg.(*v1alpha1.Grant)
	if !ok {
		return nil, errors.New(errNotGrant)
	}

	if err := c.usage.Track(ctx, mg); err != nil {
		return nil, errors.Wrap(err, errTrackPCUsage)
	}

	// ProviderConfigReference could theoretically be nil, but in practice the
	// DefaultProviderConfig initializer will set it before we get here.
	pc := &v1alpha1.ProviderConfig{}
	if err := c.kube.Get(ctx, types.NamespacedName{Name: cr.GetProviderConfigReference().Name}, pc); err != nil {
		return nil, errors.Wrap(err, errGetPC)
	}

	// We don't need to check the credentials source because we currently only
	// support one source (MSSQLConnectionSecret), which is required and
	// enforced by the ProviderConfig schema.
	ref := pc.Spec.Credentials.ConnectionSecretRef
	if ref == nil {
		return nil, errors.New(errNoSecretRef)
	}

	s := &corev1.Secret{}
	if err := c.kube.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, s); err != nil {
		return nil, errors.Wrap(err, errGetSecret)
	}

	return &external{
		db:   c.newClient(s.Data, ptr.Deref(cr.Spec.ForProvider.Database, "")),
		kube: c.kube,
	}, nil
}

type external struct {
	db   xsql.DB
	kube client.Client
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.Grant)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotGrant)
	}

	permissions, err := c.getPermissions(ctx, cr)
	if err != nil {
		return managed.ExternalObservation{}, err
	}
	if len(permissions) == 0 {
		return managed.ExternalObservation{}, nil
	}

	cr.SetConditions(xpv1.Available())

	g, r := diffPermissions(cr.Spec.ForProvider.Permissions.ToStringSlice(), permissions)
	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: len(g) == 0 && len(r) == 0,
	}, nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.Grant)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotGrant)
	}

	username := *cr.Spec.ForProvider.User
	permissions := strings.Join(cr.Spec.ForProvider.Permissions.ToStringSlice(), ", ")

	query := fmt.Sprintf("GRANT %s %s TO %s", permissions, onSchemaQuery(cr), mssql.QuoteIdentifier(username))
	return managed.ExternalCreation{}, errors.Wrap(c.db.Exec(ctx, xsql.Query{String: query}), errGrant)
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.Grant)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotGrant)
	}

	observed, err := c.getPermissions(ctx, cr)
	if err != nil {
		return managed.ExternalUpdate{}, err
	}
	desired := cr.Spec.ForProvider.Permissions.ToStringSlice()
	toGrant, toRevoke := diffPermissions(desired, observed)

	if len(toRevoke) > 0 {
		sort.Strings(toRevoke)
		query := fmt.Sprintf("REVOKE %s %s FROM %s",
			strings.Join(toRevoke, ", "), onSchemaQuery(cr), mssql.QuoteIdentifier(*cr.Spec.ForProvider.User))
		if err = c.db.Exec(ctx, xsql.Query{String: query}); err != nil {
			return managed.ExternalUpdate{}, errors.Wrap(err, errRevoke)
		}
	}
	if len(toGrant) > 0 {
		sort.Strings(toGrant)
		query := fmt.Sprintf("GRANT %s %s TO %s",
			strings.Join(toGrant, ", "), onSchemaQuery(cr), mssql.QuoteIdentifier(*cr.Spec.ForProvider.User))
		if err = c.db.Exec(ctx, xsql.Query{String: query}); err != nil {
			return managed.ExternalUpdate{}, errors.Wrap(err, errGrant)
		}
	}
	return managed.ExternalUpdate{}, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) error {
	cr, ok := mg.(*v1alpha1.Grant)
	if !ok {
		return errors.New(errNotGrant)
	}

	username := *cr.Spec.ForProvider.User

	query := fmt.Sprintf("REVOKE %s %s FROM %s",
		strings.Join(cr.Spec.ForProvider.Permissions.ToStringSlice(), ", "),
		onSchemaQuery(cr),
		mssql.QuoteIdentifier(username),
	)
	return errors.Wrap(c.db.Exec(ctx, xsql.Query{String: query}), errRevoke)
}

// TODO(turkenh/ulucinar): Possible performance improvement. We first
//
//	calculate the Cartesian product, and then filter. It would be more
//	efficient to first filter principals by name, and then join.
const queryPermissionDefault = `SELECT pe.permission_name
	FROM sys.database_principals AS pr
	JOIN sys.database_permissions AS pe
	    ON pe.grantee_principal_id = pr.principal_id
	WHERE
	      pe.class = 0 /* DATABASE (default) */
	  AND pr.name = %s`

const queryPermissionSchema = `SELECT pe.permission_name
	FROM sys.database_principals AS pr
	JOIN sys.database_permissions AS pe
	    ON pe.grantee_principal_id = pr.principal_id
	JOIN sys.schemas AS s
	    ON s.schema_id = pe.major_id
	WHERE
	      pe.class = 3 /* SCHEMA */
	  AND s.name = %s
	  AND pr.name = %s`

func (c *external) getPermissions(ctx context.Context, cr *v1alpha1.Grant) ([]string, error) {
	var query string
	if cr.Spec.ForProvider.Schema == nil {
		query = fmt.Sprintf(queryPermissionDefault, mssql.QuoteValue(*cr.Spec.ForProvider.User))
	} else {
		query = fmt.Sprintf(queryPermissionSchema,
			mssql.QuoteValue(*cr.Spec.ForProvider.Schema),
			mssql.QuoteValue(*cr.Spec.ForProvider.User),
		)
	}
	rows, err := c.db.Query(ctx, xsql.Query{String: query})
	if err != nil {
		return nil, errors.Wrap(err, errCannotGetGrants)
	}
	defer rows.Close() //nolint:errcheck

	var permissions []string
	for rows.Next() {
		var grant string
		if err := rows.Scan(&grant); err != nil {
			return nil, errors.Wrap(err, errCannotGetGrants)
		}
		permissions = append(permissions, grant)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, errCannotGetGrants)
	}
	return permissions, nil
}

func onSchemaQuery(cr *v1alpha1.Grant) (schema string) {
	if cr.Spec.ForProvider.Schema != nil {
		schema = fmt.Sprintf("ON SCHEMA::%s", *cr.Spec.ForProvider.Schema)
	}
	return
}

func diffPermissions(desired, observed []string) ([]string, []string) {
	md := make(map[string]struct{}, len(desired))
	mo := make(map[string]struct{}, len(observed))

	for _, v := range desired {
		md[v] = struct{}{}
	}
	for _, v := range observed {
		mo[v] = struct{}{}
	}

	var toGrant []string
	var toRevoke []string

	for p := range md {
		if _, ok := mo[p]; !ok {
			toGrant = append(toGrant, p)
		}
	}

	for p := range mo {
		if _, ok := md[p]; !ok {
			toRevoke = append(toRevoke, p)
		}
	}

	return toGrant, toRevoke
}
