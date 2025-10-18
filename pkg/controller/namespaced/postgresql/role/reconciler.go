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
	"strings"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"

	"github.com/lib/pq"
	"github.com/pkg/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	xpcontroller "github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/feature"
	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/password"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"

	namespacedv1alpha1 "github.com/crossplane-contrib/provider-sql/apis/namespaced/postgresql/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/postgresql"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
	"github.com/crossplane-contrib/provider-sql/pkg/controller/namespaced/postgresql/provider"
)

const (
	errTrackPCUsage = "cannot track ProviderConfig usage"

	errNotRole                 = "managed resource is not a Role custom resource"
	errSelectRole              = "cannot select role"
	errCreateRole              = "cannot create role"
	errDropRole                = "cannot drop role"
	errUpdateRole              = "cannot update role"
	errGetPasswordSecretFailed = "cannot get password secret"
	errComparePrivileges       = "cannot compare desired and observed privileges"
	errSetRoleConfigs          = "cannot set role configuration parameters"

	maxConcurrency = 5
)

// TODO(nateinaction): This looks wrong, can tracker creation be improved?
type tracker struct {
	tracker *resource.ProviderConfigUsageTracker
}

var _ resource.Tracker = &tracker{}

func (t *tracker) Track(ctx context.Context, mg resource.Managed) error {
	return t.tracker.Track(ctx, mg.(resource.ModernManaged))
}

// Setup adds a controller that reconciles Database managed resources.
func Setup(mgr ctrl.Manager, o xpcontroller.Options) error {
	name := managed.ControllerName(namespacedv1alpha1.RoleGroupKind)

	t := resource.NewProviderConfigUsageTracker(mgr.GetClient(), &namespacedv1alpha1.ProviderConfigUsage{})
	trk := &tracker{tracker: t}

	reconcilerOptions := []managed.ReconcilerOption{
		managed.WithTypedExternalConnector(&connector{kube: mgr.GetClient(), usage: trk, newDB: postgresql.New}),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithPollInterval(o.PollInterval),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))),
	}
	if o.Features.Enabled(feature.EnableBetaManagementPolicies) {
		reconcilerOptions = append(reconcilerOptions, managed.WithManagementPolicies())
	}
	r := managed.NewReconciler(mgr,
		resource.ManagedKind(namespacedv1alpha1.RoleGroupVersionKind),
		reconcilerOptions...,
	)
	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		For(&namespacedv1alpha1.Role{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: maxConcurrency,
		}).
		Complete(r)
}

type connector struct {
	kube  client.Client
	usage resource.Tracker
	newDB func(creds map[string][]byte, database string, sslmode string) xsql.DB
}

var _ managed.TypedExternalConnector[resource.Managed] = &connector{}

func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.TypedExternalClient[resource.Managed], error) {
	cr, ok := mg.(*namespacedv1alpha1.Role)
	if !ok {
		return nil, errors.New(errNotRole)
	}

	if err := c.usage.Track(ctx, mg); err != nil {
		return nil, errors.Wrap(err, errTrackPCUsage)
	}

	// ProviderConfigReference could theoretically be nil, but in practice the
	// DefaultProviderConfig initializer will set it before we get here.
	providerInfo, err := provider.GetProviderConfig(ctx, c.kube, cr)
	if err != nil {
		return nil, err
	}

	return &external{
		db:   c.newDB(providerInfo.SecretData, providerInfo.DefaultDatabase, clients.ToString(providerInfo.SSLMode)),
		kube: c.kube,
	}, nil
}

type external struct {
	db   xsql.DB
	kube client.Client
}

var _ managed.TypedExternalClient[resource.Managed] = &external{}

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

func privilegesToClauses(p namespacedv1alpha1.RolePrivilege) []string {
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

	return pc
}

