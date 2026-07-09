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
//	owner, before Create ran     -> not exists (datacl is NULL; Create must run)
//	non-owner, exact match       -> exists
//	non-owner holding a superset -> not exists (equality preserved)
//
// The pre-Create case matters: an "owner => exists" short-circuit would skip
// Create altogether, and with it `revokePublicOnDb`.
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

	// datacl must still be required: short-circuiting on ownership alone would
	// skip Create, and with it revokePublicOnDb.
	if !strings.Contains(q.String, "aclexplode(db.datacl) as acl") {
		t.Errorf("the query no longer inspects datacl; Create (and revokePublicOnDb) "+
			"would be skipped for owners. Query:\n%s", q.String)
	}

	if len(q.Parameters) != 4 {
		t.Fatalf("got %d parameters, want 4", len(q.Parameters))
	}
}
