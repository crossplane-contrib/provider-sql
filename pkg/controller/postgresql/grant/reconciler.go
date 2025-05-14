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
	"github.com/crossplane/crossplane-runtime/pkg/feature"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/reference"
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

	errNotGrant    = "managed resource is not a Grant custom resource"
	errSelectGrant = "cannot select grant"
	errCreateGrant = "cannot create grant"
	errRevokeGrant = "cannot revoke grant"
	errNoRole      = "role not passed or could not be resolved"

	errUnknownGrant     = "cannot identify grant type based on passed params"
	errUnsupportedGrant = "grant type not supported: %s"
	errInvalidParams    = "invalid parameters for grant type %s"

	maxConcurrency = 5
)

// Setup adds a controller that reconciles Grant managed resources.
func Setup(mgr ctrl.Manager, o xpcontroller.Options) error {
	name := managed.ControllerName(v1alpha1.GrantGroupKind)

	t := resource.NewProviderConfigUsageTracker(mgr.GetClient(), &v1alpha1.ProviderConfigUsage{})
	reconcilerOptions := []managed.ReconcilerOption{
		managed.WithExternalConnecter(&connector{kube: mgr.GetClient(), usage: t, newDB: postgresql.New}),
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
	kube  client.Client
	usage resource.Tracker
	newDB func(creds map[string][]byte, database string, sslmode string) xsql.DB
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
	db := reference.FromPtrValue(cr.Spec.ForProvider.Database)
	if db == "" {
		db = pc.Spec.DefaultDatabase
	}
	return &external{
		db:   c.newDB(s.Data, db, clients.ToString(pc.Spec.SSLMode)),
		kube: c.kube,
	}, nil
}

type external struct {
	db   xsql.DB
	kube client.Client
}

func yesOrNo(b bool) string {
	if b {
		return "YES"
	} else {
		return "NO"
	}
}

func selectForeignServerGrantQuery(gp v1alpha1.GrantParameters, q *xsql.Query) error {
	gro := gp.WithOption != nil && *gp.WithOption == v1alpha1.GrantOptionGrant

	ep := gp.ExpandPrivileges()
	sp := ep.ToStringSlice()

	// Join grantee. Filter by schema name, table name and grantee name.
	// Finally, perform a permission comparison against expected
	// permissions.
	q.String = "SELECT COUNT(*) >= $1 AS ct " +
		"FROM (SELECT 1 " +
		"FROM information_schema.role_usage_grants " +
		// Filter by column, table, schema, role and grantable setting
		"WHERE grantee=$2 " +
		"AND object_type = 'FOREIGN SERVER' " +
		"AND object_name = ANY($3) " +
		"AND is_grantable=$4 " +
		"GROUP BY object_name " +
		// Check privileges match. Convoluted right-hand-side is necessary to
		// ensure identical sort order of the input permissions.
		"HAVING array_agg(TEXT(privilege_type) ORDER BY privilege_type ASC) " +
		"= (SELECT array(SELECT unnest($5::text[]) as perms ORDER BY perms ASC))" +
		") sub"
	q.Parameters = []interface{}{
		len(gp.ForeignServers),
		gp.Role,
		pq.Array(gp.ForeignServers),
		yesOrNo(gro),
		pq.Array(sp),
	}

	return nil
}

func selectForeignDataWrapperGrantQuery(gp v1alpha1.GrantParameters, q *xsql.Query) error {
	gro := gp.WithOption != nil && *gp.WithOption == v1alpha1.GrantOptionGrant

	ep := gp.ExpandPrivileges()
	sp := ep.ToStringSlice()

	// Join grantee. Filter by schema name, table name and grantee name.
	// Finally, perform a permission comparison against expected
	// permissions.
	q.String = "SELECT COUNT(*) >= $1 AS ct " +
		"FROM (SELECT 1 " +
		"FROM information_schema.role_usage_grants " +
		// Filter by column, table, schema, role and grantable setting
		"WHERE grantee=$2 " +
		"AND object_type = 'FOREIGN DATA WRAPPER' " +
		"AND object_name = ANY($3) " +
		"AND is_grantable=$4 " +
		"GROUP BY object_name " +
		// Check privileges match. Convoluted right-hand-side is necessary to
		// ensure identical sort order of the input permissions.
		"HAVING array_agg(TEXT(privilege_type) ORDER BY privilege_type ASC) " +
		"= (SELECT array(SELECT unnest($5::text[]) as perms ORDER BY perms ASC))" +
		") sub"
	q.Parameters = []interface{}{
		len(gp.ForeignDataWrappers),
		gp.Role,
		pq.Array(gp.ForeignDataWrappers),
		yesOrNo(gro),
		pq.Array(sp),
	}

	return nil
}

func selectColumnGrantQuery(gp v1alpha1.GrantParameters, q *xsql.Query) error {
	gro := gp.WithOption != nil && *gp.WithOption == v1alpha1.GrantOptionGrant

	ep := gp.ExpandPrivileges()
	sp := ep.ToStringSlice()

	// Join grantee. Filter by schema name, table name and grantee name.
	// Finally, perform a permission comparison against expected
	// permissions.
	q.String = "SELECT COUNT(*) >= $1 AS ct " +
		"FROM (SELECT 1 " +
		"FROM information_schema.role_column_grants " +
		// Filter by column, table, schema, role and grantable setting
		"WHERE table_schema=$2 " +
		"AND table_name = ANY($3) " +
		"AND column_name = ANY($4) " +
		"AND grantee=$5 " +
		"AND is_grantable=$6 " +
		"GROUP BY table_name, column_name " +
		// Check privileges match. Convoluted right-hand-side is necessary to
		// ensure identical sort order of the input permissions.
		"HAVING array_agg(TEXT(privilege_type) ORDER BY privilege_type ASC) " +
		"= (SELECT array(SELECT unnest($7::text[]) as perms ORDER BY perms ASC))" +
		") sub"
	q.Parameters = []interface{}{
		len(gp.Tables) * len(gp.Columns),
		gp.Schema,
		pq.Array(gp.Tables),
		pq.Array(gp.Columns),
		gp.Role,
		yesOrNo(gro),
		pq.Array(sp),
	}

	return nil
}

func selectRoutineGrantQuery(gp v1alpha1.GrantParameters, q *xsql.Query) error {
	gro := gp.WithOption != nil && *gp.WithOption == v1alpha1.GrantOptionGrant

	ep := gp.ExpandPrivileges()
	sp := ep.ToStringSlice()

	routinesSignatures := make([]string, len(gp.Routines))
	for i, routine := range gp.Routines {
		routinesSignatures[i] = signature(routine)
	}

	// Join grantee. Filter by routine name and signature, schema name and grantee name.
	// Finally, perform a permission comparison against expected
	// permissions.
	q.String = "SELECT COUNT(*) = $1 AS ct " +
		"FROM (SELECT " +
		// format routine args
		"p.proname || '(' || coalesce(array_to_string(array_agg(pg_catalog.format_type(t, NULL) ORDER BY args.ord), ',')) || ')' " +
		"AS signature " +
		"FROM pg_proc p " +
		"LEFT JOIN unnest(p.proargtypes) WITH ORDINALITY AS args(t, ord) on true " +
		"INNER JOIN pg_namespace n ON p.pronamespace = n.oid, " +
		"aclexplode(p.proacl) as acl " +
		"INNER JOIN pg_roles s ON acl.grantee = s.oid " +
		// Filter by sequence, schema, role and grantable setting
		"WHERE n.nspname=$2 " +
		"AND s.rolname=$3 " +
		"AND acl.is_grantable=$4 " +
		"GROUP BY n.nspname, s.rolname, acl.is_grantable, p.oid " +
		// Check privileges match. Convoluted right-hand-side is necessary to
		// ensure identical sort order of the input permissions.
		"HAVING array_agg(acl.privilege_type ORDER BY privilege_type ASC) " +
		"= (SELECT array(SELECT unnest($5::text[]) as perms ORDER BY perms ASC))" +
		") sub " +
		"WHERE sub.signature = ANY($6)"
	q.Parameters = []interface{}{
		len(gp.Routines),
		gp.Schema,
		gp.Role,
		gro,
		pq.Array(sp),
		pq.Array(routinesSignatures),
	}

	return nil
}

func selectSequenceGrantQuery(gp v1alpha1.GrantParameters, q *xsql.Query) error {
	gro := gp.WithOption != nil && *gp.WithOption == v1alpha1.GrantOptionGrant

	ep := gp.ExpandPrivileges()
	sp := ep.ToStringSlice()
	// Join grantee. Filter by sequence name, schema name and grantee name.
	// Finally, perform a permission comparison against expected
	// permissions.
	q.String = "SELECT COUNT(*) = $1 AS ct " +
		"FROM (SELECT 1 FROM pg_class c " +
		"INNER JOIN pg_namespace n ON c.relnamespace = n.oid, " +
		"aclexplode(c.relacl) as acl " +
		"INNER JOIN pg_roles s ON acl.grantee = s.oid " +
		"WHERE c.relkind = 'S' " +
		// Filter by sequence, schema, role and grantable setting
		"AND n.nspname=$2 " +
		"AND s.rolname=$3 " +
		"AND c.relname = ANY($4) " +
		"AND acl.is_grantable=$5 " +
		"GROUP BY n.nspname, s.rolname, acl.is_grantable " +
		// Check privileges match. Convoluted right-hand-side is necessary to
		// ensure identical sort order of the input permissions.
		"HAVING array_agg(acl.privilege_type ORDER BY privilege_type ASC) " +
		"= (SELECT array(SELECT unnest($6::text[]) as perms ORDER BY perms ASC))" +
		") sub"
	q.Parameters = []interface{}{
		len(gp.Sequences),
		gp.Schema,
		gp.Role,
		pq.Array(gp.Sequences),
		gro,
		pq.Array(sp),
	}

	return nil
}

func selectTableGrantQuery(gp v1alpha1.GrantParameters, q *xsql.Query) error {
	gro := gp.WithOption != nil && *gp.WithOption == v1alpha1.GrantOptionGrant

	ep := gp.ExpandPrivileges()
	sp := ep.ToStringSlice()

	// Join grantee. Filter by schema name, table name and grantee name.
	// Finally, perform a permission comparison against expected
	// permissions.
	q.String = "SELECT COUNT(*) = $1 AS ct " +
		"FROM (SELECT 1 " +
		"FROM information_schema.role_table_grants " +
		// Filter by table, schema, role and grantable setting
		"WHERE table_schema=$2 " +
		"AND table_name = ANY($3) " +
		"AND grantee=$4 " +
		"AND is_grantable=$5 " +
		"GROUP BY table_name " +
		// Check privileges match. Convoluted right-hand-side is necessary to
		// ensure identical sort order of the input permissions.
		"HAVING array_agg(TEXT(privilege_type) ORDER BY privilege_type ASC) " +
		"= (SELECT array(SELECT unnest($6::text[]) as perms ORDER BY perms ASC))" +
		") sub"
	q.Parameters = []interface{}{
		len(gp.Tables),
		gp.Schema,
		pq.Array(gp.Tables),
		gp.Role,
		yesOrNo(gro),
		pq.Array(sp),
	}

	return nil
}

func selectSchemaGrantQuery(gp v1alpha1.GrantParameters, q *xsql.Query) error {
	gro := gp.WithOption != nil && *gp.WithOption == v1alpha1.GrantOptionGrant

	ep := gp.ExpandPrivileges()
	sp := ep.ToStringSlice()
	// Join grantee. Filter by schema name and grantee name.
	// Finally, perform a permission comparison against expected
	// permissions.
	q.String = "SELECT EXISTS(SELECT 1 " +
		"FROM pg_namespace n, " +
		"aclexplode(nspacl) as acl " +
		"INNER JOIN pg_roles s ON acl.grantee = s.oid " +
		// Filter by schema, role and grantable setting
		"WHERE n.nspname=$1 " +
		"AND s.rolname=$2 " +
		"AND acl.is_grantable=$3 " +
		"GROUP BY n.nspname, s.rolname, acl.is_grantable " +
		// Check privileges match. Convoluted right-hand-side is necessary to
		// ensure identical sort order of the input permissions.
		"HAVING array_agg(acl.privilege_type ORDER BY privilege_type ASC) " +
		"= (SELECT array(SELECT unnest($4::text[]) as perms ORDER BY perms ASC)))"
	q.Parameters = []interface{}{
		gp.Schema,
		gp.Role,
		gro,
		pq.Array(sp),
	}
	return nil
}

func selectDatabaseGrantQuery(gp v1alpha1.GrantParameters, q *xsql.Query) error {
	gro := gp.WithOption != nil && *gp.WithOption == v1alpha1.GrantOptionGrant

	ep := gp.ExpandPrivileges()
	sp := ep.ToStringSlice()
	// Join grantee. Filter by database name and grantee name.
	// Finally, perform a permission comparison against expected
	// permissions.
	q.String = "SELECT EXISTS(SELECT 1 " +
		"FROM pg_database db, " +
		"aclexplode(datacl) as acl " +
		"INNER JOIN pg_roles s ON acl.grantee = s.oid " +
		// Filter by database, role and grantable setting
		"WHERE db.datname=$1 " +
		"AND s.rolname=$2 " +
		"AND acl.is_grantable=$3 " +
		"GROUP BY db.datname, s.rolname, acl.is_grantable " +
		// Check privileges match. Convoluted right-hand-side is necessary to
		// ensure identical sort order of the input permissions.
		"HAVING array_agg(acl.privilege_type ORDER BY privilege_type ASC) " +
		"= (SELECT array(SELECT unnest($4::text[]) as perms ORDER BY perms ASC)))"

	q.Parameters = []interface{}{
		gp.Database,
		gp.Role,
		gro,
		pq.Array(sp),
	}
	return nil
}

func selectMemberGrantQuery(gp v1alpha1.GrantParameters, q *xsql.Query) error {
	ao := gp.WithOption != nil && *gp.WithOption == v1alpha1.GrantOptionAdmin

	// Always returns a row with a true or false value
	// A simpler query would use ::regrol to cast the
	// roleid and member oids to their role names, but
	// if this is used with a nonexistent role name it will
	// throw an error rather than return false.
	q.String = "SELECT EXISTS(SELECT 1 FROM pg_auth_members m " +
		"INNER JOIN pg_roles mo ON m.roleid = mo.oid " +
		"INNER JOIN pg_roles r ON m.member = r.oid " +
		"WHERE r.rolname=$1 AND mo.rolname=$2 AND " +
		"m.admin_option = $3)"

	q.Parameters = []interface{}{
		gp.Role,
		gp.MemberOf,
		ao,
	}
	return nil
}

func withOption(option *v1alpha1.GrantOption) string {
	if option != nil {
		return fmt.Sprintf("WITH %s OPTION", string(*option))
	}
	return ""
}

func selectGrantQuery(gp v1alpha1.GrantParameters, q *xsql.Query) error { // nolint: gocyclo
	gt, err := gp.IdentifyGrantType()
	if err != nil {
		return err
	}

	switch gt {
	case v1alpha1.RoleMember:
		return selectMemberGrantQuery(gp, q)
	case v1alpha1.RoleDatabase:
		return selectDatabaseGrantQuery(gp, q)
	case v1alpha1.RoleSchema:
		return selectSchemaGrantQuery(gp, q)
	case v1alpha1.RoleTable:
		return selectTableGrantQuery(gp, q)
	case v1alpha1.RoleSequence:
		return selectSequenceGrantQuery(gp, q)
	case v1alpha1.RoleRoutine:
		return selectRoutineGrantQuery(gp, q)
	case v1alpha1.RoleColumn:
		return selectColumnGrantQuery(gp, q)
	case v1alpha1.RoleForeignDataWrapper:
		return selectForeignDataWrapperGrantQuery(gp, q)
	case v1alpha1.RoleForeignServer:
		return selectForeignServerGrantQuery(gp, q)
	}
	return errors.Errorf(errUnsupportedGrant, gt)
}

func createGrantQueries(gp v1alpha1.GrantParameters, ql *[]xsql.Query) error { // nolint: gocyclo
	gt, err := gp.IdentifyGrantType()
	if err != nil {
		return err
	}

	ro := pq.QuoteIdentifier(*gp.Role)

	switch gt {
	case v1alpha1.RoleMember:
		return createMemberGrantQueries(gp, ql, ro)
	case v1alpha1.RoleDatabase:
		return createDatabaseGrantQueries(gp, ql, ro)
	case v1alpha1.RoleSchema:
		return createSchemaGrantQueries(gp, ql, ro)
	case v1alpha1.RoleTable:
		return createTableGrantQueries(gp, ql, ro)
	case v1alpha1.RoleSequence:
		return createSequenceGrantQueries(gp, ql, ro)
	case v1alpha1.RoleRoutine:
		return createRoutineGrantQueries(gp, ql, ro)
	case v1alpha1.RoleColumn:
		return createColumnGrantQueries(gp, ql, ro)
	case v1alpha1.RoleForeignDataWrapper:
		return createForeignDataWrapperGrantQueries(gp, ql, ro)
	case v1alpha1.RoleForeignServer:
		return createForeignServerGrantQueries(gp, ql, ro)
	}
	return errors.Errorf(errUnsupportedGrant, gt)
}

func createMemberGrantQueries(gp v1alpha1.GrantParameters, ql *[]xsql.Query, ro string) error {
	if gp.MemberOf == nil || gp.Role == nil {
		return errors.Errorf(errInvalidParams, v1alpha1.RoleMember)
	}

	mo := pq.QuoteIdentifier(*gp.MemberOf)

	*ql = append(*ql,
		xsql.Query{String: fmt.Sprintf("REVOKE %s FROM %s", mo, ro)},
		xsql.Query{String: fmt.Sprintf("GRANT %s TO %s %s", mo, ro,
			withOption(gp.WithOption),
		)},
	)
	return nil
}

func createDatabaseGrantQueries(gp v1alpha1.GrantParameters, ql *[]xsql.Query, ro string) error {
	if gp.Database == nil || gp.Role == nil || len(gp.Privileges) < 1 {
		return errors.Errorf(errInvalidParams, v1alpha1.RoleDatabase)
	}

	db := pq.QuoteIdentifier(*gp.Database)
	sp := strings.Join(gp.Privileges.ToStringSlice(), ",")

	*ql = append(*ql,
		// REVOKE ANY MATCHING EXISTING PERMISSIONS
		xsql.Query{String: fmt.Sprintf("REVOKE %s ON DATABASE %s FROM %s",
			sp,
			db,
			ro,
		)},

		// GRANT REQUESTED PERMISSIONS
		xsql.Query{String: fmt.Sprintf("GRANT %s ON DATABASE %s TO %s %s",
			sp,
			db,
			ro,
			withOption(gp.WithOption),
		)},
	)
	if gp.RevokePublicOnDb != nil && *gp.RevokePublicOnDb {
		*ql = append(*ql,
			// REVOKE FROM PUBLIC
			xsql.Query{String: fmt.Sprintf("REVOKE ALL ON DATABASE %s FROM PUBLIC",
				db,
			)},
		)
	}
	return nil
}

func createSchemaGrantQueries(gp v1alpha1.GrantParameters, ql *[]xsql.Query, ro string) error {
	if gp.Database == nil || gp.Schema == nil || gp.Role == nil || len(gp.Privileges) < 1 {
		return errors.Errorf(errInvalidParams, v1alpha1.RoleSchema)
	}

	sh := pq.QuoteIdentifier(*gp.Schema)
	sp := strings.Join(gp.Privileges.ToStringSlice(), ",")

	*ql = append(*ql,
		// REVOKE ANY MATCHING EXISTING PERMISSIONS
		xsql.Query{String: fmt.Sprintf("REVOKE %s ON SCHEMA %s FROM %s",
			sp,
			sh,
			ro,
		)},

		// GRANT REQUESTED PERMISSIONS
		xsql.Query{String: fmt.Sprintf("GRANT %s ON SCHEMA %s TO %s %s",
			sp,
			sh,
			ro,
			withOption(gp.WithOption),
		)},
	)
	return nil
}

func createTableGrantQueries(gp v1alpha1.GrantParameters, ql *[]xsql.Query, ro string) error {
	if gp.Database == nil || gp.Schema == nil || len(gp.Tables) < 1 || gp.Role == nil || len(gp.Privileges) < 1 {
		return errors.Errorf(errInvalidParams, v1alpha1.RoleTable)
	}

	tb := strings.Join(prefixAndQuote(*gp.Schema, gp.Tables), ",")
	sp := strings.Join(gp.Privileges.ToStringSlice(), ",")

	*ql = append(*ql,
		// REVOKE ANY MATCHING EXISTING PERMISSIONS
		xsql.Query{String: fmt.Sprintf("REVOKE %s ON TABLE %s FROM %s",
			sp,
			tb,
			ro,
		)},

		// GRANT REQUESTED PERMISSIONS
		xsql.Query{String: fmt.Sprintf("GRANT %s ON TABLE %s TO %s %s",
			sp,
			tb,
			ro,
			withOption(gp.WithOption),
		)},
	)
	return nil
}

func createColumnGrantQueries(gp v1alpha1.GrantParameters, ql *[]xsql.Query, ro string) error {
	if gp.Database == nil || gp.Schema == nil || len(gp.Tables) < 1 || len(gp.Columns) < 1 || gp.Role == nil || len(gp.Privileges) < 1 {
		return errors.Errorf(errInvalidParams, v1alpha1.RoleColumn)
	}

	co := strings.Join(gp.Columns, ",")
	tb := strings.Join(prefixAndQuote(*gp.Schema, gp.Tables), ",")
	sp := strings.Join(gp.Privileges.ToStringSlice(), ",")

	*ql = append(*ql,
		// REVOKE ANY MATCHING EXISTING PERMISSIONS
		xsql.Query{String: fmt.Sprintf("REVOKE %s (%s) ON TABLE %s FROM %s",
			sp,
			co,
			tb,
			ro,
		)},

		// GRANT REQUESTED PERMISSIONS
		xsql.Query{String: fmt.Sprintf("GRANT %s (%s) ON TABLE %s TO %s %s",
			sp,
			co,
			tb,
			ro,
			withOption(gp.WithOption),
		)},
	)
	return nil
}

func createSequenceGrantQueries(gp v1alpha1.GrantParameters, ql *[]xsql.Query, ro string) error {
	if gp.Database == nil || gp.Schema == nil || len(gp.Sequences) < 1 || gp.Role == nil || len(gp.Privileges) < 1 {
		return errors.Errorf(errInvalidParams, v1alpha1.RoleSequence)
	}

	sq := strings.Join(prefixAndQuote(*gp.Schema, gp.Sequences), ",")
	sp := strings.Join(gp.Privileges.ToStringSlice(), ",")

	*ql = append(*ql,
		// REVOKE ANY MATCHING EXISTING PERMISSIONS
		xsql.Query{String: fmt.Sprintf("REVOKE %s ON SEQUENCE %s FROM %s",
			sp,
			sq,
			ro,
		)},

		// GRANT REQUESTED PERMISSIONS
		xsql.Query{String: fmt.Sprintf("GRANT %s ON SEQUENCE %s TO %s %s",
			sp,
			sq,
			ro,
			withOption(gp.WithOption),
		)},
	)
	return nil
}

func createRoutineGrantQueries(gp v1alpha1.GrantParameters, ql *[]xsql.Query, ro string) error {
	if gp.Database == nil || gp.Schema == nil || len(gp.Routines) < 1 || gp.Role == nil || len(gp.Privileges) < 1 {
		return errors.Errorf(errInvalidParams, v1alpha1.RoleRoutine)
	}

	rt := strings.Join(quotedSignatures(*gp.Schema, gp.Routines), ",")
	sp := strings.Join(gp.Privileges.ToStringSlice(), ",")

	*ql = append(*ql,
		// REVOKE ANY MATCHING EXISTING PERMISSIONS
		xsql.Query{String: fmt.Sprintf("REVOKE %s ON ROUTINE %s FROM %s",
			sp,
			rt,
			ro,
		)},

		// GRANT REQUESTED PERMISSIONS
		xsql.Query{String: fmt.Sprintf("GRANT %s ON ROUTINE %s TO %s %s",
			sp,
			rt,
			ro,
			withOption(gp.WithOption),
		)},
	)
	return nil
}

func createForeignDataWrapperGrantQueries(gp v1alpha1.GrantParameters, ql *[]xsql.Query, ro string) error {
	if gp.Database == nil || len(gp.ForeignDataWrappers) < 1 || gp.Role == nil || len(gp.Privileges) < 1 {
		return errors.Errorf(errInvalidParams, v1alpha1.RoleForeignDataWrapper)
	}

	sp := strings.Join(gp.Privileges.ToStringSlice(), ",")

	*ql = append(*ql,
		// REVOKE ANY MATCHING EXISTING PERMISSIONS
		xsql.Query{String: fmt.Sprintf("REVOKE %s ON FOREIGN DATA WRAPPER %s FROM %s",
			sp,
			strings.Join(gp.ForeignDataWrappers, ","),
			ro,
		)},

		// GRANT REQUESTED PERMISSIONS
		xsql.Query{String: fmt.Sprintf("GRANT %s ON FOREIGN DATA WRAPPER %s TO %s %s",
			sp,
			strings.Join(gp.ForeignDataWrappers, ","),
			ro,
			withOption(gp.WithOption),
		)},
	)
	return nil
}

func createForeignServerGrantQueries(gp v1alpha1.GrantParameters, ql *[]xsql.Query, ro string) error {
	if gp.Database == nil || len(gp.ForeignServers) < 1 || gp.Role == nil || len(gp.Privileges) < 1 {
		return errors.Errorf(errInvalidParams, v1alpha1.RoleForeignServer)
	}

	sp := strings.Join(gp.Privileges.ToStringSlice(), ",")

	*ql = append(*ql,
		// REVOKE ANY MATCHING EXISTING PERMISSIONS
		xsql.Query{String: fmt.Sprintf("REVOKE %s ON FOREIGN SERVER %s FROM %s",
			sp,
			strings.Join(gp.ForeignServers, ","),
			ro,
		)},

		// GRANT REQUESTED PERMISSIONS
		xsql.Query{String: fmt.Sprintf("GRANT %s ON FOREIGN SERVER %s TO %s %s",
			sp,
			strings.Join(gp.ForeignServers, ","),
			ro,
			withOption(gp.WithOption),
		)},
	)
	return nil
}

func deleteGrantQuery(gp v1alpha1.GrantParameters, q *xsql.Query) error { // nolint: gocyclo
	gt, err := gp.IdentifyGrantType()
	if err != nil {
		return err
	}

	ro := pq.QuoteIdentifier(*gp.Role)

	switch gt {
	case v1alpha1.RoleMember:
		q.String = fmt.Sprintf("REVOKE %s FROM %s",
			pq.QuoteIdentifier(*gp.MemberOf),
			ro,
		)
		return nil
	case v1alpha1.RoleDatabase:
		q.String = fmt.Sprintf("REVOKE %s ON DATABASE %s FROM %s",
			strings.Join(gp.Privileges.ToStringSlice(), ","),
			pq.QuoteIdentifier(*gp.Database),
			ro,
		)
		return nil
	case v1alpha1.RoleSchema:
		q.String = fmt.Sprintf("REVOKE %s ON SCHEMA %s FROM %s",
			strings.Join(gp.Privileges.ToStringSlice(), ","),
			pq.QuoteIdentifier(*gp.Schema),
			ro,
		)
		return nil
	case v1alpha1.RoleTable:
		q.String = fmt.Sprintf("REVOKE %s ON TABLE %s FROM %s",
			strings.Join(gp.Privileges.ToStringSlice(), ","),
			strings.Join(prefixAndQuote(*gp.Schema, gp.Tables), ","),
			ro,
		)
		return nil
	case v1alpha1.RoleSequence:
		q.String = fmt.Sprintf("REVOKE %s ON SEQUENCE %s FROM %s",
			strings.Join(gp.Privileges.ToStringSlice(), ","),
			strings.Join(prefixAndQuote(*gp.Schema, gp.Sequences), ","),
			ro,
		)
		return nil
	case v1alpha1.RoleRoutine:
		q.String = fmt.Sprintf("REVOKE %s ON ROUTINE %s FROM %s",
			strings.Join(gp.Privileges.ToStringSlice(), ","),
			strings.Join(quotedSignatures(*gp.Schema, gp.Routines), ","),
			ro,
		)
		return nil
	case v1alpha1.RoleColumn:
		q.String = fmt.Sprintf("REVOKE %s (%s) ON TABLE %s FROM %s",
			strings.Join(gp.Privileges.ToStringSlice(), ","),
			strings.Join(gp.Columns, ","),
			strings.Join(prefixAndQuote(*gp.Schema, gp.Tables), ","),
			ro,
		)
		return nil
	case v1alpha1.RoleForeignDataWrapper:
		q.String = fmt.Sprintf("REVOKE %s ON FOREIGN DATA WRAPPER %s FROM %s",
			strings.Join(gp.Privileges.ToStringSlice(), ","),
			strings.Join(gp.ForeignDataWrappers, ","),
			ro,
		)
		return nil
	case v1alpha1.RoleForeignServer:
		q.String = fmt.Sprintf("REVOKE %s ON FOREIGN SERVER %s FROM %s",
			strings.Join(gp.Privileges.ToStringSlice(), ","),
			strings.Join(gp.ForeignServers, ","),
			ro,
		)
		return nil
	}
	return errors.Errorf(errUnsupportedGrant, gt)
}

// signature returns routines with the same format as the select query
func signature(r v1alpha1.Routine) string {
	args := make([]string, len(r.Arguments))
	for i, v := range r.Arguments {
		args[i] = strings.ToLower(v)
	}
	return r.Name + "(" + strings.Join(args, ",") + ")"
}

// quotedSignatures returns routines in a quoted grantable format, prefixed with the schema
func quotedSignatures(sc string, rs []v1alpha1.Routine) []string {
	qsc := pq.QuoteIdentifier(sc)
	sigs := make([]string, len(rs))

	for i, r := range rs {
		sigs[i] = qsc + "." + pq.QuoteIdentifier(r.Name) + "(" + strings.Join(r.Arguments, ",") + ")"
	}
	return sigs
}

// prefixAndQuote returns objects in a quoted grantable format, prefixed with the schema
func prefixAndQuote(sc string, obj []string) []string {
	qsc := pq.QuoteIdentifier(sc)
	ret := make([]string, len(obj))
	for i, v := range obj {
		ret[i] = qsc + "." + pq.QuoteIdentifier(v)
	}
	return ret
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

	if err := c.db.Scan(ctx, query, &exists); err != nil {
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
	return managed.ExternalCreation{}, errors.Wrap(c.db.ExecTx(ctx, queries), errCreateGrant)
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

	if err := deleteGrantQuery(cr.Spec.ForProvider, &query); err != nil {
		return errors.Wrap(err, errRevokeGrant)
	}

	return errors.Wrap(c.db.Exec(ctx, query), errRevokeGrant)
}
