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
	"testing"

	"k8s.io/utils/ptr"

	"github.com/crossplane-contrib/provider-sql/apis/namespaced/postgresql/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
)

// TestWithInheritToleratesUnknownServerVersion pins the interaction between two
// independently-correct changes.
//
// Connect falls back to serverVersion == versionUnknown (0) when the backend
// cannot answer `SELECT current_setting('server_version_num')::int`, rather than
// failing and wedging the finalizer. ExpandPrivilegesWithVersion already treats
// 0 as "latest, include every privilege".
//
// The withInherit gate, however, was a plain `serverVersion < 160000`, which
// reads the same 0 as "older than PostgreSQL 16". A genuine PG16+ server that
// merely cannot report its version would therefore have withInherit rejected.
//
// Unknown must mean "assume newest", consistently. A genuinely old server still
// gets a clean error -- see TestWithInheritRejectedOnOldServer.
func TestWithInheritToleratesUnknownServerVersion(t *testing.T) {
	gp := memberGrantWithInherit()

	var ql []xsql.Query
	if err := createGrantQueriesWithVersion(gp, &ql, versionUnknown); err != nil {
		t.Errorf("createGrantQueriesWithVersion(serverVersion=versionUnknown) = %v, want nil.\n"+
			"An unknown server version must be treated as newest, not as pre-16, or a "+
			"PG16 backend that cannot report server_version_num loses withInherit.", err)
	}

	q := &xsql.Query{}
	if err := selectGrantQueryWithVersion(gp, q, versionUnknown); err != nil {
		t.Errorf("selectGrantQueryWithVersion(serverVersion=versionUnknown) = %v, want nil", err)
	}
}

// TestWithInheritRejectedOnOldServer is the counterpart: the version gate must
// still fire for a server that really does predate PostgreSQL 16.
func TestWithInheritRejectedOnOldServer(t *testing.T) {
	gp := memberGrantWithInherit()

	var ql []xsql.Query
	if err := createGrantQueriesWithVersion(gp, &ql, 150000); err == nil {
		t.Error("createGrantQueriesWithVersion(serverVersion=150000) = nil, want an error: " +
			"withInherit requires PostgreSQL 16+")
	}

	q := &xsql.Query{}
	if err := selectGrantQueryWithVersion(gp, q, 150000); err == nil {
		t.Error("selectGrantQueryWithVersion(serverVersion=150000) = nil, want an error")
	}
}

func memberGrantWithInherit() v1alpha1.GrantParameters {
	inherit := false
	return v1alpha1.GrantParameters{
		Role:        ptr.To("myrole"),
		MemberOf:    ptr.To("mygroup"),
		WithInherit: &inherit,
	}
}
