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
	"fmt"
	"sort"
	"strings"
	"time"

	"github.com/lib/pq"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/event"
	"github.com/crossplane/crossplane-runtime/pkg/logging"
	"github.com/crossplane/crossplane-runtime/pkg/meta"
	"github.com/crossplane/crossplane-runtime/pkg/password"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/resource"

	"github.com/crossplane-contrib/provider-sql/apis/postgresql/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/postgresql"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
)

const (
	errTrackPCUsage = "cannot track ProviderConfig usage"
	errGetPC        = "cannot get ProviderConfig"
	errNoSecretRef  = "ProviderConfig does not reference a credentials Secret"
	errGetSecret    = "cannot get credentials Secret"

	errNotRole                 = "managed resource is not a Role custom resource"
	errSelectRole              = "cannot select role"
	errCreateRole              = "cannot create role"
	errDropRole                = "cannot drop role"
	errUpdateRole              = "cannot update role"
	errGetPasswordSecretFailed = "cannot get password secret"
)

// Setup adds a controller that reconciles Role managed resources.
func Setup(mgr ctrl.Manager, l logging.Logger) error {
	name := managed.ControllerName(v1alpha1.RoleGroupKind)

	t := resource.NewProviderConfigUsageTracker(mgr.GetClient(), &v1alpha1.ProviderConfigUsage{})
	r := managed.NewReconciler(mgr,
		resource.ManagedKind(v1alpha1.RoleGroupVersionKind),
		managed.WithExternalConnecter(&connector{kube: mgr.GetClient(), usage: t, newDB: postgresql.New}),
		managed.WithLogger(l.WithValues("controller", name)),
		managed.WithShortWait(10*time.Second),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))))

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		For(&v1alpha1.Role{}).
		Complete(r)
}

type connector struct {
	kube  client.Client
	usage resource.Tracker
	newDB func(creds map[string][]byte) xsql.DB
}

func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) {
	cr, ok := mg.(*v1alpha1.Role)
	if !ok {
		return nil, errors.New(errNotRole)
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
	// support one source (PostgreSQLConnectionSecret), which is required and
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
		db:   c.newDB(s.Data),
		kube: c.kube,
	}, nil
}

type external struct {
	db   xsql.DB
	kube client.Client
}

func negateClause(clause string, negate *bool, out *[]string) {
	// If clause boolean is not set (nil pointer), do not push a setting.
	// This means the postgres default is applied.
	if negate == nil {
		return
	}

	if !(*negate) {
		clause = "NO" + clause
	}
	*out = append(*out, clause)
}

