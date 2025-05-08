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

package default_privileges

import (
	"context"
	"fmt"
	"sort"
	"strings"

	"github.com/lib/pq"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	xpcontroller "github.com/crossplane/crossplane-runtime/pkg/controller"
	"github.com/crossplane/crossplane-runtime/pkg/event"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/resource"

	"github.com/crossplane-contrib/provider-sql/apis/postgresql/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/postgresql"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
)

const (
	errTrackPCUsage = "cannot track ProviderConfig usage"
	errGetPC        = "cannot get ProviderConfig"
	errNoSecretRef  = "ProviderConfig does not reference a credentials Secret"
	errGetSecret    = "cannot get credentials Secret"

	errNotDefaultPrivileges    = "managed resource is not a Grant custom resource"
	errSelectDefaultPrivileges = "cannot select default privileges"
	errCreateDefaultPrivileges = "cannot create default privileges"
	errRevokeDefaultPrivileges = "cannot revoke default privileges"
	errNoRole                  = "role not passed or could not be resolved"
	errNoTargetRole            = "target role not passed or could not be resolved"
	errNoObjectType            = "object type not passed"
	errNoDatabase              = "database not passed or could not be resolved"
	errNoPrivileges            = "privileges not passed"
	errUnknownGrant            = "cannot identify grant type based on passed params"

	maxConcurrency = 5
)

// Setup adds a controller that reconciles Grant managed resources.
func Setup(mgr ctrl.Manager, o xpcontroller.Options) error {
	name := managed.ControllerName(v1alpha1.DefaultPrivilegesGroupKind)

	t := resource.NewProviderConfigUsageTracker(mgr.GetClient(), &v1alpha1.ProviderConfigUsage{})
	r := managed.NewReconciler(mgr,
		resource.ManagedKind(v1alpha1.DefaultPrivilegesGroupVersionKind),
		managed.WithExternalConnecter(&connector{kube: mgr.GetClient(), usage: t, newDB: postgresql.New}),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithPollInterval(o.PollInterval),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))))

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		For(&v1alpha1.DefaultPrivileges{}).
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

func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) {
	cr, ok := mg.(*v1alpha1.DefaultPrivileges)
	if !ok {
		return nil, errors.New(errNotDefaultPrivileges)
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
		db:   c.newDB(s.Data, pc.Spec.DefaultDatabase, clients.ToString(pc.Spec.SSLMode)),
		kube: c.kube,
	}, nil
}

type external struct {
	db   xsql.DB
	kube client.Client
}

var (
	objectTypes = map[string]string{
		"table":    "r",
		"sequence": "S",
		"function": "f",
		"type":     "T",
		"schema":   "n",
	}
)

func selectDefaultPrivilegesQuery(gp v1alpha1.DefaultPrivilegesParameters, q *xsql.Query) {
	sqlString := `
	select distinct(default_acl.privilege_type)
	from pg_roles r
	join (SELECT defaclnamespace, (aclexplode(defaclacl)).* FROM pg_default_acl
	WHERE defaclobjtype = $1) default_acl
	on r.oid = default_acl.grantee
	where r.rolname = $2;
	`
	q.String = sqlString
	q.Parameters = []interface{}{
		objectTypes[*gp.ObjectType],
		*gp.Role,
	}

}

func withOption(option *v1alpha1.GrantOption) string {
	if option != nil {
		return fmt.Sprintf("WITH %s OPTION", string(*option))
	}
	return ""
}

func inSchema(params *v1alpha1.DefaultPrivilegesParameters) string {
	if params.Schema != nil {
		return fmt.Sprintf("IN SCHEMA %s", pq.QuoteIdentifier(*params.Schema))
	}
	return ""
}

func createDefaultPrivilegesQuery(gp v1alpha1.DefaultPrivilegesParameters, q *xsql.Query) {

	roleName := pq.QuoteIdentifier(*gp.Role)

	targetRoleName := pq.QuoteIdentifier(*gp.TargetRole)

	query := strings.TrimSpace(fmt.Sprintf(
		"ALTER DEFAULT PRIVILEGES FOR ROLE %s %s GRANT %s ON %sS TO %s %s",
		targetRoleName,
		inSchema(&gp),
		strings.Join(gp.Privileges.ToStringSlice(), ","),
		*gp.ObjectType,
		roleName,
		withOption(gp.WithOption),
	))

	q.String = query
}