func changedPrivs(existing []string, desired []string) ([]string, error) {
	out := []string{}

	// Make sure existing observation has at least as many items as
	// desired. If it does not, then we cannot safely compare
	// privileges.
	if len(existing) < len(desired) {
		return nil, errors.New(errComparePrivileges)
	}

	// The input slices here are outputted by privilegesToClauses above.
	// Because these are created by repeated calls to negateClause in the
	// same order, we can rely on each clause being in the same array
	// position in the 'desired' and 'existing' inputs.

	for i, v := range desired {
		if v != existing[i] {
			out = append(out, v)
		}
	}
	return out, nil
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*namespacedv1alpha1.Role)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotRole)
	}

	observed := &namespacedv1alpha1.RoleParameters{
		Privileges: namespacedv1alpha1.RolePrivilege{
			SuperUser:   new(bool),
			Inherit:     new(bool),
			CreateDb:    new(bool),
			CreateRole:  new(bool),
			Login:       new(bool),
			Replication: new(bool),
			BypassRls:   new(bool),
		},
	}

	query := "SELECT " +
		"rolsuper, " +
		"rolinherit, " +
		"rolcreatedb, " +
		"rolcreaterole, " +
		"rolcanlogin, " +
		"rolreplication, " +
		"rolbypassrls, " +
		"rolconnlimit, " +
		"rolconfig " +
		"FROM pg_roles WHERE rolname = $1"

	var rolconfigs []string
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
		pq.Array(&rolconfigs),
	)

	if xsql.IsNoRows(err) {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errSelectRole)
	}
	if len(rolconfigs) > 0 {
		var rc []namespacedv1alpha1.RoleConfigurationParameter
		for _, c := range rolconfigs {
			kv := strings.Split(c, "=")
			rc = append(rc, namespacedv1alpha1.RoleConfigurationParameter{
				Name:  kv[0],
				Value: kv[1],
			})
		}
		observed.ConfigurationParameters = &rc
	}
	cr.Status.AtProvider.ConfigurationParameters = observed.ConfigurationParameters

	_, pwdChanged, err := c.getPassword(ctx, cr)
	if err != nil {
		return managed.ExternalObservation{}, err
	}

	cr.SetConditions(xpv1.Available())

	// PrivilegesAsClauses is used as role status output
	cr.Status.AtProvider.PrivilegesAsClauses = privilegesToClauses(observed.Privileges)

	return managed.ExternalObservation{
		ResourceExists:          true,
		ResourceLateInitialized: lateInit(observed, &cr.Spec.ForProvider),
		ResourceUpToDate:        !pwdChanged && upToDate(observed, &cr.Spec.ForProvider),
	}, nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*namespacedv1alpha1.Role)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotRole)
	}

	cr.SetConditions(xpv1.Creating())

	crn := pq.QuoteIdentifier(meta.GetExternalName(cr))
	privs := privilegesToClauses(cr.Spec.ForProvider.Privileges)

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
			strings.Join(privs, " "),
		),
	}); err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, errCreateRole)
	}

	// PrivilegesAsClauses is used as role status output
	// Update here so that state is reflected to the user prior to the next
	// reconciler loop.
	cr.Status.AtProvider.PrivilegesAsClauses = privs
	if cr.Spec.ForProvider.ConfigurationParameters != nil {
		for _, v := range *cr.Spec.ForProvider.ConfigurationParameters {
			if err := c.db.Exec(ctx, xsql.Query{
				String: fmt.Sprintf("ALTER ROLE %s set %s=%s", crn, pq.QuoteIdentifier(v.Name), pq.QuoteIdentifier(v.Value)),
			}); err != nil {
				return managed.ExternalCreation{}, errors.Wrap(err, errSetRoleConfigs)
			}
		}
		cr.Status.AtProvider.ConfigurationParameters = cr.Spec.ForProvider.ConfigurationParameters
	}

	return managed.ExternalCreation{
		ConnectionDetails: c.db.GetConnectionDetails(meta.GetExternalName(cr), pw),
	}, nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) { //nolint:gocyclo
	// NOTE(benagricola): This is just a touch over the cyclomatic complexity
	// limit, but is unlikely to become more complex unless new role features
	// are added. Think about splitting this method up if new functionality
	// is desired.
	cr, ok := mg.(*namespacedv1alpha1.Role)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotRole)
	}

	pw, pwchanged, err := c.getPassword(ctx, cr)
	if err != nil {
		return managed.ExternalUpdate{}, err
	}

	crn := pq.QuoteIdentifier(meta.GetExternalName(cr))

	if pwchanged {
		if err := c.db.Exec(ctx, xsql.Query{
			String: fmt.Sprintf("ALTER ROLE %s PASSWORD %s", crn, pq.QuoteLiteral(pw)),
		}); err != nil {
			return managed.ExternalUpdate{}, errors.Wrap(err, errUpdateRole)
		}
	}

	privs := privilegesToClauses(cr.Spec.ForProvider.Privileges)
	cp, err := changedPrivs(cr.Status.AtProvider.PrivilegesAsClauses, privs)

	if err != nil {
		return managed.ExternalUpdate{}, errors.Wrap(err, errUpdateRole)
	}

	if len(cp) > 0 {
		if err := c.db.Exec(ctx, xsql.Query{
			String: fmt.Sprintf("ALTER ROLE %s %s", crn, strings.Join(cp, " ")),
		}); err != nil {
			return managed.ExternalUpdate{}, errors.Wrap(err, errUpdateRole)
		}
	}

	// PrivilegesAsClauses is used as role status output
	// Update here so that state is reflected to the user prior to the next
	// reconciler loop.
	cr.Status.AtProvider.PrivilegesAsClauses = privs

	// Checks if current role configuration parameters differs from desired state.
	// If difference, reset all parameters and apply desired parameters in a transaction
	if cr.Spec.ForProvider.ConfigurationParameters != nil && !cmp.Equal(cr.Status.AtProvider.ConfigurationParameters, cr.Spec.ForProvider.ConfigurationParameters,
		cmpopts.SortSlices(func(o, d namespacedv1alpha1.RoleConfigurationParameter) bool { return o.Name < d.Name })) {
		q := make([]xsql.Query, 0)
		q = append(q, xsql.Query{
			String: fmt.Sprintf("ALTER ROLE %s RESET ALL", crn),
		})
		// search_path="$user", public is valid so need to handle that
		for _, v := range *cr.Spec.ForProvider.ConfigurationParameters {
			sb := strings.Builder{}
			values := strings.Split(v.Value, ",")
			for i, v := range values {
				sb.WriteString(pq.QuoteLiteral(strings.TrimSpace(strings.Trim(v, "'\""))))
				if i < len(values)-1 {
					sb.WriteString(",")
				}
			}
			q = append(q, xsql.Query{
				String: fmt.Sprintf("ALTER ROLE %s set %s=%s", crn, pq.QuoteIdentifier(v.Name), sb.String()),
			})
		}
		if err := c.db.ExecTx(ctx, q); err != nil {
			return managed.ExternalUpdate{}, errors.Wrap(err, errUpdateRole)
		}
		// Update state to reflect the current configuration parameters
		cr.Status.AtProvider.ConfigurationParameters = cr.Spec.ForProvider.ConfigurationParameters
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

func (c *external) Delete(ctx context.Context, mg resource.Managed) (managed.ExternalDelete, error) {
	cr, ok := mg.(*namespacedv1alpha1.Role)
	if !ok {
		return managed.ExternalDelete{}, errors.New(errNotRole)
	}
	cr.SetConditions(xpv1.Deleting())
	err := c.db.Exec(ctx, xsql.Query{
		String: "DROP ROLE IF EXISTS " + pq.QuoteIdentifier(meta.GetExternalName(cr)),
	})
	return managed.ExternalDelete{}, errors.Wrap(err, errDropRole)
}

func upToDate(observed *namespacedv1alpha1.RoleParameters, desired *namespacedv1alpha1.RoleParameters) bool {
	if observed.ConnectionLimit != desired.ConnectionLimit {
		return false
	}
	if observed.Privileges.SuperUser != desired.Privileges.SuperUser {
		return false
	}
	if observed.Privileges.Inherit != desired.Privileges.Inherit {
		return false
	}
	if observed.Privileges.CreateDb != desired.Privileges.CreateDb {
		return false
	}
	if observed.Privileges.CreateRole != desired.Privileges.CreateRole {
		return false
	}
	if observed.Privileges.Login != desired.Privileges.Login {
		return false
	}
	if observed.Privileges.Replication != desired.Privileges.Replication {
		return false
	}
	if observed.Privileges.BypassRls != desired.Privileges.BypassRls {
		return false
	}
	if !cmp.Equal(observed.ConfigurationParameters, desired.ConfigurationParameters,
		cmpopts.SortSlices(func(o, d namespacedv1alpha1.RoleConfigurationParameter) bool { return o.Name < d.Name })) {
		return false
	}
	return true
}

func lateInit(observed *namespacedv1alpha1.RoleParameters, desired *namespacedv1alpha1.RoleParameters) bool {
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

func (c *external) Disconnect(ctx context.Context) error {
	return nil
}
