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

	"github.com/lib/pq"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/event"
	"github.com/crossplane/crossplane-runtime/pkg/logging"
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

	errNotGrant     = "managed resource is not a Grant custom resource"
	errSelectGrant  = "cannot select grant"
	errCreateGrant  = "cannot create grant"
	errRevokeGrant  = "cannot revoke grant"
	errNoRole       = "role not passed or could not be resolved"
	errNoDatabase   = "database not passed or could not be resolved"
	errNoPrivileges = "privileges not passed"
	errUnknownGrant = "cannot identify grant type based on passed params"

	errInvalidParams = "invalid parameters for grant type %s"
)

// Setup adds a controller that reconciles Grant managed resources.
func Setup(mgr ctrl.Manager, l logging.Logger) error {
	name := managed.ControllerName(v1alpha1.GrantGroupKind)

	t := resource.NewProviderConfigUsageTracker(mgr.GetClient(), &v1alpha1.ProviderConfigUsage{})
	r := managed.NewReconciler(mgr,
		resource.ManagedKind(v1alpha1.GrantGroupVersionKind),
		managed.WithExternalConnecter(&connector{kube: mgr.GetClient(), usage: t, newDB: postgresql.New}),
		managed.WithLogger(l.WithValues("controller", name)),
		managed.WithShortWait(10*time.Second),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))))

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		For(&v1alpha1.Grant{}).
		Complete(r)
}