func deleteDefaultPrivilegesQuery(gp v1alpha1.DefaultPrivilegesParameters, q *xsql.Query) {
	roleName := pq.QuoteIdentifier(*gp.Role)
	targetRoleName := pq.QuoteIdentifier(*gp.TargetRole)
	objectType := objectTypes[*gp.ObjectType]

	query := strings.TrimSpace(fmt.Sprintf(
		"ALTER DEFAULT PRIVILEGES FOR ROLE %s %s REVOKE ALL ON %s TO %s %s",
		targetRoleName,
		inSchema(&gp),
		objectType,
		roleName,
		withOption(gp.WithOption),
	))

	q.String = query

}

func matchingGrants(currentGrants []string, specGrants []string) bool {
	if len(currentGrants) != len(specGrants) {
		return false
	}

	sort.Strings(currentGrants)
	sort.Strings(specGrants)

	for i, g := range currentGrants {
		if g != specGrants[i] {
			return false
		}
	}

	return true
}
func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.DefaultPrivileges)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotDefaultPrivileges)
	}

	if cr.Spec.ForProvider.Role == nil {
		return managed.ExternalObservation{}, errors.New(errNoRole)
	}

	if cr.Spec.ForProvider.TargetRole == nil {
		return managed.ExternalObservation{}, errors.New(errNoTargetRole)
	}

	if cr.Spec.ForProvider.ObjectType == nil {
		return managed.ExternalObservation{}, errors.New(errNoObjectType)
	}

	gp := cr.Spec.ForProvider
	var query xsql.Query
	selectDefaultPrivilegesQuery(gp, &query)

	var defaultPrivileges []string

	rows, err := c.db.Query(ctx, query)
	if xsql.IsNoRows(err) {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}

	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errSelectDefaultPrivileges)
	}
	defer rows.Close()
	for rows.Next() {
		var privilege string
		if err := rows.Scan(&privilege); err != nil {
			return managed.ExternalObservation{}, errors.Wrap(err, errSelectDefaultPrivileges)
		}
		defaultPrivileges = append(defaultPrivileges, privilege)
	}

	// Check for any errors encountered during iteration
	if err := rows.Err(); err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errSelectDefaultPrivileges)
	}

	// If no default privileges are found, the resource does not exist.
	// Maybe this is covered by the xsql.IsNoRows(err) check above?
	if len(defaultPrivileges) == 0 {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}

	cr.SetConditions(xpv1.Available())

	resourceMatches := matchingGrants(defaultPrivileges, gp.Privileges.ToStringSlice())
	return managed.ExternalObservation{
		ResourceLateInitialized: false,
		// check that the list of grants matches the expected grants
		// if not, the resource is not up to date.
		// Because create first revokes all grants and then grants them again,
		// we can assume that if the grants are present, they are up to date.
		ResourceExists:   resourceMatches,
		ResourceUpToDate: resourceMatches,
	}, nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.DefaultPrivileges)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotDefaultPrivileges)
	}

	cr.SetConditions(xpv1.Creating())

	var createQuery xsql.Query
	createDefaultPrivilegesQuery(cr.Spec.ForProvider, &createQuery)

	var deleteQuery xsql.Query
	deleteDefaultPrivilegesQuery(cr.Spec.ForProvider, &deleteQuery)

	err := c.db.ExecTx(ctx, []xsql.Query{
		deleteQuery, createQuery,
	})

	return managed.ExternalCreation{}, errors.Wrap(err, errCreateDefaultPrivileges)
}

func (c *external) Update(
	ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	// Update is a no-op, as permissions are fully revoked and then granted in the Create function,
	// inside a transaction. Same approach as the grant resource.
	return managed.ExternalUpdate{}, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) error {
	cr, ok := mg.(*v1alpha1.DefaultPrivileges)
	if !ok {
		return errors.New(errNotDefaultPrivileges)
	}
	var query xsql.Query

	cr.SetConditions(xpv1.Deleting())

	deleteDefaultPrivilegesQuery(cr.Spec.ForProvider, &query)

	return errors.Wrap(c.db.Exec(ctx, query), errRevokeDefaultPrivileges)
}
