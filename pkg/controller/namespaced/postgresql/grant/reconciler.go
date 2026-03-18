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

	"github.com/crossplane/crossplane-runtime/v2/pkg/statemetrics"
	"github.com/lib/pq"
	"github.com/pkg/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	xpcontroller "github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/feature"
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

	errSelectGrant  = "cannot select grant"
	errCreateGrant  = "cannot create grant"
	errRevokeGrant  = "cannot revoke grant"
	errNoRole       = "role not passed or could not be resolved"
	errUnknownGrant = "cannot identify grant type based on passed params"

	errUnsupportedGrant = "grant type not supported: %s"
	errInvalidParams    = "invalid parameters for grant type %s"

	errMemberOfWithDatabaseOrPrivileges = "cannot set privileges or database in the same grant as memberOf"

	maxConcurrency = 5
)

// Setup adds a controller that reconciles Database managed resources.
func Setup(mgr ctrl.Manager, o xpcontroller.Options) error {
	name := managed.ControllerName(namespacedv1alpha1.GrantGroupKind)

	t := resource.NewProviderConfigUsageTracker(mgr.GetClient(), &namespacedv1alpha1.ProviderConfigUsage{})

	reconcilerOptions := []managed.ReconcilerOption{
		managed.WithTypedExternalConnector(&connector{kube: mgr.GetClient(), track: t.Track, newDB: postgresql.New}),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithPollInterval(o.PollInterval),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))),
	}
	if o.Features.Enabled(feature.EnableBetaManagementPolicies) {
		reconcilerOptions = append(reconcilerOptions, managed.WithManagementPolicies())
	}
	r := managed.NewReconciler(mgr,
		resource.ManagedKind(namespacedv1alpha1.GrantGroupVersionKind),
		reconcilerOptions...,
	)
	if err := mgr.Add(statemetrics.NewMRStateRecorder(
		mgr.GetClient(), o.Logger, o.MetricOptions.MRStateMetrics,
		&namespacedv1alpha1.GrantList{}, o.MetricOptions.PollStateMetricInterval,
	)); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		For(&namespacedv1alpha1.Grant{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: maxConcurrency,
		}).
		Complete(r)
}

type connector struct {
	kube  client.Client
	track func(ctx context.Context, mg resource.ModernManaged) error
	newDB func(creds map[string][]byte, database string, sslmode string) xsql.DB
}

var _ managed.TypedExternalConnector[*namespacedv1alpha1.Grant] = &connector{}

func (c *connector) Connect(ctx context.Context, mg *namespacedv1alpha1.Grant) (managed.TypedExternalClient[*namespacedv1alpha1.Grant], error) {
	if err := c.track(ctx, mg); err != nil {
		return nil, errors.Wrap(err, errTrackPCUsage)
	}

	// ProviderConfigReference could theoretically be nil, but in practice the
	// DefaultProviderConfig initializer will set it before we get here.
	providerInfo, err := provider.GetProviderConfig(ctx, c.kube, mg)
	if err != nil {
		return nil, err
	}

	// Use the grant's target database when specified, falling back to the
	// ProviderConfig's default. This matters for object-level grants (table,
	// schema, sequence, etc.) which must run against the correct database.
	db := providerInfo.DefaultDatabase
	if mg.Spec.ForProvider.Database != nil && *mg.Spec.ForProvider.Database != "" {
		db = *mg.Spec.ForProvider.Database
	}

	return &external{
		db:   c.newDB(providerInfo.SecretData, db, clients.ToString(providerInfo.SSLMode)),
		kube: c.kube,
	}, nil
}

type external struct {
	db   xsql.DB
	kube client.Client
}

var _ managed.TypedExternalClient[*namespacedv1alpha1.Grant] = &external{}

