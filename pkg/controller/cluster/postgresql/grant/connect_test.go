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
	"context"

	"testing"

	"github.com/pkg/errors"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"

	"github.com/crossplane-contrib/provider-sql/apis/cluster/postgresql/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
)

// kubeWithSecret returns a client that resolves a ProviderConfig with a
// connection secret and the given default database.
func kubeWithSecret(defaultDB string) client.Client {
	return &test.MockClient{
		MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
			if o, ok := obj.(*v1alpha1.ProviderConfig); ok {
				o.Spec.Credentials.ConnectionSecretRef = &xpv1.SecretReference{}
				o.Spec.DefaultDatabase = defaultDB
			}
			return nil
		}),
	}
}

func databaseGrant(db string) *v1alpha1.Grant {
	return &v1alpha1.Grant{
		Spec: v1alpha1.GrantSpec{
			ResourceSpec: xpv1.ResourceSpec{
				ProviderConfigReference: &xpv1.Reference{},
			},
			ForProvider: v1alpha1.GrantParameters{
				Role:       ptr.To("appuser"),
				Database:   ptr.To(db),
				Privileges: v1alpha1.GrantPrivileges{"CONNECT"},
			},
		},
	}
}

// TestConnectServerVersionUnavailable pins the regression introduced by #374
// (and its follow-up moving GetServerVersion from Observe/Create into Connect).
//
// In v0.15.0 Connect performed no database I/O. It now issues
// "SELECT current_setting('server_version_num')::int" and aborts on any error.
// Because Connect gates every operation, a backend that does not expose
// server_version_num (CockroachDB, pgbouncer admin targets, other wire-
// compatible proxies) can no longer Observe, Create *or Delete* a Grant — the
// finalizer wedges and the resource cannot be removed.
//
// ExpandPrivilegesWithVersion already treats serverVersion==0 as "latest,
// include all privileges", which is exactly v0.15.0's unconditional behaviour.
// Connect should therefore fall back to 0 rather than fail.
func TestConnectServerVersionUnavailable(t *testing.T) {
	errBoom := errors.New("ERROR: unrecognized configuration parameter \"server_version_num\"")

	c := &connector{
		kube:  kubeWithSecret("postgres"),
		track: func(context.Context, resource.LegacyManaged) error { return nil },
		newDB: func(_ map[string][]byte, _ string, _ string) xsql.DB {
			return mockDB{
				MockGetServerVersion: func(context.Context) (int, error) { return 0, errBoom },
			}
		},
	}

	ext, err := c.Connect(context.Background(), databaseGrant("appdb"))
	if err != nil {
		t.Fatalf("Connect() must not fail when the server does not expose "+
			"server_version_num; it should fall back to version 0 (all privileges), "+
			"as v0.15.0 did. Got error: %v", err)
	}
	if ext == nil {
		t.Fatal("Connect() returned a nil external client without an error")
	}
}

// TestConnectDatabaseGrantTargetsDefaultDatabase pins the regression introduced
// by #345.
//
// v0.15.0 always connected to pc.Spec.DefaultDatabase:
//
//	db: c.newDB(s.Data, pc.Spec.DefaultDatabase, ...)
//
// Post-#345 Connect targets the grant's own spec.forProvider.database when set.
// A ROLE_DATABASE grant always sets it, so database-level grants now open a
// session against the grant's target database.
//
// Combined with the GetServerVersion call above, this means a database-level
// Grant now requires the provider's credentials to be able to *open a session
// to the target database* before the grant is applied. It did not before.
// Consequences: the target Database must already exist and be connectable, and
// `revokePublicOnDb: true` can lock the provider out of the very database it
// must reconnect to on the next reconcile.
//
// GRANT ... ON DATABASE x and the pg_database existence SELECT both work from
// any session, so a database-level grant has no need to connect to x.
func TestConnectDatabaseGrantTargetsDefaultDatabase(t *testing.T) {
	var connectedTo string

	c := &connector{
		kube:  kubeWithSecret("postgres"),
		track: func(context.Context, resource.LegacyManaged) error { return nil },
		newDB: func(_ map[string][]byte, database string, _ string) xsql.DB {
			connectedTo = database
			return mockDB{}
		},
	}

	if _, err := c.Connect(context.Background(), databaseGrant("appdb")); err != nil {
		t.Fatalf("Connect() failed: %v", err)
	}

	if want := "postgres"; connectedTo != want {
		t.Errorf("a database-level Grant connected to %q, want %q (the "+
			"ProviderConfig default database). GRANT ... ON DATABASE and the "+
			"pg_database existence check both work from any session, so the "+
			"provider should not require a session on the grant target.",
			connectedTo, want)
	}
}

// TestConnectObjectGrantTargetsGrantDatabase is the counterpart to the test
// above: grants on objects *inside* a database must still open their session on
// that database, because pg_namespace/pg_class/pg_proc are database local.
//
// Without this, the fix for database-level grants would silently break every
// schema/table/column/sequence/routine grant.
func TestConnectObjectGrantTargetsGrantDatabase(t *testing.T) {
	for _, tc := range []struct {
		name string
		gp   func(*v1alpha1.GrantParameters)
	}{
		{"schema", func(p *v1alpha1.GrantParameters) { p.Schema = ptr.To("myschema") }},
		{"table", func(p *v1alpha1.GrantParameters) {
			p.Schema = ptr.To("myschema")
			p.Tables = []string{"t1"}
		}},
		{"routine", func(p *v1alpha1.GrantParameters) {
			p.Schema = ptr.To("myschema")
			p.Routines = []v1alpha1.Routine{{Name: "f", Arguments: []string{"text"}}}
		}},
	} {
		t.Run(tc.name, func(t *testing.T) {
			var connectedTo string
			g := databaseGrant("appdb")
			tc.gp(&g.Spec.ForProvider)

			c := &connector{
				kube:  kubeWithSecret("postgres"),
				track: func(context.Context, resource.LegacyManaged) error { return nil },
				newDB: func(_ map[string][]byte, database string, _ string) xsql.DB {
					connectedTo = database
					return mockDB{}
				},
			}
			if _, err := c.Connect(context.Background(), g); err != nil {
				t.Fatalf("Connect() failed: %v", err)
			}
			if want := "appdb"; connectedTo != want {
				t.Errorf("a %s grant connected to %q, want %q (the grant's target "+
					"database; its catalog is database local)", tc.name, connectedTo, want)
			}
		})
	}
}
