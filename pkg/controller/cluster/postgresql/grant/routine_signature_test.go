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

	"github.com/crossplane-contrib/provider-sql/apis/cluster/postgresql/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
)

// TestSelectRoutineGrantQueryDoesNotCrossJoinArgsWithAcl guards against a
// regression where a routine Grant on a function with two or more arguments
// could never be observed as existing.
//
// The query used to join unnest(p.proargtypes) -- one row per ARGUMENT -- into
// the same FROM clause as aclexplode(p.proacl) -- one row per PRIVILEGE. The
// comma between two set-returning functions is a cross join, so
//
//	array_agg(acl.privilege_type ORDER BY privilege_type ASC)
//
// collected one entry per argument. EXECUTE is the only privilege a function
// can hold, so the HAVING equality against the requested ARRAY['EXECUTE'] held
// only for functions with exactly one argument.
//
// Measured against PostgreSQL 18.4, before the fix:
//
//	0 args -> observed (the LEFT JOIN produced a single NULL row)
//	1 arg  -> observed
//	2 args -> never observed
//	9 args -> never observed
//
// The GRANT itself succeeded, so the resource reported Synced=True and
// ReconcileSuccess while Observe returned ResourceExists=false forever: Create
// retried indefinitely and Ready stayed False.
//
// The argument types are now formatted in a correlated subquery, keeping the
// argument rows out of the ACL aggregation entirely.
func TestSelectRoutineGrantQueryDoesNotCrossJoinArgsWithAcl(t *testing.T) {
	gp := v1alpha1.GrantParameters{
		Role:       ptr.To("mydbrole"),
		Database:   ptr.To("mydb"),
		Schema:     ptr.To("aws_s3"),
		Privileges: v1alpha1.GrantPrivileges{"EXECUTE"},
		Routines: []v1alpha1.Routine{{
			Name:      "table_import_from_s3",
			Arguments: []string{"text", "text", "text", "text", "text", "text", "text", "text", "text"},
		}},
	}

	q := &xsql.Query{}
	if err := selectRoutineGrantQuery(gp, q); err != nil {
		t.Fatalf("selectRoutineGrantQuery: %v", err)
	}

	if strings.Contains(q.String, "LEFT JOIN unnest(p.proargtypes)") {
		t.Errorf("selectRoutineGrantQuery joins unnest(p.proargtypes) into the outer "+
			"query, crossing argument rows with aclexplode() privilege rows. "+
			"array_agg(acl.privilege_type) then yields one entry per argument, so any "+
			"function with 2+ arguments can never be observed. Format the argument "+
			"types in a correlated subquery instead. Query:\n%s", q.String)
	}

	if !strings.Contains(q.String, "FROM unnest(p.proargtypes) WITH ORDINALITY AS a(t, ord)") {
		t.Errorf("the routine signature is no longer derived from p.proargtypes; the "+
			"signature comparison would be wrong. Query:\n%s", q.String)
	}

	// Once the argument rows are gone, proname/proargtypes are only groupable
	// via the pg_proc primary-key functional dependency. Spell them out so the
	// query stays obvious and portable.
	if !strings.Contains(q.String, "GROUP BY n.nspname, s.rolname, acl.is_grantable, p.oid, p.proname, p.proargtypes") {
		t.Errorf("unexpected GROUP BY; the signature columns must be grouped. Query:\n%s", q.String)
	}

	// The parameters are unchanged by the fix: routine count, schema, role,
	// grant option, privileges, signatures.
	if len(q.Parameters) != 6 {
		t.Fatalf("got %d query parameters, want 6", len(q.Parameters))
	}
	if got, want := q.Parameters[0], 1; got != want {
		t.Errorf("parameter $1 (routine count) = %v, want %v", got, want)
	}
}

// TestRoutineSignatureMatchesFormatType documents the signature format the
// Observe query compares against. pg_catalog.format_type() emits comma-separated
// canonical type names with no spaces, which is what routineSignature must
// produce for `sub.signature = ANY($6)` to ever match.
func TestRoutineSignatureMatchesFormatType(t *testing.T) {
	got := routineSignature(v1alpha1.Routine{
		Name:      "table_import_from_s3",
		Arguments: []string{"text", "TEXT", "text"},
	})
	want := "table_import_from_s3(text,text,text)"
	if got != want {
		t.Errorf("routineSignature() = %q, want %q", got, want)
	}
}