// resolveGrantType returns the grant type for the given parameters.
// It checks MemberOfRef/MemberOfSelector first (even if MemberOf is not yet
// resolved) so the reconciler can identify roleMember grants before ref
// resolution completes.
func resolveGrantType(gp namespacedv1alpha1.GrantParameters) (namespacedv1alpha1.GrantType, error) {
	pc := len(gp.Privileges)

	// If memberOf is specified via ref or selector, treat as RoleMember even
	// if the value hasn't been resolved yet.
	if gp.MemberOfRef != nil || gp.MemberOfSelector != nil || gp.MemberOf != nil {
		if gp.Database != nil || pc > 0 {
			return "", errors.New(errMemberOfWithDatabaseOrPrivileges)
		}
		return namespacedv1alpha1.RoleMember, nil
	}

	return gp.IdentifyGrantType()
}

func selectMemberGrantQuery(gp namespacedv1alpha1.GrantParameters, q *xsql.Query) {
	ao := gp.WithOption != nil && *gp.WithOption == namespacedv1alpha1.GrantOptionAdmin

	// Always returns a row with a true or false value.
	// A simpler query would use ::regrole to cast the roleid and member oids
	// to their role names, but that throws an error for nonexistent roles
	// rather than returning false.
	q.String = "SELECT EXISTS(SELECT 1 FROM pg_auth_members m " +
		"INNER JOIN pg_roles mo ON m.roleid = mo.oid " +
		"INNER JOIN pg_roles r ON m.member = r.oid " +
		"WHERE r.rolname=$1 AND mo.rolname=$2 AND " +
		"m.admin_option = $3)"
	q.Parameters = []interface{}{gp.Role, gp.MemberOf, ao}
}

func selectDatabaseGrantQuery(gp namespacedv1alpha1.GrantParameters, q *xsql.Query) {
	gro := gp.WithOption != nil && *gp.WithOption == namespacedv1alpha1.GrantOptionGrant
	ep := gp.ExpandPrivileges()
	sp := ep.ToStringSlice()

	q.String = "SELECT EXISTS(SELECT 1 " +
		"FROM pg_database db, " +
		"aclexplode(db.datacl) as acl " +
		"INNER JOIN pg_roles s ON acl.grantee = s.oid " +
		"WHERE db.datname=$1 " +
		"AND s.rolname=$2 " +
		"AND acl.is_grantable=$3 " +
		"GROUP BY db.datname, s.rolname, acl.is_grantable " +
		"HAVING array_agg(acl.privilege_type ORDER BY privilege_type ASC) " +
		"= (SELECT array(SELECT unnest($4::text[]) as perms ORDER BY perms ASC)))"
	q.Parameters = []interface{}{gp.Database, gp.Role, gro, pq.Array(sp)}
}

func selectSchemaGrantQuery(gp namespacedv1alpha1.GrantParameters, q *xsql.Query) {
	gro := gp.WithOption != nil && *gp.WithOption == namespacedv1alpha1.GrantOptionGrant
	ep := gp.ExpandPrivileges()
	sp := ep.ToStringSlice()

	q.String = "SELECT EXISTS(SELECT 1 " +
		"FROM pg_namespace n, " +
		"aclexplode(n.nspacl) as acl " +
		"INNER JOIN pg_roles s ON acl.grantee = s.oid " +
		"WHERE n.nspname=$1 " +
		"AND s.rolname=$2 " +
		"AND acl.is_grantable=$3 " +
		"GROUP BY n.nspname, s.rolname, acl.is_grantable " +
		"HAVING array_agg(acl.privilege_type ORDER BY privilege_type ASC) " +
		"= (SELECT array(SELECT unnest($4::text[]) as perms ORDER BY perms ASC)))"
	q.Parameters = []interface{}{gp.Schema, gp.Role, gro, pq.Array(sp)}
}