type connector struct {
	kube  client.Client
	usage resource.Tracker
	newDB func(creds map[string][]byte) xsql.DB
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

type grantType string

const (
	roleMember   grantType = "ROLE_MEMBER"
	roleDatabase grantType = "ROLE_DATABASE"
	roleTable    grantType = "ROLE_TABLE"
)

func identifyGrantType(gp v1alpha1.GrantParameters) (grantType, error) {
	tc := len(gp.Tables)
	pc := len(gp.Privileges)

	// If memberOf is specified, this is ROLE_MEMBER
	// NOTE: If any of these are set, even if the lookup by ref or selector fails,
	// then this is still a roleMember grant type.
	if gp.MemberOfRef != nil || gp.MemberOfSelector != nil || gp.MemberOf != nil {
		if gp.Database != nil || tc > 0 || pc > 0 {
			return "", errors.Errorf(errInvalidParams, roleMember)
		}
		return roleMember, nil
	}

	if gp.Database == nil {
		return "", errors.New(errNoDatabase)
	}

	if pc < 1 {
		return "", errors.New(errNoPrivileges)
	}

	// If tables are specified, this is ROLE_TABLE
	if tc > 0 {
		return roleTable, nil
	}

	// Otherwise, this is ROLE_DATABASE
	return roleDatabase, nil
}

func selectGrantQuery(gp v1alpha1.GrantParameters, q *xsql.Query) error {
	gt, err := identifyGrantType(gp)
	if err != nil {
		return err
	}

	switch gt {
	case roleMember:

		// Always returns a row with a true or false value
		// A simpler query would use ::regrol to cast the
		// roleid and member oids to their role names, but
		// if this is used with a nonexistent role name it will
		// throw an error rather than return false.
		q.String = "SELECT EXISTS(SELECT 1 FROM pg_auth_members m " +
			"INNER JOIN pg_authid r ON m.roleid = r.oid " +
			"INNER JOIN pg_authid mo ON m.member = mo.oid " +
			"WHERE r.rolname=$1 AND mo.rolname=$2)"

		q.Parameters = []interface{}{
			gp.MemberOf,
			gp.Role,
		}
		return nil
	case roleDatabase:
		sp := make([]string, len(gp.Privileges))
		copy(sp, gp.Privileges)
		sort.Strings(sp)
		// Join grantee. Filter by database name and grantee name.
		// Finally, perform a permission comparison against expected
		// permissions. Input permissions MUST be sorted.
		q.String = "SELECT EXISTS(SELECT 1 FROM pg_database db, " +
			"aclexplode(datacl) as acl " +
			"INNER JOIN pg_authid s ON acl.grantee = s.oid " +
			"WHERE db.datname=$1 " +
			"AND s.rolname=$2 " +
			"GROUP BY db.datname, s.rolname " +
			"HAVING array_agg(acl.privilege_type ORDER BY privilege_type ASC) = $3::text[])"

		q.Parameters = []interface{}{
			gp.Database,
			gp.Role,
			pq.Array(gp.Privileges),
		}
		return nil
	case roleTable:
		// Select grants where grantee matches and table is any of the requested tables
		// Filter by privilege to make sure each entry matches the required privs
		// Make sure that each required table appears with matching privs.
		q.String = "SELECT EXISTS( " +
			"SELECT 1 FROM ( " +
			"SELECT array_agg(gbp.table_name ORDER BY gbp.table_name)::text[] AS tables " +
			"FROM (" +
			"SELECT table_name, " +
			"array_agg(privilege_type ORDER BY privilege_type)::text[] as privs, " +
			"FROM information_schema.role_table_grants " +
			"WHERE grantee = $1 AND table_name = ANY($2::text[]) " +
			"GROUP BY grantee, table_name " +
			") AS gbp " +
			"WHERE gbp.privs = ($3::text[]) " +
			") AS exists " +
			"WHERE exists.tables::text[] = $4::text[]" +
			")"
		q.Parameters = []interface{}{
			gp.Role,
			pq.Array(gp.Tables),
			pq.Array(gp.Privileges),
			pq.Array(gp.Tables),
		}
	}
	return errors.New(errUnknownGrant)
}

func createGrantQueries(gp v1alpha1.GrantParameters, ql *[]xsql.Query) error { // nolint: gocyclo
	gt, err := identifyGrantType(gp)
	if err != nil {
		return err
	}

	switch gt {
	case roleMember:
		if gp.MemberOf == nil || gp.Role == nil {
			return errors.Errorf(errInvalidParams, roleMember)
		}
		*ql = append(*ql, xsql.Query{String: fmt.Sprintf("GRANT %s TO %s",
			pq.QuoteIdentifier(*gp.MemberOf),
			pq.QuoteIdentifier(*gp.Role),
		)})
		return nil
	case roleDatabase:
		if gp.Database == nil || gp.Role == nil || len(gp.Privileges) < 1 {
			return errors.Errorf(errInvalidParams, roleDatabase)
		}

		*ql = append(*ql,
			// REVOKE ANY EXISTING PERMISSIONS
			xsql.Query{String: fmt.Sprintf("REVOKE ALL ON DATABASE %s FROM %s",
				pq.QuoteIdentifier(*gp.Database),
				pq.QuoteIdentifier(*gp.Role),
			)},

			// GRANT REQUESTED PERMISSIONS
			xsql.Query{String: fmt.Sprintf("GRANT %s ON DATABASE %s TO %s",
				strings.Join(gp.Privileges, ","), // TODO: Make sure these are sanitised
				pq.QuoteIdentifier(*gp.Database),
				pq.QuoteIdentifier(*gp.Role),
			)},
		)
		return nil
	case roleTable:
		if gp.Database == nil || gp.Role == nil || len(gp.Privileges) < 1 || len(gp.Tables) < 1 {
			return errors.Errorf(errInvalidParams, roleTable)
		}

		*ql = append(*ql,
			// REVOKE ANY EXISTING PERMISSIONS ON ALL TABLES
			xsql.Query{String: fmt.Sprintf("REVOKE ALL ON TABLE %s FROM %s",
				QuoteIdentifierArray(gp.Tables),
				pq.QuoteIdentifier(*gp.Role),
			)},

			xsql.Query{String: fmt.Sprintf("GRANT %s ON TABLE %s TO %s",
				strings.Join(gp.Privileges, ","), // TODO: Make sure these are sanitised
				QuoteIdentifierArray(gp.Tables),
				pq.QuoteIdentifier(*gp.Role),
			)},
		)
		return nil
	}
	return errors.New(errUnknownGrant)
}

func deleteGrantQuery(gp v1alpha1.GrantParameters, q *xsql.Query) error {
	gt, err := identifyGrantType(gp)
	if err != nil {
		return err
	}

	switch gt {
	case roleMember:
		q.String = fmt.Sprintf("REVOKE %s FROM %s",
			pq.QuoteIdentifier(*gp.MemberOf),
			pq.QuoteIdentifier(*gp.Role),
		)
		return nil
	case roleDatabase:
		q.String = fmt.Sprintf("REVOKE %s ON DATABASE %s FROM %s",
			strings.Join(gp.Privileges, ","),
			pq.QuoteIdentifier(*gp.Database),
			pq.QuoteIdentifier(*gp.Role),
		)
		return nil
	case roleTable:
		q.String = fmt.Sprintf("REVOKE %s ON TABLE %s FROM %s",
			strings.Join(gp.Privileges, ","),
			QuoteIdentifierArray(gp.Tables),
			pq.QuoteIdentifier(*gp.Role),
		)
		return nil
	}
	return errors.New(errUnknownGrant)
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.Grant)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotGrant)
	}

	if cr.Spec.ForProvider.Role == nil {
		return managed.ExternalObservation{}, errors.New(errNoRole)
	}

	gp := cr.Spec.ForProvider
	var query xsql.Query
	err := selectGrantQuery(gp, &query)

	if err != nil {
		return managed.ExternalObservation{}, err
	}

	exists := false

	err = c.db.Scan(ctx, query, &exists)
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errSelectGrant)
	}

	if !exists {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}

	// Grants have no way of being 'not up to date' - if they exist, they are up to date
	cr.SetConditions(xpv1.Available())

	return managed.ExternalObservation{
		ResourceExists:          true,
		ResourceUpToDate:        true,
		ResourceLateInitialized: false,
	}, nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.Grant)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotGrant)
	}

	var queries []xsql.Query

	cr.SetConditions(xpv1.Creating())

	if err := createGrantQueries(cr.Spec.ForProvider, &queries); err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, errCreateGrant)
	}

	if err := c.db.ExecTx(ctx, queries); err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, errCreateGrant)
	}

	return managed.ExternalCreation{}, nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	// Update is a no-op, as permissions are fully revoked and then granted in the Create function,
	// inside a transaction.
	return managed.ExternalUpdate{}, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) error {
	cr, ok := mg.(*v1alpha1.Grant)
	if !ok {
		return errors.New(errNotGrant)
	}
	var query xsql.Query

	cr.SetConditions(xpv1.Deleting())

	err := deleteGrantQuery(cr.Spec.ForProvider, &query)
	if err != nil {
		return errors.Wrap(err, errRevokeGrant)
	}

	return errors.Wrap(c.db.Exec(ctx, query), errRevokeGrant)
}

// QuoteIdentifierArray for PostgreSQL queries
func QuoteIdentifierArray(ia []string) string {
	if len(ia) < 1 {
		return ""
	}

	o := make([]string, len(ia))

	for i, s := range ia {
		o[i] = pq.QuoteIdentifier(s)
	}

	return strings.Join(o, ",")
}
