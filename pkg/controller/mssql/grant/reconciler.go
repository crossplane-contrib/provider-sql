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

package grant

import (
	"context"
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/event"
	"github.com/crossplane/crossplane-runtime/pkg/logging"
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

	errNotGrant     = "managed resource is not a Grant custom resource"
	errCreateGrant  = "cannot create grant"
	errRevokeGrant  = "cannot revoke grant"
	errCannotGetGrants = "cannot get current grants"
	errFlushPriv    = "cannot flush privileges"

	errCodeNoSuchGrant = 1141
	maxConcurrency     = 5
)

// Setup adds a controller that reconciles Grant managed resources.
func Setup(mgr ctrl.Manager, l logging.Logger) error {
	name := managed.ControllerName(v1alpha1.GrantGroupKind)

	t := resource.NewProviderConfigUsageTracker(mgr.GetClient(), &v1alpha1.ProviderConfigUsage{})
	r := managed.NewReconciler(mgr,
		resource.ManagedKind(v1alpha1.GrantGroupVersionKind),
		managed.WithExternalConnecter(&connector{kube: mgr.GetClient(), usage: t, newDB: mssql.New}),
		managed.WithReferenceResolver(managed.NewAPISimpleReferenceResolver(mgr.GetClient())),
		managed.WithLogger(l.WithValues("controller", name)),
		managed.WithPollInterval(10*time.Minute),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))))

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		For(&v1alpha1.Grant{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: maxConcurrency,
		}).
		Complete(r)
}

type connector struct {
	kube  client.Client
	usage resource.Tracker
	newDB func(creds map[string][]byte, database string) xsql.DB
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
	// support one source (MySQLConnectionSecret), which is required and
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
		db:   c.newDB(s.Data, stringValue(cr.Spec.ForProvider.Database)),
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

	username := *cr.Spec.ForProvider.User

	permissions, err := c.getPermissions(ctx, username)
	if err != nil {
		return managed.ExternalObservation{}, err
	}
	if len(permissions) == 0 {
		return managed.ExternalObservation{}, nil
	}

	cr.SetConditions(xpv1.Available())

	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: permissionsEqual(cr.Spec.ForProvider.Permissions.ToStringSlice(), permissions),
	}, nil
}

func permissionsEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	sort.Strings(a)
	sort.Strings(b)

	for i := range a {
		if a[i] != b[i] {
			return false
		}
	}
	return true
}


func (c *external) getPermissions(ctx context.Context, username string) ([]string, error) {
	query := fmt.Sprintf(`SELECT pe.permission_name
	FROM sys.database_principals AS pr  
	JOIN sys.database_permissions AS pe  
	    ON pe.grantee_principal_id = pr.principal_id  
	WHERE
	  pr.name = %s`, mssql.QuoteValue(username))
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
		if strings.EqualFold(grant, "CONNECT") {
			// Ignore CONNECT permission which is granted by default at user
			// creation.
			continue
		}
		permissions = append(permissions, grant)
	}
	if err := rows.Err(); err != nil {
		return nil, errors.Wrap(err, errCannotGetGrants)
	}
	return permissions, nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.Grant)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotGrant)
	}

	username := *cr.Spec.ForProvider.User
	permissions := strings.Join(cr.Spec.ForProvider.Permissions.ToStringSlice(), ", ")

	query := fmt.Sprintf("GRANT %s TO %s", permissions, mssql.QuoteIdentifier(username))
	return managed.ExternalCreation{}, errors.Wrap(c.db.Exec(ctx, xsql.Query{String: query}), errCreateGrant)
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	_, ok := mg.(*v1alpha1.Grant)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotGrant)
	}
	return managed.ExternalUpdate{}, nil
/*
	username := *cr.Spec.ForProvider.User
	dbname := *cr.Spec.ForProvider.Database

	privileges := strings.Join(cr.Spec.ForProvider.Permissions.ToStringSlice(), ", ")

	// Remove current grants since it's not possible to update grants.
	// This might leave applications with no access to the DB for a short time
	// until the privileges are granted again.
	// Using a transaction is unfortunately not possible because a GRANT triggers
	// an implicit commit: https://dev.mssql.com/doc/refman/8.0/en/implicit-commit.html
	query := fmt.Sprintf("REVOKE ALL ON %s.* FROM %s@%s",
		mssql.QuoteIdentifier(dbname),
		mssql.QuoteValue(username),
	)
	if err := c.db.Exec(ctx, xsql.Query{String: query}); err != nil {
		return managed.ExternalUpdate{}, errors.Wrap(err, errRevokeGrant)
	}

	query = createGrantQuery(privileges, username)
	if err := c.db.Exec(ctx, xsql.Query{String: query}); err != nil {
		return managed.ExternalUpdate{}, err
	}
	err := c.db.Exec(ctx, xsql.Query{String: "FLUSH PRIVILEGES"})
	return managed.ExternalUpdate{}, errors.Wrap(err, errFlushPriv)*/
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) error {
	cr, ok := mg.(*v1alpha1.Grant)
	if !ok {
		return errors.New(errNotGrant)
	}

	username := *cr.Spec.ForProvider.User

	privileges := strings.Join(cr.Spec.ForProvider.Permissions.ToStringSlice(), ", ")

	query := fmt.Sprintf("REVOKE %s FROM %s",
		privileges,
		mssql.QuoteIdentifier(username),
	)
	return errors.Wrap(c.db.Exec(ctx, xsql.Query{String: query}), errRevokeGrant)
}

func stringValue(p *string) string  {
	if p == nil {
		return ""
	}
	return *p
}