func selectTableGrantQuery(gp namespacedv1alpha1.GrantParameters, q *xsql.Query) {
	gro := gp.WithOption != nil && *gp.WithOption == namespacedv1alpha1.GrantOptionGrant
	ep := gp.ExpandPrivileges()
	sp := ep.ToStringSlice()

	q.String = "SELECT COUNT(*) = $1 AS ct " +
		"FROM (SELECT 1 FROM pg_class c " +
		"INNER JOIN pg_namespace n ON c.relnamespace = n.oid, " +
		"aclexplode(c.relacl) as acl " +
		"INNER JOIN pg_roles s ON acl.grantee = s.oid " +
		"WHERE c.relkind = 'r' " +
		"AND n.nspname=$2 " +
		"AND s.rolname=$3 " +
		"AND c.relname = ANY($4) " +
		"AND acl.is_grantable=$5 " +
		"GROUP BY c.relname, n.nspname, s.rolname, acl.is_grantable " +
		"HAVING array_agg(acl.privilege_type ORDER BY privilege_type ASC) " +
		"= (SELECT array(SELECT unnest($6::text[]) as perms ORDER BY perms ASC))" +
		") sub"
	q.Parameters = []interface{}{len(gp.Tables), gp.Schema, gp.Role, pq.Array(gp.Tables), gro, pq.Array(sp)}
}

func selectColumnGrantQuery(gp namespacedv1alpha1.GrantParameters, q *xsql.Query) {
	gro := gp.WithOption != nil && *gp.WithOption == namespacedv1alpha1.GrantOptionGrant
	ep := gp.ExpandPrivileges()
	sp := ep.ToStringSlice()

	q.String = "SELECT COUNT(*) = $1 AS ct " +
		"FROM (SELECT 1 FROM pg_class c " +
		"INNER JOIN pg_namespace n ON c.relnamespace = n.oid " +
		"INNER JOIN pg_attribute attr on c.oid = attr.attrelid, " +
		"aclexplode(attr.attacl) as acl " +
		"INNER JOIN pg_roles s ON acl.grantee = s.oid " +
		"WHERE c.relkind = 'r' " +
		"AND n.nspname=$2 " +
		"AND s.rolname=$3 " +
		"AND c.relname = ANY($4) " +
		"AND attr.attname = ANY($5) " +
		"AND acl.is_grantable=$6 " +
		"GROUP BY c.relname, n.nspname, s.rolname, attr.attname, acl.is_grantable " +
		"HAVING array_agg(acl.privilege_type ORDER BY privilege_type ASC) " +
		"= (SELECT array(SELECT unnest($7::text[]) as perms ORDER BY perms ASC))" +
		") sub"
	q.Parameters = []interface{}{
		len(gp.Tables) * len(gp.Columns),
		gp.Schema, gp.Role,
		pq.Array(gp.Tables), pq.Array(gp.Columns),
		gro, pq.Array(sp),
	}
}

func selectSequenceGrantQuery(gp namespacedv1alpha1.GrantParameters, q *xsql.Query) {
	gro := gp.WithOption != nil && *gp.WithOption == namespacedv1alpha1.GrantOptionGrant
	ep := gp.ExpandPrivileges()
	sp := ep.ToStringSlice()

	q.String = "SELECT COUNT(*) = $1 AS ct " +
		"FROM (SELECT 1 FROM pg_class c " +
		"INNER JOIN pg_namespace n ON c.relnamespace = n.oid, " +
		"aclexplode(c.relacl) as acl " +
		"INNER JOIN pg_roles s ON acl.grantee = s.oid " +
		"WHERE c.relkind = 'S' " +
		"AND n.nspname=$2 " +
		"AND s.rolname=$3 " +
		"AND c.relname = ANY($4) " +
		"AND acl.is_grantable=$5 " +
		"GROUP BY c.relname, n.nspname, s.rolname, acl.is_grantable " +
		"HAVING array_agg(acl.privilege_type ORDER BY privilege_type ASC) " +
		"= (SELECT array(SELECT unnest($6::text[]) as perms ORDER BY perms ASC))" +
		") sub"
	q.Parameters = []interface{}{len(gp.Sequences), gp.Schema, gp.Role, pq.Array(gp.Sequences), gro, pq.Array(sp)}
}

