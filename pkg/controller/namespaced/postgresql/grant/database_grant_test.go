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
	"strings"
	"testing"

	"k8s.io/utils/ptr"

	"github.com/crossplane-contrib/provider-sql/apis/namespaced/postgresql/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
)

// TestSelectDatabaseGrantQueryToleratesOwnerImplicitPrivileges guards the fix
// for issue #240.
//
// A role that owns a database implicitly holds CONNECT, CREATE and TEMPORARY on
// it. PostgreSQL materialises all three into datacl as soon as anything is
// granted or revoked, so a Grant requesting a subset -- typically just CONNECT
// -- could never satisfy the old exact set comparison. Observe reported the
// grant as missing forever, Create reapplied it forever, and Ready never became
// true. Requesting ALL masked the bug because ExpandPrivileges expands it to
// exactly those three privileges.
//
// Verified against PostgreSQL 18.4 with the query this function emits:
//
//	owner, CONNECT only          -> exists     (was: never)
//	owner, all three             -> exists
//	owner, datacl still NULL     -> not exists (nothing granted yet; Create runs)
//	non-owner, exact match       -> exists
//	non-owner holding a superset -> not exists (equality preserved)
func TestSelectDatabaseGrantQueryToleratesOwnerImplicitPrivileges(t *testing.T) {
	gp := v1alpha1.GrantParameters{
		Role:       ptr.To("svc"),
		Database:   ptr.To("svcdb"),
		Privileges: v1alpha1.GrantPrivileges{"CONNECT"},
	}

	q := &xsql.Query{}
	if err := selectDatabaseGrantQuery(gp, q); err != nil {
		t.Fatalf("selectDatabaseGrantQuery: %v", err)
	}

	// The owner branch must use containment, not equality.
	if !strings.Contains(q.String, "WHEN db.datdba = s.oid") {
		t.Errorf("selectDatabaseGrantQuery does not special-case the database owner, "+
			"so a Grant asking for a subset of the owner's implicit "+
			"{CONNECT,CREATE,TEMPORARY} can never be observed (issue #240). Query:\n%s",
			q.String)
	}
	if !strings.Contains(q.String, "@> (SELECT array(SELECT unnest($4::text[])") {
		t.Errorf("the owner branch must compare with containment (@>). Query:\n%s", q.String)
	}

	// Non-owners keep exact equality: the provider fully controls their grants,
	// so a superset means drift.
	if !strings.Contains(q.String, "ELSE array_agg(acl.privilege_type ORDER BY privilege_type ASC) = (SELECT array(") {
		t.Errorf("non-owner grantees must still be compared with set equality. Query:\n%s", q.String)
	}

	// datacl must still be inspected: an unconditional "owner => exists"
	// short-circuit would skip Create for owners entirely.
	if !strings.Contains(q.String, "aclexplode(db.datacl) as acl") {
		t.Errorf("the query no longer inspects datacl; Create "+
			"would be skipped for owners. Query:\n%s", q.String)
	}

	// Without revokePublicOnDb the query must not mention PUBLIC — its shape
	// (and behaviour) is unchanged for plain database grants.
	if strings.Contains(q.String, "acl.grantee = 0") {
		t.Errorf("plain database grants must not check PUBLIC's ACL. Query:\n%s", q.String)
	}

	if len(q.Parameters) != 4 {
		t.Fatalf("got %d parameters, want 4", len(q.Parameters))
	}
}

// TestSelectDatabaseGrantQueryChecksPublicWhenRevoked pins the Observe side of
// `revokePublicOnDb`.
//
// The REVOKE ALL ... FROM PUBLIC is only issued by Create, so Observe must
// treat "PUBLIC still holds privileges" as drift. Nothing about the role's own
// privileges implies Create ever ran: ANY grant or revoke on the database — by
// another Grant resource or by hand — materialises datacl with PUBLIC's
// default CONNECT/TEMPORARY entries, which would otherwise satisfy the owner
// containment check and park the resource Ready with PUBLIC never revoked.
//
// Verified against PostgreSQL 16 with the query this function emits:
//
//	owner grant, datacl materialised by an unrelated GRANT -> not exists
//	  (PUBLIC entries present; Create runs and revokes them)
//	same, after REVOKE ALL FROM PUBLIC                     -> exists
//	PUBLIC re-granted CONNECT afterwards                   -> not exists (drift)
func TestSelectDatabaseGrantQueryChecksPublicWhenRevoked(t *testing.T) {
	gp := v1alpha1.GrantParameters{
		Role:             ptr.To("svc"),
		Database:         ptr.To("svcdb"),
		Privileges:       v1alpha1.GrantPrivileges{"CONNECT"},
		RevokePublicOnDb: ptr.To(true),
	}

	q := &xsql.Query{}
	if err := selectDatabaseGrantQuery(gp, q); err != nil {
		t.Fatalf("selectDatabaseGrantQuery: %v", err)
	}

	// PUBLIC is grantee oid 0 in aclexplode(); it has no pg_roles row, so the
	// check must not go through the pg_roles join.
	if !strings.Contains(q.String, "AND NOT EXISTS(SELECT 1 ") ||
		!strings.Contains(q.String, "acl.grantee = 0") {
		t.Errorf("revokePublicOnDb grants must observe PUBLIC's ACL entries as "+
			"drift; otherwise the REVOKE ... FROM PUBLIC is never (re)applied once "+
			"the role's own privileges are in place. Query:\n%s", q.String)
	}

	if len(q.Parameters) != 4 {
		t.Fatalf("got %d parameters, want 4", len(q.Parameters))
	}
}