func privilegesToPermissionClauses(p v1alpha1.RolePrivilege) string {
	// Never copy user inputted data to this string. These values are
	// passed directly into the query.
	pc := []string{}

	negateClause("SUPERUSER", p.SuperUser, &pc)
	negateClause("INHERIT", p.Inherit, &pc)
	negateClause("CREATEDB", p.CreateDb, &pc)
	negateClause("CREATEROLE", p.CreateRole, &pc)
	negateClause("LOGIN", p.Login, &pc)
	negateClause("REPLICATION", p.Replication, &pc)
	negateClause("BYPASSRLS", p.BypassRls, &pc)

	sort.Strings(pc)
	return strings.Join(pc, " ")
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.Role)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotRole)
	}

	observed := &v1alpha1.RoleObservation{
		Privileges: &v1alpha1.RolePrivilege{
			SuperUser:   new(bool),
			Inherit:     new(bool),
			CreateDb:    new(bool),
			CreateRole:  new(bool),
			Login:       new(bool),
			Replication: new(bool),
			BypassRls:   new(bool),
		},
		PrivilegesAsClauses: "",
	}

	query := "SELECT " +
		"rolsuper, " +
		"rolinherit, " +
		"rolcreatedb, " +
		"rolcreaterole, " +
		"rolcanlogin, " +
		"rolreplication, " +
		"rolbypassrls, " +
		"rolconnlimit " +
		"FROM pg_roles WHERE rolname = $1"

	err := c.db.Scan(ctx,
		xsql.Query{
			String: query,
			Parameters: []interface{}{
				meta.GetExternalName(cr),
			},
		},
		&observed.Privileges.SuperUser,
		&observed.Privileges.Inherit,
		&observed.Privileges.CreateDb,
		&observed.Privileges.CreateRole,
		&observed.Privileges.Login,
		&observed.Privileges.Replication,
		&observed.Privileges.BypassRls,
		&observed.ConnectionLimit,
	)

	if xsql.IsNoRows(err) {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errSelectRole)
	}

	_, pwdChanged, err := c.getPassword(ctx, cr)
	if err != nil {
		return managed.ExternalObservation{}, err
	}

	observed.PrivilegesAsClauses = privilegesToPermissionClauses(*observed.Privileges)

	// Load privileges from query to status
	cr.Status.AtProvider = *observed

	cr.SetConditions(xpv1.Available())

	return managed.ExternalObservation{
		ResourceExists:          true,
		ResourceLateInitialized: lateInit(observed, &cr.Spec.ForProvider),
		ResourceUpToDate:        !pwdChanged && upToDate(observed, &cr.Spec.ForProvider),
	}, nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.Role)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotRole)
	}

	cr.SetConditions(xpv1.Creating())

	crn := pq.QuoteIdentifier(meta.GetExternalName(cr))
	privs := privilegesToPermissionClauses(cr.Spec.ForProvider.Privileges)

	pw, _, err := c.getPassword(ctx, cr)
	if err != nil {
		return managed.ExternalCreation{}, err
	}

	if pw == "" {
		pw, err = password.Generate()
		if err != nil {
			return managed.ExternalCreation{}, err
		}
	}

	// NOTE we're not using pq's "Parameters" setting here
	// because it does not allow us to pass identifiers.
	if err := c.db.Exec(ctx, xsql.Query{
		String: fmt.Sprintf(
			"CREATE ROLE %s PASSWORD %s %s",
			crn,
			pq.QuoteLiteral(pw),
			privs,
		),
	}); err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, errCreateRole)
	}

	return managed.ExternalCreation{
		ConnectionDetails: c.db.GetConnectionDetails(meta.GetExternalName(cr), pw),
	}, nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.Role)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotRole)
	}

	pw, pwchanged, err := c.getPassword(ctx, cr)
	if err != nil {
		return managed.ExternalUpdate{}, err
	}

	crn := pq.QuoteIdentifier(meta.GetExternalName(cr))
	privs := privilegesToPermissionClauses(cr.Spec.ForProvider.Privileges)

	if pwchanged {
		if err := c.db.Exec(ctx, xsql.Query{
			String: fmt.Sprintf("ALTER ROLE %s PASSWORD %s", crn, pq.QuoteLiteral(pw)),
		}); err != nil {
			return managed.ExternalUpdate{}, errors.Wrap(err, errUpdateRole)
		}
	}

	if len(privs) > 0 {
		if err := c.db.Exec(ctx, xsql.Query{
			String: fmt.Sprintf("ALTER ROLE %s %s", crn, privs),
		}); err != nil {
			return managed.ExternalUpdate{}, errors.Wrap(err, errUpdateRole)
		}
	}

	cl := cr.Spec.ForProvider.ConnectionLimit
	if cl != nil {
		if err := c.db.Exec(ctx, xsql.Query{
			String: fmt.Sprintf("ALTER ROLE %s CONNECTION LIMIT %d", crn, int64(*cl)),
		}); err != nil {
			return managed.ExternalUpdate{}, errors.Wrap(err, errUpdateRole)
		}
	}

	// Only update connection details if password is changed
	if pwchanged {
		return managed.ExternalUpdate{
			ConnectionDetails: c.db.GetConnectionDetails(meta.GetExternalName(cr), pw),
		}, nil
	}
	return managed.ExternalUpdate{}, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) error {
	cr, ok := mg.(*v1alpha1.Role)
	if !ok {
		return errors.New(errNotRole)
	}
	cr.SetConditions(xpv1.Deleting())
	err := c.db.Exec(ctx, xsql.Query{
		String: "DROP ROLE IF EXISTS" + pq.QuoteIdentifier(meta.GetExternalName(cr)),
	})
	return errors.Wrap(err, errDropRole)
}

func upToDate(observed *v1alpha1.RoleObservation, desired *v1alpha1.RoleParameters) bool {
	if observed.ConnectionLimit != desired.ConnectionLimit {
		return false
	}

	// Permission clauses are sorted, so we can just compare the string
	if observed.PrivilegesAsClauses != privilegesToPermissionClauses(desired.Privileges) {
		return false
	}
	return true
}

func lateInit(observed *v1alpha1.RoleObservation, desired *v1alpha1.RoleParameters) bool {
	li := false

	if desired.Privileges.SuperUser == nil {
		desired.Privileges.SuperUser = observed.Privileges.SuperUser
		li = true
	}
	if desired.Privileges.Inherit == nil {
		desired.Privileges.Inherit = observed.Privileges.Inherit
		li = true
	}
	if desired.Privileges.CreateDb == nil {
		desired.Privileges.CreateDb = observed.Privileges.CreateDb
		li = true
	}
	if desired.Privileges.CreateRole == nil {
		desired.Privileges.CreateRole = observed.Privileges.CreateRole
		li = true
	}
	if desired.Privileges.Login == nil {
		desired.Privileges.Login = observed.Privileges.Login
		li = true
	}
	if desired.Privileges.Replication == nil {
		desired.Privileges.Replication = observed.Privileges.Replication
		li = true
	}
	if desired.Privileges.BypassRls == nil {
		desired.Privileges.BypassRls = observed.Privileges.BypassRls
		li = true
	}
	if desired.ConnectionLimit == nil {
		desired.ConnectionLimit = observed.ConnectionLimit
		li = true
	}

	return li
}