func selectRoutineGrantQuery(gp namespacedv1alpha1.GrantParameters, q *xsql.Query) {
	gro := gp.WithOption != nil && *gp.WithOption == namespacedv1alpha1.GrantOptionGrant
	ep := gp.ExpandPrivileges()
	sp := ep.ToStringSlice()

	routinesSignatures := make([]string, len(gp.Routines))
	for i, r := range gp.Routines {
		routinesSignatures[i] = routineSignature(r)
	}

	q.String = "SELECT COUNT(*) = $1 AS ct " +
		"FROM (SELECT " +
		"p.proname || '(' || coalesce(array_to_string(array_agg(pg_catalog.format_type(t, NULL) ORDER BY args.ord), ',')) || ')' " +
		"AS signature " +
		"FROM pg_proc p " +
		"LEFT JOIN unnest(p.proargtypes) WITH ORDINALITY AS args(t, ord) on true " +
		"INNER JOIN pg_namespace n ON p.pronamespace = n.oid, " +
		"aclexplode(p.proacl) as acl " +
		"INNER JOIN pg_roles s ON acl.grantee = s.oid " +
		"WHERE n.nspname=$2 " +
		"AND s.rolname=$3 " +
		"AND acl.is_grantable=$4 " +
		"GROUP BY n.nspname, s.rolname, acl.is_grantable, p.oid " +
		"HAVING array_agg(acl.privilege_type ORDER BY privilege_type ASC) " +
		"= (SELECT array(SELECT unnest($5::text[]) as perms ORDER BY perms ASC))" +
		") sub " +
		"WHERE sub.signature = ANY($6)"
	q.Parameters = []interface{}{
		len(gp.Routines), gp.Schema, gp.Role,
		gro, pq.Array(sp), pq.Array(routinesSignatures),
	}
}

func selectForeignDataWrapperGrantQuery(gp namespacedv1alpha1.GrantParameters, q *xsql.Query) {
	gro := gp.WithOption != nil && *gp.WithOption == namespacedv1alpha1.GrantOptionGrant
	ep := gp.ExpandPrivileges()
	sp := ep.ToStringSlice()

	q.String = "SELECT COUNT(*) >= $1 AS ct " +
		"FROM (SELECT 1 " +
		"FROM information_schema.role_usage_grants " +
		"WHERE grantee=$2 " +
		"AND object_type = 'FOREIGN DATA WRAPPER' " +
		"AND object_name = ANY($3) " +
		"AND is_grantable=$4 " +
		"GROUP BY object_name " +
		"HAVING array_agg(TEXT(privilege_type) ORDER BY privilege_type ASC) " +
		"= (SELECT array(SELECT unnest($5::text[]) as perms ORDER BY perms ASC))" +
		") sub"
	q.Parameters = []interface{}{
		len(gp.ForeignDataWrappers), gp.Role,
		pq.Array(gp.ForeignDataWrappers), yesOrNo(gro), pq.Array(sp),
	}
}

func selectForeignServerGrantQuery(gp namespacedv1alpha1.GrantParameters, q *xsql.Query) {
	gro := gp.WithOption != nil && *gp.WithOption == namespacedv1alpha1.GrantOptionGrant
	ep := gp.ExpandPrivileges()
	sp := ep.ToStringSlice()

	q.String = "SELECT COUNT(*) >= $1 AS ct " +
		"FROM (SELECT 1 " +
		"FROM information_schema.role_usage_grants " +
		"WHERE grantee=$2 " +
		"AND object_type = 'FOREIGN SERVER' " +
		"AND object_name = ANY($3) " +
		"AND is_grantable=$4 " +
		"GROUP BY object_name " +
		"HAVING array_agg(TEXT(privilege_type) ORDER BY privilege_type ASC) " +
		"= (SELECT array(SELECT unnest($5::text[]) as perms ORDER BY perms ASC))" +
		") sub"
	q.Parameters = []interface{}{
		len(gp.ForeignServers), gp.Role,
		pq.Array(gp.ForeignServers), yesOrNo(gro), pq.Array(sp),
	}
}

// yesOrNo converts a boolean to the YES/NO string used by information_schema views.
func yesOrNo(b bool) string {
	if b {
		return "YES"
	}
	return "NO"
}

func selectGrantQuery(gp namespacedv1alpha1.GrantParameters, q *xsql.Query) error { // nolint: gocyclo
	gt, err := resolveGrantType(gp)
	if err != nil {
		return err
	}

	switch gt {
	case namespacedv1alpha1.RoleMember:
		selectMemberGrantQuery(gp, q)
	case namespacedv1alpha1.RoleDatabase:
		selectDatabaseGrantQuery(gp, q)
	case namespacedv1alpha1.RoleSchema:
		selectSchemaGrantQuery(gp, q)
	case namespacedv1alpha1.RoleTable:
		selectTableGrantQuery(gp, q)
	case namespacedv1alpha1.RoleColumn:
		selectColumnGrantQuery(gp, q)
	case namespacedv1alpha1.RoleSequence:
		selectSequenceGrantQuery(gp, q)
	case namespacedv1alpha1.RoleRoutine:
		selectRoutineGrantQuery(gp, q)
	case namespacedv1alpha1.RoleForeignDataWrapper:
		selectForeignDataWrapperGrantQuery(gp, q)
	case namespacedv1alpha1.RoleForeignServer:
		selectForeignServerGrantQuery(gp, q)
	default:
		return errors.Errorf(errUnsupportedGrant, gt)
	}
	return nil
}

func withOption(option *namespacedv1alpha1.GrantOption) string {
	if option != nil {
		return fmt.Sprintf("WITH %s OPTION", string(*option))
	}
	return ""
}

func createGrantQueries(gp namespacedv1alpha1.GrantParameters, ql *[]xsql.Query) error { // nolint: gocyclo
	gt, err := resolveGrantType(gp)
	if err != nil {
		return err
	}

	ro := pq.QuoteIdentifier(*gp.Role)

	switch gt {
	case namespacedv1alpha1.RoleMember:
		if gp.MemberOf == nil || gp.Role == nil {
			return errors.Errorf(errInvalidParams, namespacedv1alpha1.RoleMember)
		}
		mo := pq.QuoteIdentifier(*gp.MemberOf)
		*ql = append(*ql,
			xsql.Query{String: fmt.Sprintf("REVOKE %s FROM %s", mo, ro)},
			xsql.Query{String: fmt.Sprintf("GRANT %s TO %s %s", mo, ro, withOption(gp.WithOption))},
		)
		return nil

	case namespacedv1alpha1.RoleDatabase:
		if gp.Database == nil || gp.Role == nil || len(gp.Privileges) < 1 {
			return errors.Errorf(errInvalidParams, namespacedv1alpha1.RoleDatabase)
		}
		db := pq.QuoteIdentifier(*gp.Database)
		sp := strings.Join(gp.Privileges.ToStringSlice(), ",")
		*ql = append(*ql,
			xsql.Query{String: fmt.Sprintf("REVOKE %s ON DATABASE %s FROM %s", sp, db, ro)},
			xsql.Query{String: fmt.Sprintf("GRANT %s ON DATABASE %s TO %s %s", sp, db, ro, withOption(gp.WithOption))},
		)
		if gp.RevokePublicOnDb != nil && *gp.RevokePublicOnDb {
			*ql = append(*ql, xsql.Query{String: fmt.Sprintf("REVOKE ALL ON DATABASE %s FROM PUBLIC", db)})
		}
		return nil

	case namespacedv1alpha1.RoleSchema:
		if gp.Database == nil || gp.Schema == nil || gp.Role == nil || len(gp.Privileges) < 1 {
			return errors.Errorf(errInvalidParams, namespacedv1alpha1.RoleSchema)
		}
		sh := pq.QuoteIdentifier(*gp.Schema)
		sp := strings.Join(gp.Privileges.ToStringSlice(), ",")
		*ql = append(*ql,
			xsql.Query{String: fmt.Sprintf("REVOKE %s ON SCHEMA %s FROM %s", sp, sh, ro)},
			xsql.Query{String: fmt.Sprintf("GRANT %s ON SCHEMA %s TO %s %s", sp, sh, ro, withOption(gp.WithOption))},
		)
		return nil

	case namespacedv1alpha1.RoleTable:
		if gp.Database == nil || gp.Schema == nil || len(gp.Tables) < 1 || gp.Role == nil || len(gp.Privileges) < 1 {
			return errors.Errorf(errInvalidParams, namespacedv1alpha1.RoleTable)
		}
		tb := strings.Join(prefixAndQuote(*gp.Schema, gp.Tables), ",")
		sp := strings.Join(gp.Privileges.ToStringSlice(), ",")
		*ql = append(*ql,
			xsql.Query{String: fmt.Sprintf("REVOKE %s ON TABLE %s FROM %s", sp, tb, ro)},
			xsql.Query{String: fmt.Sprintf("GRANT %s ON TABLE %s TO %s %s", sp, tb, ro, withOption(gp.WithOption))},
		)
		return nil

	case namespacedv1alpha1.RoleColumn:
		if gp.Database == nil || gp.Schema == nil || len(gp.Tables) < 1 || len(gp.Columns) < 1 || gp.Role == nil || len(gp.Privileges) < 1 {
			return errors.Errorf(errInvalidParams, namespacedv1alpha1.RoleColumn)
		}
		co := strings.Join(quoteIdentifiers(gp.Columns), ",")
		cp := columnsPrivileges(gp.Privileges.ToStringSlice(), co)
		tb := strings.Join(prefixAndQuote(*gp.Schema, gp.Tables), ",")
		*ql = append(*ql,
			xsql.Query{String: fmt.Sprintf("REVOKE %s ON TABLE %s FROM %s", cp, tb, ro)},
			xsql.Query{String: fmt.Sprintf("GRANT %s ON TABLE %s TO %s %s", cp, tb, ro, withOption(gp.WithOption))},
		)
		return nil

	case namespacedv1alpha1.RoleSequence:
		if gp.Database == nil || gp.Schema == nil || len(gp.Sequences) < 1 || gp.Role == nil || len(gp.Privileges) < 1 {
			return errors.Errorf(errInvalidParams, namespacedv1alpha1.RoleSequence)
		}
		sq := strings.Join(prefixAndQuote(*gp.Schema, gp.Sequences), ",")
		sp := strings.Join(gp.Privileges.ToStringSlice(), ",")
		*ql = append(*ql,
			xsql.Query{String: fmt.Sprintf("REVOKE %s ON SEQUENCE %s FROM %s", sp, sq, ro)},
			xsql.Query{String: fmt.Sprintf("GRANT %s ON SEQUENCE %s TO %s %s", sp, sq, ro, withOption(gp.WithOption))},
		)
		return nil

	case namespacedv1alpha1.RoleRoutine:
		if gp.Database == nil || gp.Schema == nil || len(gp.Routines) < 1 || gp.Role == nil || len(gp.Privileges) < 1 {
			return errors.Errorf(errInvalidParams, namespacedv1alpha1.RoleRoutine)
		}
		rt := strings.Join(quotedSignatures(*gp.Schema, gp.Routines), ",")
		sp := strings.Join(gp.Privileges.ToStringSlice(), ",")
		*ql = append(*ql,
			xsql.Query{String: fmt.Sprintf("REVOKE %s ON ROUTINE %s FROM %s", sp, rt, ro)},
			xsql.Query{String: fmt.Sprintf("GRANT %s ON ROUTINE %s TO %s %s", sp, rt, ro, withOption(gp.WithOption))},
		)
		return nil

	case namespacedv1alpha1.RoleForeignDataWrapper:
		if gp.Database == nil || len(gp.ForeignDataWrappers) < 1 || gp.Role == nil || len(gp.Privileges) < 1 {
			return errors.Errorf(errInvalidParams, namespacedv1alpha1.RoleForeignDataWrapper)
		}
		sp := strings.Join(gp.Privileges.ToStringSlice(), ",")
		*ql = append(*ql,
			xsql.Query{String: fmt.Sprintf("REVOKE %s ON FOREIGN DATA WRAPPER %s FROM %s",
				sp, strings.Join(quoteIdentifiers(gp.ForeignDataWrappers), ","), ro)},
			xsql.Query{String: fmt.Sprintf("GRANT %s ON FOREIGN DATA WRAPPER %s TO %s %s",
				sp, strings.Join(quoteIdentifiers(gp.ForeignDataWrappers), ","), ro, withOption(gp.WithOption))},
		)
		return nil

	case namespacedv1alpha1.RoleForeignServer:
		if gp.Database == nil || len(gp.ForeignServers) < 1 || gp.Role == nil || len(gp.Privileges) < 1 {
			return errors.Errorf(errInvalidParams, namespacedv1alpha1.RoleForeignServer)
		}
		sp := strings.Join(gp.Privileges.ToStringSlice(), ",")
		*ql = append(*ql,
			xsql.Query{String: fmt.Sprintf("REVOKE %s ON FOREIGN SERVER %s FROM %s",
				sp, strings.Join(quoteIdentifiers(gp.ForeignServers), ","), ro)},
			xsql.Query{String: fmt.Sprintf("GRANT %s ON FOREIGN SERVER %s TO %s %s",
				sp, strings.Join(quoteIdentifiers(gp.ForeignServers), ","), ro, withOption(gp.WithOption))},
		)
		return nil
	}
	return errors.Errorf(errUnsupportedGrant, gt)
}

func deleteGrantQuery(gp namespacedv1alpha1.GrantParameters, q *xsql.Query) error { // nolint: gocyclo
	gt, err := resolveGrantType(gp)
	if err != nil {
		return err
	}

	ro := pq.QuoteIdentifier(*gp.Role)

	switch gt {
	case namespacedv1alpha1.RoleMember:
		q.String = fmt.Sprintf("REVOKE %s FROM %s", pq.QuoteIdentifier(*gp.MemberOf), ro)
		return nil
	case namespacedv1alpha1.RoleDatabase:
		q.String = fmt.Sprintf("REVOKE %s ON DATABASE %s FROM %s",
			strings.Join(gp.Privileges.ToStringSlice(), ","),
			pq.QuoteIdentifier(*gp.Database), ro)
		return nil
	case namespacedv1alpha1.RoleSchema:
		q.String = fmt.Sprintf("REVOKE %s ON SCHEMA %s FROM %s",
			strings.Join(gp.Privileges.ToStringSlice(), ","),
			pq.QuoteIdentifier(*gp.Schema), ro)
		return nil
	case namespacedv1alpha1.RoleTable:
		q.String = fmt.Sprintf("REVOKE %s ON TABLE %s FROM %s",
			strings.Join(gp.Privileges.ToStringSlice(), ","),
			strings.Join(prefixAndQuote(*gp.Schema, gp.Tables), ","), ro)
		return nil
	case namespacedv1alpha1.RoleColumn:
		co := strings.Join(quoteIdentifiers(gp.Columns), ",")
		cp := columnsPrivileges(gp.Privileges.ToStringSlice(), co)
		q.String = fmt.Sprintf("REVOKE %s ON TABLE %s FROM %s",
			cp, strings.Join(prefixAndQuote(*gp.Schema, gp.Tables), ","), ro)
		return nil
	case namespacedv1alpha1.RoleSequence:
		q.String = fmt.Sprintf("REVOKE %s ON SEQUENCE %s FROM %s",
			strings.Join(gp.Privileges.ToStringSlice(), ","),
			strings.Join(prefixAndQuote(*gp.Schema, gp.Sequences), ","), ro)
		return nil
	case namespacedv1alpha1.RoleRoutine:
		q.String = fmt.Sprintf("REVOKE %s ON ROUTINE %s FROM %s",
			strings.Join(gp.Privileges.ToStringSlice(), ","),
			strings.Join(quotedSignatures(*gp.Schema, gp.Routines), ","), ro)
		return nil
	case namespacedv1alpha1.RoleForeignDataWrapper:
		q.String = fmt.Sprintf("REVOKE %s ON FOREIGN DATA WRAPPER %s FROM %s",
			strings.Join(gp.Privileges.ToStringSlice(), ","),
			strings.Join(quoteIdentifiers(gp.ForeignDataWrappers), ","), ro)
		return nil
	case namespacedv1alpha1.RoleForeignServer:
		q.String = fmt.Sprintf("REVOKE %s ON FOREIGN SERVER %s FROM %s",
			strings.Join(gp.Privileges.ToStringSlice(), ","),
			strings.Join(quoteIdentifiers(gp.ForeignServers), ","), ro)
		return nil
	}
	return errors.Errorf(errUnsupportedGrant, gt)
}

// routineSignature returns the routine in the same format used by the select query.
func routineSignature(r namespacedv1alpha1.Routine) string {
	args := make([]string, len(r.Arguments))
	for i, v := range r.Arguments {
		args[i] = strings.ToLower(v)
	}
	return r.Name + "(" + strings.Join(args, ",") + ")"
}

// quotedSignatures returns routines in a quoted grantable format, prefixed with the schema.
func quotedSignatures(sc string, rs []namespacedv1alpha1.Routine) []string {
	qsc := pq.QuoteIdentifier(sc)
	sigs := make([]string, len(rs))
	for i, r := range rs {
		sigs[i] = qsc + "." + pq.QuoteIdentifier(r.Name) + "(" + strings.Join(quoteIdentifiers(r.Arguments), ",") + ")"
	}
	return sigs
}

// quoteIdentifiers returns a slice of PostgreSQL-quoted identifiers.
func quoteIdentifiers(items []string) []string {
	ret := make([]string, len(items))
	for i, v := range items {
		ret[i] = pq.QuoteIdentifier(v)
	}
	return ret
}

// prefixAndQuote returns objects in a quoted grantable format, prefixed with the schema.
func prefixAndQuote(sc string, obj []string) []string {
	qsc := pq.QuoteIdentifier(sc)
	ret := make([]string, len(obj))
	for i, v := range obj {
		ret[i] = qsc + "." + pq.QuoteIdentifier(v)
	}
	return ret
}

// columnsPrivileges returns the privileges for columns in grant format.
func columnsPrivileges(priv []string, cols string) string {
	ret := make([]string, len(priv))
	for i, v := range priv {
		ret[i] = v + "(" + cols + ")"
	}
	return strings.Join(ret, ",")
}

func (c *external) Observe(ctx context.Context, mg *namespacedv1alpha1.Grant) (managed.ExternalObservation, error) {
	if mg.Spec.ForProvider.Role == nil {
		return managed.ExternalObservation{}, errors.New(errNoRole)
	}

	gp := mg.Spec.ForProvider
	var query xsql.Query
	if err := selectGrantQuery(gp, &query); err != nil {
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
	mg.SetConditions(xpv1.Available())

	return managed.ExternalObservation{
		ResourceExists:          true,
		ResourceUpToDate:        true,
		ResourceLateInitialized: false,
	}, nil
}

func (c *external) Create(ctx context.Context, mg *namespacedv1alpha1.Grant) (managed.ExternalCreation, error) {
	var queries []xsql.Query

	mg.SetConditions(xpv1.Creating())

	if err := createGrantQueries(mg.Spec.ForProvider, &queries); err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, errCreateGrant)
	}

	err := c.db.ExecTx(ctx, queries)
	return managed.ExternalCreation{}, errors.Wrap(err, errCreateGrant)
}

func (c *external) Update(ctx context.Context, mg *namespacedv1alpha1.Grant) (managed.ExternalUpdate, error) {
	// Update is a no-op, as permissions are fully revoked and then granted in the Create function,
	// inside a transaction.
	return managed.ExternalUpdate{}, nil
}

func (c *external) Disconnect(ctx context.Context) error {
	return nil
}

func (c *external) Delete(ctx context.Context, mg *namespacedv1alpha1.Grant) (managed.ExternalDelete, error) {
	var query xsql.Query

	mg.SetConditions(xpv1.Deleting())

	err := deleteGrantQuery(mg.Spec.ForProvider, &query)
	if err != nil {
		return managed.ExternalDelete{}, errors.Wrap(err, errRevokeGrant)
	}

	return managed.ExternalDelete{}, errors.Wrap(c.db.Exec(ctx, query), errRevokeGrant)
}
