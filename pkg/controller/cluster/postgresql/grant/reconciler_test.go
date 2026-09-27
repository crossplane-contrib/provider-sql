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
	"database/sql"
	"fmt"
	"regexp"
	"sort"
	"strconv"
	"strings"
	"testing"

	"github.com/crossplane-contrib/provider-sql/apis/cluster/postgresql/v1alpha1"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/lib/pq"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
)

type mockDB struct {
	MockExec                 func(ctx context.Context, q xsql.Query) error
	MockExecTx               func(ctx context.Context, ql []xsql.Query) error
	MockScan                 func(ctx context.Context, q xsql.Query, dest ...interface{}) error
	MockQuery                func(ctx context.Context, q xsql.Query) (*sql.Rows, error)
	MockGetConnectionDetails func(username, password string) managed.ConnectionDetails
	MockGetServerVersion     func(ctx context.Context) (int, error)
}

func (m mockDB) Exec(ctx context.Context, q xsql.Query) error {
	return m.MockExec(ctx, q)
}

func (m mockDB) ExecTx(ctx context.Context, ql []xsql.Query) error {
	return m.MockExecTx(ctx, ql)
}

func (m mockDB) Scan(ctx context.Context, q xsql.Query, dest ...interface{}) error {
	return m.MockScan(ctx, q, dest...)
}

func (m mockDB) Query(ctx context.Context, q xsql.Query) (*sql.Rows, error) {
	return m.MockQuery(ctx, q)
}

func (m mockDB) GetConnectionDetails(username, password string) managed.ConnectionDetails {
	return m.MockGetConnectionDetails(username, password)
}

func (m mockDB) GetServerVersion(ctx context.Context) (int, error) {
	if m.MockGetServerVersion == nil {
		return 0, nil // Default to version 0 (latest) if not set
	}
	return m.MockGetServerVersion(ctx)
}

func TestConnect(t *testing.T) {
	errBoom := errors.New("boom")
	nopUsage := func(ctx context.Context, mg resource.LegacyManaged) error { return nil }

	type fields struct {
		kube  client.Client
		track func(context.Context, resource.LegacyManaged) error
		newDB func(creds map[string][]byte, database string, sslmode string) xsql.DB
	}

	type args struct {
		ctx context.Context
		mg  *v1alpha1.Grant
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   error
	}{
		"ErrTrackProviderConfigUsage": {
			reason: "An error should be returned if we can't track our ProviderConfig usage",
			fields: fields{
				track: func(ctx context.Context, mg resource.LegacyManaged) error { return errBoom },
			},
			args: args{
				mg: &v1alpha1.Grant{},
			},
			want: errors.Wrap(errBoom, errTrackPCUsage),
		},
		"ErrGetProviderConfig": {
			reason: "An error should be returned if we can't get our ProviderConfig",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(errBoom),
				},
				track: nopUsage,
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ClusterManagedResourceSpec: xpv2.ClusterManagedResourceSpec{
							ProviderConfigReference: &xpv2.Reference{},
						},
					},
				},
			},
			want: errors.Wrap(errBoom, errGetPC),
		},
		"ErrMissingConnectionSecret": {
			reason: "An error should be returned if our ProviderConfig doesn't specify a connection secret",
			fields: fields{
				kube: &test.MockClient{
					// We call get to populate the Grant struct, then again
					// to populate the (empty) ProviderConfig struct, resulting
					// in a ProviderConfig with a nil connection secret.
					MockGet: test.NewMockGetFn(nil),
				},
				track: nopUsage,
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ClusterManagedResourceSpec: xpv2.ClusterManagedResourceSpec{
							ProviderConfigReference: &xpv2.Reference{},
						},
					},
				},
			},
			want: errors.New(errNoSecretRef),
		},
		"ErrGetConnectionSecret": {
			reason: "An error should be returned if we can't get our ProviderConfig's connection secret",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
						switch o := obj.(type) {
						case *v1alpha1.ProviderConfig:
							o.Spec.Credentials.ConnectionSecretRef = &xpv2.SecretReference{}
						case *corev1.Secret:
							return errBoom
						}
						return nil
					}),
				},
				track: nopUsage,
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ClusterManagedResourceSpec: xpv2.ClusterManagedResourceSpec{
							ProviderConfigReference: &xpv2.Reference{},
						},
					},
				},
			},
			want: errors.Wrap(errBoom, errGetSecret),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := &connector{kube: tc.fields.kube, log: logging.NewNopLogger(), track: tc.fields.track, newDB: tc.fields.newDB}
			_, err := e.Connect(tc.args.ctx, tc.args.mg)
			if diff := cmp.Diff(tc.want, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Connect(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
		})
	}
}

func TestObserve(t *testing.T) {
	errBoom := errors.New("boom")
	goa := v1alpha1.GrantOptionAdmin
	gog := v1alpha1.GrantOptionGrant

	type fields struct {
		db            xsql.DB
		serverVersion int
	}

	type args struct {
		ctx context.Context
		mg  *v1alpha1.Grant
	}

	type want struct {
		o   managed.ExternalObservation
		err error
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   want
	}{
		"ErrNotGrant": {
			reason: "An error should be returned if the managed resource is not a *Grant",
			args: args{
				mg: nil,
			},
			want: want{
				err: errors.New(errNotGrant),
			},
		},
		"ErrBadGrant": {
			reason: "An error should be returned if the managed resource has no identifiable grant type",
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("test-example"),
							Tables:     []string{"test-example"},
							Role:       ptr.To("test-example"),
							Privileges: v1alpha1.GrantPrivileges{privAll},
						},
					},
				},
			},
			want: want{
				err: errors.New(errUnknownGrant),
			},
		},
		"SuccessNoGrant": {
			reason: "We should return ResourceExists: false when no grant is found",
			fields: fields{
				db: mockDB{
					MockScan: func(ctx context.Context, q xsql.Query, dest ...interface{}) error {
						// Default value is false, so just return
						bv := dest[0].(*bool)
						*bv = false
						return nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("test-example"),
							Role:       ptr.To("test-example"),
							Privileges: v1alpha1.GrantPrivileges{privAll},
						},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{ResourceExists: false},
			},
		},
		"AllMapsToExpandedPrivileges": {
			reason: "We expand ALL to CREATE, TEMPORARY, CONNECT when checking for existing grants",
			fields: fields{
				db: mockDB{
					MockScan: func(ctx context.Context, q xsql.Query, dest ...interface{}) error {
						privileges := q.Parameters[3]

						privs, ok := privileges.(*pq.StringArray)
						if !ok {
							return fmt.Errorf("expected Scan parameter to be pq.StringArray, got %T", privileges)
						}

						// The order is not guaranteed, so sort the slices before comparing
						sort.Strings(*privs)

						// Return if there's a diff between the expected and actual privileges
						diff := cmp.Diff(&pq.StringArray{"CONNECT", "CREATE", "TEMPORARY"}, privileges)

						bv := dest[0].(*bool)
						*bv = diff == ""

						// Extra logging in case this test is going to fail
						if diff != "" {
							t.Logf("expected empty diff, got: %s", diff)
						}

						return nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("test-example"),
							Role:       ptr.To("test-example"),
							Privileges: v1alpha1.GrantPrivileges{privAll},
						},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
				},
				err: nil,
			},
		},
		"ErrSelectGrant": {
			reason: "We should return any errors encountered while trying to show the grant",
			fields: fields{
				db: mockDB{
					MockScan: func(ctx context.Context, q xsql.Query, dest ...interface{}) error {
						return errBoom
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("test-example"),
							Role:       ptr.To("test-example"),
							Privileges: v1alpha1.GrantPrivileges{"CONNECT", "TEMPORARY"},
							WithOption: &gog,
						},
					},
				},
			},
			want: want{
				err: errors.Wrap(errBoom, errSelectGrant),
			},
		},
		"SuccessRoleDb": {
			reason: "We should return no error if we can find our role-db grant",
			fields: fields{
				db: mockDB{
					MockScan: func(ctx context.Context, q xsql.Query, dest ...interface{}) error {
						bv := dest[0].(*bool)
						*bv = true
						return nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("testdb"),
							Role:       ptr.To("testrole"),
							Privileges: v1alpha1.GrantPrivileges{privAll},
							WithOption: &gog,
						},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
				},
				err: nil,
			},
		},
		"SuccessRoleMembership": {
			reason: "We should return no error if we can find our role-membership grant",
			fields: fields{
				db: mockDB{
					MockScan: func(ctx context.Context, q xsql.Query, dest ...interface{}) error {
						bv := dest[0].(*bool)
						*bv = true
						return nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Role:       ptr.To("testrole"),
							MemberOf:   ptr.To("parentrole"),
							WithOption: &goa,
						},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
				},
				err: nil,
			},
		},
		"SuccessRoleSchema": {
			reason: "We should return no error if we can find our role-schema grant",
			fields: fields{
				db: mockDB{
					MockScan: func(ctx context.Context, q xsql.Query, dest ...interface{}) error {
						bv := dest[0].(*bool)
						*bv = true
						return nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("testdb"),
							Role:       ptr.To("testrole"),
							Schema:     ptr.To("testschema"),
							Privileges: v1alpha1.GrantPrivileges{privAll},
							WithOption: &gog,
						},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
				},
				err: nil,
			},
		},
		"SuccessRoleTable": {
			reason: "We should return no error if we can find our role-table grant",
			fields: fields{
				db: mockDB{
					MockScan: func(ctx context.Context, q xsql.Query, dest ...interface{}) error {
						bv := dest[0].(*bool)
						*bv = true
						return nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("testdb"),
							Role:       ptr.To("testrole"),
							Schema:     ptr.To("testschema"),
							Tables:     []string{"testtable"},
							Privileges: v1alpha1.GrantPrivileges{privAll},
							WithOption: &gog,
						},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
				},
				err: nil,
			},
		},
		"SuccessWildcardTables": {
			// The wildcard path builds a different query with a different
			// parameter layout, so it needs its own trip through Observe.
			reason: "We should return no error if every table in the schema carries the grant",
			fields: fields{
				db: mockDB{
					MockScan: func(ctx context.Context, q xsql.Query, dest ...interface{}) error {
						if !strings.Contains(q.String, "FROM pg_class ac") {
							return errors.New("expected the wildcard observe query")
						}
						if len(q.Parameters) != 4 {
							return errors.Errorf("expected 4 parameters, got %d", len(q.Parameters))
						}
						bv := dest[0].(*bool)
						*bv = true
						return nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("testdb"),
							Role:       ptr.To("testrole"),
							Schema:     ptr.To("testschema"),
							Tables:     []string{"*"},
							Privileges: v1alpha1.GrantPrivileges{privSelect},
						},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
				},
				err: nil,
			},
		},
		"WildcardTablesNotYetGranted": {
			reason: "A table in the schema without the grant must report ResourceExists false so Create re-runs",
			fields: fields{
				db: mockDB{
					MockScan: func(ctx context.Context, q xsql.Query, dest ...interface{}) error {
						bv := dest[0].(*bool)
						*bv = false
						return nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("testdb"),
							Role:       ptr.To("testrole"),
							Schema:     ptr.To("testschema"),
							Tables:     []string{"*"},
							Privileges: v1alpha1.GrantPrivileges{privSelect},
						},
					},
				},
			},
			want: want{
				o:   managed.ExternalObservation{ResourceExists: false},
				err: nil,
			},
		},
		"SuccessRoleColumn": {
			reason: "We should return no error if we can find our role-column grant",
			fields: fields{
				db: mockDB{
					MockScan: func(ctx context.Context, q xsql.Query, dest ...interface{}) error {
						bv := dest[0].(*bool)
						*bv = true
						return nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("testdb"),
							Role:       ptr.To("testrole"),
							Schema:     ptr.To("testschema"),
							Tables:     []string{"testtable"},
							Columns:    []string{"testcolumn"},
							Privileges: v1alpha1.GrantPrivileges{privAll},
							WithOption: &gog,
						},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
				},
				err: nil,
			},
		},
		"SuccessRoleSequence": {
			reason: "We should return no error if we can find our role-sequence grant",
			fields: fields{
				db: mockDB{
					MockScan: func(ctx context.Context, q xsql.Query, dest ...interface{}) error {
						bv := dest[0].(*bool)
						*bv = true
						return nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("testdb"),
							Role:       ptr.To("testrole"),
							Schema:     ptr.To("testschema"),
							Sequences:  []string{"testsequence"},
							Privileges: v1alpha1.GrantPrivileges{privAll},
							WithOption: &gog,
						},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
				},
				err: nil,
			},
		},
		"SuccessRoleRoutine": {
			reason: "We should return no error if we can find our role-routine grant",
			fields: fields{
				db: mockDB{
					MockScan: func(ctx context.Context, q xsql.Query, dest ...interface{}) error {
						bv := dest[0].(*bool)
						*bv = true
						return nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("testdb"),
							Role:       ptr.To("testrole"),
							Schema:     ptr.To("testschema"),
							Routines:   []v1alpha1.Routine{{Name: "testroutine", Arguments: []string{"text"}}},
							Privileges: v1alpha1.GrantPrivileges{privAll},
							WithOption: &gog,
						},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
				},
				err: nil,
			},
		},
		"SuccessRoleForeingDataWrapper": {
			reason: "We should return no error if we can find our role-foreign-data-wrapper grant",
			fields: fields{
				db: mockDB{
					MockScan: func(ctx context.Context, q xsql.Query, dest ...interface{}) error {
						bv := dest[0].(*bool)
						*bv = true
						return nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:            ptr.To("testdb"),
							Role:                ptr.To("testrole"),
							ForeignDataWrappers: []string{"testforeigndatawrapper"},
							Privileges:          v1alpha1.GrantPrivileges{privAll},
							WithOption:          &gog,
						},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
				},
				err: nil,
			},
		},
		"SuccessRoleForeignServer": {
			reason: "We should return no error if we can find our role-foreign-server grant",
			fields: fields{
				db: mockDB{
					MockScan: func(ctx context.Context, q xsql.Query, dest ...interface{}) error {
						bv := dest[0].(*bool)
						*bv = true
						return nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:       ptr.To("testdb"),
							Role:           ptr.To("testrole"),
							ForeignServers: []string{"testforeignserver"},
							Privileges:     v1alpha1.GrantPrivileges{privAll},
							WithOption:     &gog,
						},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
				},
				err: nil,
			},
		},
		"SuccessRoleMembershipWithInheritNil": {
			reason: "WithInherit nil should produce a 3-parameter query (no inherit_option filter)",
			fields: fields{
				serverVersion: 160000,
				db: mockDB{
					MockScan: func(ctx context.Context, q xsql.Query, dest ...interface{}) error {
						if len(q.Parameters) != 3 {
							return fmt.Errorf("expected 3 query parameters, got %d", len(q.Parameters))
						}
						bv := dest[0].(*bool)
						*bv = true
						return nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Role:     ptr.To("testrole"),
							MemberOf: ptr.To("parentrole"),
						},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
				},
				err: nil,
			},
		},
		"SuccessRoleMembershipWithInheritFalse": {
			reason: "WithInherit false should produce a 4-parameter query with $4 == false",
			fields: fields{
				serverVersion: 160000,
				db: mockDB{
					MockScan: func(ctx context.Context, q xsql.Query, dest ...interface{}) error {
						if len(q.Parameters) != 4 {
							return fmt.Errorf("expected 4 query parameters, got %d", len(q.Parameters))
						}
						inheritParam, ok := q.Parameters[3].(bool)
						if !ok || inheritParam != false {
							return fmt.Errorf("expected $4 to be false, got %v", q.Parameters[3])
						}
						bv := dest[0].(*bool)
						*bv = true
						return nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Role:        ptr.To("testrole"),
							MemberOf:    ptr.To("parentrole"),
							WithInherit: ptr.To(false),
						},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
				},
				err: nil,
			},
		},
		"SuccessRoleMembershipWithInheritTrue": {
			reason: "WithInherit true should produce a 4-parameter query with $4 == true",
			fields: fields{
				serverVersion: 160000,
				db: mockDB{
					MockScan: func(ctx context.Context, q xsql.Query, dest ...interface{}) error {
						if len(q.Parameters) != 4 {
							return fmt.Errorf("expected 4 query parameters, got %d", len(q.Parameters))
						}
						inheritParam, ok := q.Parameters[3].(bool)
						if !ok || inheritParam != true {
							return fmt.Errorf("expected $4 to be true, got %v", q.Parameters[3])
						}
						bv := dest[0].(*bool)
						*bv = true
						return nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Role:        ptr.To("testrole"),
							MemberOf:    ptr.To("parentrole"),
							WithInherit: ptr.To(true),
						},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
				},
				err: nil,
			},
		},
		"ErrRoleMembershipWithInheritOnPG15": {
			reason: "WithInherit on a server older than PostgreSQL 16 should return an error before any query is run",
			fields: fields{
				serverVersion: 150000,
				db: mockDB{
					MockScan: func(ctx context.Context, q xsql.Query, dest ...interface{}) error {
						return fmt.Errorf("Scan should not be called when server version is below 16")
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Role:        ptr.To("testrole"),
							MemberOf:    ptr.To("parentrole"),
							WithInherit: ptr.To(false),
						},
					},
				},
			},
			want: want{
				err: errors.Errorf(errInheritRequiresPG16, 150000),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			db := tc.fields.db
			if db == nil {
				db = mockDB{}
			}
			e := external{db: db, serverVersion: tc.fields.serverVersion}
			got, err := e.Observe(tc.args.ctx, tc.args.mg)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Observe(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.o, got); diff != "" {
				t.Errorf("\n%s\ne.Observe(...): -want, +got:\n%s\n", tc.reason, diff)
			}
		})
	}
}

func TestCreate(t *testing.T) {
	errBoom := errors.New("boom")
	goa := v1alpha1.GrantOptionAdmin

	type fields struct {
		db            xsql.DB
		serverVersion int
	}

	type args struct {
		ctx context.Context
		mg  *v1alpha1.Grant
	}

	type want struct {
		c   managed.ExternalCreation
		err error
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   want
	}{
		"ErrNotGrant": {
			reason: "An error should be returned if the managed resource is not a *Grant",
			args: args{
				mg: nil,
			},
			want: want{
				err: errors.New(errNotGrant),
			},
		},
		"ErrBadGrant": {
			reason: "An error should be returned if the managed resource has no identifiable grant type",
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("test-example"),
							Tables:     []string{"test-example"},
							Role:       ptr.To("test-example"),
							Privileges: v1alpha1.GrantPrivileges{privAll},
						},
					},
				},
			},
			want: want{
				err: errors.Wrap(errors.New(errUnknownGrant), errCreateGrant),
			},
		},
		"ErrExec": {
			reason: "Any errors encountered while creating the grant should be returned",
			fields: fields{
				db: &mockDB{
					MockExecTx: func(ctx context.Context, ql []xsql.Query) error { return errBoom },
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("test-example"),
							Role:       ptr.To("test-example"),
							Privileges: v1alpha1.GrantPrivileges{privAll},
						},
					},
				},
			},
			want: want{
				err: errors.Wrap(errBoom, errCreateGrant),
			},
		},
		"RoleMembershipSuccess": {
			reason: "No error should be returned when we successfully create a role-membership grant",
			fields: fields{
				db: &mockDB{
					MockExecTx: func(ctx context.Context, ql []xsql.Query) error { return nil },
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Role:       ptr.To("testrole"),
							MemberOf:   ptr.To("parentrole"),
							WithOption: &goa,
						},
					},
				},
			},
			want: want{
				err: nil,
			},
		},
		"RoleDatabaseSuccess": {
			reason: "No error should be returned when we successfully create a role-database grant",
			fields: fields{
				db: &mockDB{
					MockExecTx: func(ctx context.Context, ql []xsql.Query) error { return nil },
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("test-example"),
							Role:       ptr.To("test-example"),
							Privileges: v1alpha1.GrantPrivileges{privAll},
						},
					},
				},
			},
			want: want{
				err: nil,
			},
		},
		"RoleSchemaSuccess": {
			reason: "No error should be returned when we successfully create a role-schema grant",
			fields: fields{
				db: &mockDB{
					MockExecTx: func(ctx context.Context, ql []xsql.Query) error { return nil },
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("test-example"),
							Role:       ptr.To("test-example"),
							Schema:     ptr.To("test-example"),
							Privileges: v1alpha1.GrantPrivileges{privAll},
						},
					},
				},
			},
			want: want{
				err: nil,
			},
		},
		"RoleTableSuccess": {
			reason: "No error should be returned when we successfully create a role-table grant",
			fields: fields{
				db: &mockDB{
					MockExecTx: func(ctx context.Context, ql []xsql.Query) error { return nil },
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("test-example"),
							Role:       ptr.To("test-example"),
							Schema:     ptr.To("test-example"),
							Tables:     []string{"test-example"},
							Privileges: v1alpha1.GrantPrivileges{privAll},
						},
					},
				},
			},
			want: want{
				err: nil,
			},
		},
		"RoleColumnSuccess": {
			reason: "No error should be returned when we successfully create a role-column grant",
			fields: fields{
				db: &mockDB{
					MockExecTx: func(ctx context.Context, ql []xsql.Query) error { return nil },
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("test-example"),
							Role:       ptr.To("test-example"),
							Schema:     ptr.To("test-example"),
							Tables:     []string{"test-example"},
							Columns:    []string{"test-example"},
							Privileges: v1alpha1.GrantPrivileges{privAll},
						},
					},
				},
			},
			want: want{
				err: nil,
			},
		},
		"RoleSequenceSuccess": {
			reason: "No error should be returned when we successfully create a role-sequence grant",
			fields: fields{
				db: &mockDB{
					MockExecTx: func(ctx context.Context, ql []xsql.Query) error { return nil },
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("test-example"),
							Role:       ptr.To("test-example"),
							Schema:     ptr.To("test-example"),
							Sequences:  []string{"test-example"},
							Privileges: v1alpha1.GrantPrivileges{privAll},
						},
					},
				},
			},
			want: want{
				err: nil,
			},
		},
		"RoleRoutineSuccess": {
			reason: "No error should be returned when we successfully create a role-routine grant",
			fields: fields{
				db: &mockDB{
					MockExecTx: func(ctx context.Context, ql []xsql.Query) error { return nil },
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("test-example"),
							Role:       ptr.To("test-example"),
							Schema:     ptr.To("test-example"),
							Routines:   []v1alpha1.Routine{{Name: "test-example", Arguments: []string{"test-example"}}},
							Privileges: v1alpha1.GrantPrivileges{privAll},
						},
					},
				},
			},
			want: want{
				err: nil,
			},
		},
		"RoleForeignDataWrapperSuccess": {
			reason: "No error should be returned when we successfully create a role-foreign-data-wrapper grant",
			fields: fields{
				db: &mockDB{
					MockExecTx: func(ctx context.Context, ql []xsql.Query) error { return nil },
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:            ptr.To("test-example"),
							Role:                ptr.To("test-example"),
							ForeignDataWrappers: []string{"test-example"},
							Privileges:          v1alpha1.GrantPrivileges{privAll},
						},
					},
				},
			},
			want: want{
				err: nil,
			},
		},
		"RoleForeignServerSuccess": {
			reason: "No error should be returned when we successfully create a role-foreign-server grant",
			fields: fields{
				db: &mockDB{
					MockExecTx: func(ctx context.Context, ql []xsql.Query) error { return nil },
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:       ptr.To("test-example"),
							Role:           ptr.To("test-example"),
							ForeignServers: []string{"test-example"},
							Privileges:     v1alpha1.GrantPrivileges{privAll},
						},
					},
				},
			},
			want: want{
				err: nil,
			},
		},
		"RoleMembershipWithInheritNil": {
			reason: "WithInherit nil should produce a GRANT with no WITH clause",
			fields: fields{
				serverVersion: 160000,
				db: &mockDB{
					MockExecTx: func(ctx context.Context, ql []xsql.Query) error {
						if len(ql) != 2 {
							return fmt.Errorf("expected 2 queries, got %d", len(ql))
						}
						grantSQL := ql[1].String
						if strings.Contains(grantSQL, "INHERIT") {
							return fmt.Errorf("expected no INHERIT clause, got: %s", grantSQL)
						}
						return nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Role:     ptr.To("testrole"),
							MemberOf: ptr.To("parentrole"),
						},
					},
				},
			},
			want: want{
				err: nil,
			},
		},
		"RoleMembershipWithInheritFalse": {
			reason: "WithInherit false should produce a GRANT with WITH INHERIT FALSE",
			fields: fields{
				serverVersion: 160000,
				db: &mockDB{
					MockExecTx: func(ctx context.Context, ql []xsql.Query) error {
						if len(ql) != 2 {
							return fmt.Errorf("expected 2 queries, got %d", len(ql))
						}
						grantSQL := ql[1].String
						if !strings.Contains(grantSQL, "WITH INHERIT FALSE") {
							return fmt.Errorf("expected WITH INHERIT FALSE in grant SQL, got: %s", grantSQL)
						}
						return nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Role:        ptr.To("testrole"),
							MemberOf:    ptr.To("parentrole"),
							WithInherit: ptr.To(false),
						},
					},
				},
			},
			want: want{
				err: nil,
			},
		},
		"RoleMembershipWithInheritFalseAndAdminOption": {
			reason: "WithInherit false and WithOption ADMIN should produce WITH ADMIN OPTION, INHERIT FALSE",
			fields: fields{
				serverVersion: 160000,
				db: &mockDB{
					MockExecTx: func(ctx context.Context, ql []xsql.Query) error {
						if len(ql) != 2 {
							return fmt.Errorf("expected 2 queries, got %d", len(ql))
						}
						grantSQL := ql[1].String
						if !strings.Contains(grantSQL, "WITH ADMIN OPTION, INHERIT FALSE") {
							return fmt.Errorf("expected WITH ADMIN OPTION, INHERIT FALSE in grant SQL, got: %s", grantSQL)
						}
						return nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Role:        ptr.To("testrole"),
							MemberOf:    ptr.To("parentrole"),
							WithOption:  &goa,
							WithInherit: ptr.To(false),
						},
					},
				},
			},
			want: want{
				err: nil,
			},
		},
		"ErrRoleMembershipWithInheritOnPG15": {
			reason: "WithInherit on a server older than PostgreSQL 16 should return an error before any query is run",
			fields: fields{
				serverVersion: 150000,
				db: &mockDB{
					MockExecTx: func(ctx context.Context, ql []xsql.Query) error {
						return fmt.Errorf("ExecTx should not be called when server version is below 16")
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Role:        ptr.To("testrole"),
							MemberOf:    ptr.To("parentrole"),
							WithInherit: ptr.To(false),
						},
					},
				},
			},
			want: want{
				err: errors.Wrap(errors.Errorf(errInheritRequiresPG16, 150000), errCreateGrant),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			db := tc.fields.db
			if db == nil {
				db = mockDB{}
			}
			e := external{db: db, serverVersion: tc.fields.serverVersion}
			got, err := e.Create(tc.args.ctx, tc.args.mg)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Create(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.c, got); diff != "" {
				t.Errorf("\n%s\ne.Create(...): -want, +got:\n%s\n", tc.reason, diff)
			}
		})
	}
}

func TestUpdate(t *testing.T) {
	type fields struct {
		db xsql.DB
	}

	type args struct {
		ctx context.Context
		mg  *v1alpha1.Grant
	}

	type want struct {
		c   managed.ExternalUpdate
		err error
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   want
	}{
		"ErrNoOp": {
			reason: "Update is a no-op, make sure we dont throw an error *Grant",
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("test-example"),
							Role:       ptr.To("test-example"),
							Privileges: v1alpha1.GrantPrivileges{privAll},
						},
					},
				},
			},
			want: want{
				err: nil,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := external{
				db: tc.fields.db,
			}
			got, err := e.Update(tc.args.ctx, tc.args.mg)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Create(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.c, got, cmpopts.IgnoreMapEntries(func(key string, _ []byte) bool { return key == "password" })); diff != "" {
				t.Errorf("\n%s\ne.Create(...): -want, +got:\n%s\n", tc.reason, diff)
			}
		})
	}
}

func TestDelete(t *testing.T) {
	errBoom := errors.New("boom")

	type fields struct {
		db xsql.DB
	}

	type args struct {
		ctx context.Context
		mg  *v1alpha1.Grant
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   error
	}{
		"ErrNotGrant": {
			reason: "An error should be returned if the managed resource is not a *Grant",
			args: args{
				mg: nil,
			},
			want: errors.New(errNotGrant),
		},
		"ErrBadGrant": {
			reason: "An error should be returned if the managed resource has no identifiable grant type",
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("test-example"),
							Tables:     []string{"test-example"},
							Role:       ptr.To("test-example"),
							Privileges: v1alpha1.GrantPrivileges{privAll},
						},
					},
				},
			},
			want: errors.Wrap(errors.New(errUnknownGrant), errRevokeGrant),
		},
		"ErrDropGrant": {
			reason: "Errors dropping a grant should be returned",
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error {
						return errBoom
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("test-example"),
							Role:       ptr.To("test-example"),
							Privileges: v1alpha1.GrantPrivileges{privAll},
						},
					},
				},
			},
			want: errors.Wrap(errBoom, errRevokeGrant),
		},
		"RoleDatabaseSuccess": {
			reason: "No error should be returned if the role-database grant was revoked",
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("test-example"),
							Role:       ptr.To("test-example"),
							Privileges: v1alpha1.GrantPrivileges{privAll},
						},
					},
				},
			},
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error { return nil },
				},
			},
			want: nil,
		},
		"RoleSchemaSuccess": {
			reason: "No error should be returned if the role-schema grant was revoked",
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("test-example"),
							Role:       ptr.To("test-example"),
							Schema:     ptr.To("test-example"),
							Privileges: v1alpha1.GrantPrivileges{privAll},
						},
					},
				},
			},
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error { return nil },
				},
			},
			want: nil,
		},
		"RoleTableSuccess": {
			reason: "No error should be returned if the role-table grant was revoked",
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error { return nil },
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("test-example"),
							Role:       ptr.To("test-example"),
							Schema:     ptr.To("test-example"),
							Tables:     []string{"test-example"},
							Privileges: v1alpha1.GrantPrivileges{privAll},
						},
					},
				},
			},
			want: nil,
		},
		"RoleColumnSuccess": {
			reason: "No error should be returned if the role-column grant was revoked",
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error { return nil },
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("test-example"),
							Role:       ptr.To("test-example"),
							Schema:     ptr.To("test-example"),
							Tables:     []string{"test-example"},
							Columns:    []string{"test-example"},
							Privileges: v1alpha1.GrantPrivileges{privAll},
						},
					},
				},
			},
			want: nil,
		},
		"RoleSequenceSuccess": {
			reason: "No error should be returned if the role-sequence grant was revoked",
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error { return nil },
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("test-example"),
							Role:       ptr.To("test-example"),
							Schema:     ptr.To("test-example"),
							Sequences:  []string{"test-example"},
							Privileges: v1alpha1.GrantPrivileges{privAll},
						},
					},
				},
			},
			want: nil,
		},
		"RoleRoutineSuccess": {
			reason: "No error should be returned if the role-routine grant was revoked",
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error { return nil },
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("test-example"),
							Role:       ptr.To("test-example"),
							Schema:     ptr.To("test-example"),
							Routines:   []v1alpha1.Routine{{Name: "test-example", Arguments: []string{"test-example"}}},
							Privileges: v1alpha1.GrantPrivileges{privAll},
						},
					},
				},
			},
			want: nil,
		},
		"RoleForeignDataWrapperSuccess": {
			reason: "No error should be returned if the role-foreign-data-wrapper grant was revoked",
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error { return nil },
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:            ptr.To("test-example"),
							Role:                ptr.To("test-example"),
							ForeignDataWrappers: []string{"test-example"},
							Privileges:          v1alpha1.GrantPrivileges{privAll},
						},
					},
				},
			},
			want: nil,
		},
		"RoleForeignServerSuccess": {
			reason: "No error should be returned if the role-foreign-server grant was revoked",
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error { return nil },
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:       ptr.To("test-example"),
							Role:           ptr.To("test-example"),
							ForeignServers: []string{"test-example"},
							Privileges:     v1alpha1.GrantPrivileges{privAll},
						},
					},
				},
			},
			want: nil,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			db := tc.fields.db
			if db == nil {
				db = mockDB{}
			}
			e := external{db: db}
			_, err := e.Delete(tc.args.ctx, tc.args.mg)
			if diff := cmp.Diff(tc.want, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Delete(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
		})
	}
}

const (
	privSelect  = "SELECT"
	privExecute = "EXECUTE"
	privAll     = "ALL"
	objMyTable  = "mytable"
)

// TestGrantSQL validates the SQL strings generated by selectGrantQuery,
// createGrantQueries, and deleteGrantQuery, verifying that object names are
// properly quoted and ACL column references are qualified with their table alias.
func TestGrantSQL(t *testing.T) {
	cases := map[string]struct {
		reason                string
		gp                    v1alpha1.GrantParameters
		serverVersion         int
		wantSelectContains    []string
		wantSelectNotContains []string
		wantRevoke            string
		wantGrant             string
		wantGrantContains     []string
		wantGrantNotContains  []string
		wantDelete            string
	}{
		"SchemaSelectQueryUsesQualifiedACL": {
			reason: "aclexplode must reference n.nspacl to avoid scoping issues with JOIN precedence",
			gp: v1alpha1.GrantParameters{
				Database:   ptr.To("mydb"),
				Schema:     ptr.To("myschema"),
				Role:       ptr.To("myrole"),
				Privileges: v1alpha1.GrantPrivileges{"USAGE"},
			},
			wantSelectContains: []string{"aclexplode(n.nspacl)"},
		},
		"DatabaseSelectQueryUsesQualifiedACL": {
			reason: "aclexplode must reference db.datacl to avoid scoping issues with JOIN precedence",
			gp: v1alpha1.GrantParameters{
				Database:   ptr.To("mydb"),
				Role:       ptr.To("myrole"),
				Privileges: v1alpha1.GrantPrivileges{"CONNECT"},
			},
			wantSelectContains: []string{"aclexplode(db.datacl)"},
		},
		"RoutineSelectQueryDoesNotCrossJoinArgsWithACL": {
			// Joining unnest(p.proargtypes) into the outer query crosses one row
			// per ARGUMENT with aclexplode()'s one row per PRIVILEGE, so
			// array_agg(acl.privilege_type) collects one entry per argument.
			// EXECUTE is the only privilege a routine holds, so the HAVING
			// equality against ARRAY['EXECUTE'] holds only at a single argument.
			// Verified on PostgreSQL 16: 0- and 1-argument routines are observed,
			// 2- and 9-argument routines never are. Observe then reports
			// ResourceExists=false forever while the GRANT itself succeeds.
			//
			// This asserts the shape of the query, not its result: only a real
			// server can execute it. See the multi-argument routine Grant in
			// examples/cluster/postgresql/grant.yaml, which fails to become Ready
			// in e2e if the cross join comes back.
			reason: "unnest(p.proargtypes) must be a correlated subquery, not joined into the ACL aggregation",
			gp: v1alpha1.GrantParameters{
				Database:   ptr.To("mydb"),
				Schema:     ptr.To("myschema"),
				Role:       ptr.To("myrole"),
				Privileges: v1alpha1.GrantPrivileges{privExecute},
				Routines:   []v1alpha1.Routine{{Name: "myfunc", Arguments: []string{"text", "int4"}}},
			},
			wantSelectContains: []string{
				"FROM unnest(p.proargtypes) WITH ORDINALITY AS a(t, ord)",
				// Without the argument rows, proname/proargtypes group only via
				// pg_proc's primary-key functional dependency. Spell them out.
				"GROUP BY n.nspname, s.rolname, acl.is_grantable, p.oid, p.proname, p.proargtypes",
			},
			wantSelectNotContains: []string{"LEFT JOIN unnest(p.proargtypes)"},
		},
		"ColumnNamesAreQuoted": {
			reason: "Column names must be double-quoted to prevent SQL injection",
			gp: v1alpha1.GrantParameters{
				Database:   ptr.To("mydb"),
				Schema:     ptr.To("myschema"),
				Tables:     []string{"mytable"},
				Columns:    []string{"mycol"},
				Role:       ptr.To("myrole"),
				Privileges: v1alpha1.GrantPrivileges{"SELECT"},
			},
			wantRevoke: `REVOKE SELECT("mycol") ON TABLE "myschema"."mytable" FROM "myrole"`,
			wantGrant:  `GRANT SELECT("mycol") ON TABLE "myschema"."mytable" TO "myrole" `,
			wantDelete: `REVOKE SELECT("mycol") ON TABLE "myschema"."mytable" FROM "myrole"`,
		},
		"ForeignDataWrapperNamesAreQuoted": {
			reason: "Foreign data wrapper names must be double-quoted to prevent SQL injection",
			gp: v1alpha1.GrantParameters{
				Database:            ptr.To("mydb"),
				ForeignDataWrappers: []string{"myfdw"},
				Role:                ptr.To("myrole"),
				Privileges:          v1alpha1.GrantPrivileges{"USAGE"},
			},
			wantRevoke: `REVOKE USAGE ON FOREIGN DATA WRAPPER "myfdw" FROM "myrole"`,
			wantGrant:  `GRANT USAGE ON FOREIGN DATA WRAPPER "myfdw" TO "myrole" `,
			wantDelete: `REVOKE USAGE ON FOREIGN DATA WRAPPER "myfdw" FROM "myrole"`,
		},
		"ForeignServerNamesAreQuoted": {
			reason: "Foreign server names must be double-quoted to prevent SQL injection",
			gp: v1alpha1.GrantParameters{
				Database:       ptr.To("mydb"),
				ForeignServers: []string{"myserver"},
				Role:           ptr.To("myrole"),
				Privileges:     v1alpha1.GrantPrivileges{"USAGE"},
			},
			wantRevoke: `REVOKE USAGE ON FOREIGN SERVER "myserver" FROM "myrole"`,
			wantGrant:  `GRANT USAGE ON FOREIGN SERVER "myserver" TO "myrole" `,
			wantDelete: `REVOKE USAGE ON FOREIGN SERVER "myserver" FROM "myrole"`,
		},
		"TableGrantObservesAllGrantableRelkinds": {
			reason: "GRANT ... ON TABLE accepts views, partitioned tables, matviews and foreign tables, so Observe must read those relkinds back or the Grant Creates successfully and never converges",
			gp: v1alpha1.GrantParameters{
				Database:   ptr.To("mydb"),
				Schema:     ptr.To("myschema"),
				Tables:     []string{"myview"},
				Role:       ptr.To("myrole"),
				Privileges: v1alpha1.GrantPrivileges{"SELECT"},
			},
			wantSelectContains:    []string{"c.relkind IN ('r', 'p', 'v', 'm', 'f')"},
			wantSelectNotContains: []string{"c.relkind = 'r'"},
		},
		"ColumnGrantObservesAllGrantableRelkinds": {
			reason: "Column grants on views fail the same way as table grants when Observe filters to relkind 'r'",
			gp: v1alpha1.GrantParameters{
				Database:   ptr.To("mydb"),
				Schema:     ptr.To("myschema"),
				Tables:     []string{"myview"},
				Columns:    []string{"mycol"},
				Role:       ptr.To("myrole"),
				Privileges: v1alpha1.GrantPrivileges{"SELECT"},
			},
			wantSelectContains:    []string{"c.relkind IN ('r', 'p', 'v', 'm', 'f')"},
			wantSelectNotContains: []string{"c.relkind = 'r'"},
		},
		"SequenceGrantStillFiltersToSequenceRelkind": {
			reason: "The sequence query's relkind = 'S' filter is correct and must not be widened along with the table and column queries",
			gp: v1alpha1.GrantParameters{
				Database:   ptr.To("mydb"),
				Schema:     ptr.To("myschema"),
				Sequences:  []string{"myseq"},
				Role:       ptr.To("myrole"),
				Privileges: v1alpha1.GrantPrivileges{"USAGE"},
			},
			wantSelectContains: []string{"c.relkind = 'S'"},
		},
		"RoutineArgumentTypeNamesAreNotQuoted": {
			reason: "Routine argument type names must NOT be quoted: quoting bypasses PostgreSQL's grammar-level alias resolution, so the parser accepts \"int4\" but never \"integer\" -- while Observe compares against format_type(), which emits \"integer\". Quoted, no spelling satisfies both sides. Injection is bounded by the CRD's identifier-only pattern on args.",
			gp: v1alpha1.GrantParameters{
				Database:   ptr.To("mydb"),
				Schema:     ptr.To("myschema"),
				Routines:   []v1alpha1.Routine{{Name: "myfunc", Arguments: []string{"integer"}}},
				Role:       ptr.To("myrole"),
				Privileges: v1alpha1.GrantPrivileges{privExecute},
			},
			wantRevoke: `REVOKE EXECUTE ON ROUTINE "myschema"."myfunc"(integer) FROM "myrole"`,
			wantGrant:  `GRANT EXECUTE ON ROUTINE "myschema"."myfunc"(integer) TO "myrole" `,
			wantDelete: `REVOKE EXECUTE ON ROUTINE "myschema"."myfunc"(integer) FROM "myrole"`,
		},
		"RoutineSchemaAndNameAreStillQuoted": {
			reason: "Unquoting type names must not unquote the schema or routine name, which are user-supplied identifiers",
			gp: v1alpha1.GrantParameters{
				Database:   ptr.To("mydb"),
				Schema:     ptr.To("my-schema"),
				Routines:   []v1alpha1.Routine{{Name: "my-func", Arguments: []string{"text"}}},
				Role:       ptr.To("myrole"),
				Privileges: v1alpha1.GrantPrivileges{privExecute},
			},
			wantRevoke: `REVOKE EXECUTE ON ROUTINE "my-schema"."my-func"(text) FROM "myrole"`,
			wantGrant:  `GRANT EXECUTE ON ROUTINE "my-schema"."my-func"(text) TO "myrole" `,
			wantDelete: `REVOKE EXECUTE ON ROUTINE "my-schema"."my-func"(text) FROM "myrole"`,
		},
		"RoutineArgumentsUppercaseTypeNamesAreLowercased": {
			reason: "Uppercase type names like TEXT are lowercased to match the canonical spelling pg_catalog.format_type() returns, which is what Observe compares against",
			gp: v1alpha1.GrantParameters{
				Database:   ptr.To("mydb"),
				Schema:     ptr.To("myschema"),
				Routines:   []v1alpha1.Routine{{Name: "myfunc", Arguments: []string{"TEXT"}}},
				Role:       ptr.To("myrole"),
				Privileges: v1alpha1.GrantPrivileges{privExecute},
			},
			wantRevoke: `REVOKE EXECUTE ON ROUTINE "myschema"."myfunc"(text) FROM "myrole"`,
			wantGrant:  `GRANT EXECUTE ON ROUTINE "myschema"."myfunc"(text) TO "myrole" `,
			wantDelete: `REVOKE EXECUTE ON ROUTINE "myschema"."myfunc"(text) FROM "myrole"`,
		},
		"RoutineArgumentsAllowSchemaQualifiedCompositeTypes": {
			reason: "Composite types like AWS RDS's aws_commons._s3_uri_1 are schema-qualified; the CRD pattern must accept exactly one dot-separated identifier pair, and the qualified name is spliced in unquoted like any other type name",
			gp: v1alpha1.GrantParameters{
				Database:   ptr.To("mydb"),
				Schema:     ptr.To("aws_s3"),
				Routines:   []v1alpha1.Routine{{Name: "table_import_from_s3", Arguments: []string{"text", "text", "text", "aws_commons._s3_uri_1", "aws_commons._aws_credentials_1"}}},
				Role:       ptr.To("myrole"),
				Privileges: v1alpha1.GrantPrivileges{privExecute},
			},
			wantRevoke: `REVOKE EXECUTE ON ROUTINE "aws_s3"."table_import_from_s3"(text,text,text,aws_commons._s3_uri_1,aws_commons._aws_credentials_1) FROM "myrole"`,
			wantGrant:  `GRANT EXECUTE ON ROUTINE "aws_s3"."table_import_from_s3"(text,text,text,aws_commons._s3_uri_1,aws_commons._aws_credentials_1) TO "myrole" `,
			wantDelete: `REVOKE EXECUTE ON ROUTINE "aws_s3"."table_import_from_s3"(text,text,text,aws_commons._s3_uri_1,aws_commons._aws_credentials_1) FROM "myrole"`,
		},
		"TableWildcardTargetsEveryTableInSchema": {
			// REVOKE ALL, not REVOKE SELECT: revoking only what is about to be
			// granted leaves every other privilege in place, so Observe's exact
			// set comparison never matches and Create re-runs forever.
			reason: `tables: ["*"] must emit ON ALL TABLES IN SCHEMA, not a quoted "schema"."*" identifier`,
			gp: v1alpha1.GrantParameters{
				Database:   ptr.To("mydb"),
				Schema:     ptr.To("myschema"),
				Tables:     []string{"*"},
				Role:       ptr.To("myrole"),
				Privileges: v1alpha1.GrantPrivileges{privSelect},
			},
			wantRevoke: `REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA "myschema" FROM "myrole"`,
			wantGrant:  `GRANT SELECT ON ALL TABLES IN SCHEMA "myschema" TO "myrole" `,
			wantDelete: `REVOKE ALL PRIVILEGES ON ALL TABLES IN SCHEMA "myschema" FROM "myrole"`,
			// Counting the schema, not the list, is what re-runs Create when a
			// table appears later.
			wantSelectContains: []string{"SELECT COUNT(*) > 0 AND COUNT(*) =", "SELECT COUNT(*) FROM pg_class ac", "AND an.nspname=$1"},
			// Filtering by name would compare against the literal table "*".
			wantSelectNotContains: []string{"c.relname = ANY("},
		},
		"SequenceWildcardTargetsEverySequenceInSchema": {
			reason: `sequences: ["*"] must emit ON ALL SEQUENCES IN SCHEMA`,
			gp: v1alpha1.GrantParameters{
				Database:   ptr.To("mydb"),
				Schema:     ptr.To("myschema"),
				Sequences:  []string{"*"},
				Role:       ptr.To("myrole"),
				Privileges: v1alpha1.GrantPrivileges{privSelect},
			},
			wantRevoke:            `REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA "myschema" FROM "myrole"`,
			wantGrant:             `GRANT SELECT ON ALL SEQUENCES IN SCHEMA "myschema" TO "myrole" `,
			wantDelete:            `REVOKE ALL PRIVILEGES ON ALL SEQUENCES IN SCHEMA "myschema" FROM "myrole"`,
			wantSelectContains:    []string{"SELECT COUNT(*) > 0 AND COUNT(*) =", "SELECT COUNT(*) FROM pg_class ac", "ac.relkind = 'S'"},
			wantSelectNotContains: []string{"c.relname = ANY("},
		},
		"RoutineWildcardTargetsEveryRoutineInSchema": {
			// No signature to compare, rather than formatting "*"().
			reason: `routines: [{name: "*"}] must emit ON ALL ROUTINES IN SCHEMA`,
			gp: v1alpha1.GrantParameters{
				Database:   ptr.To("mydb"),
				Schema:     ptr.To("myschema"),
				Routines:   []v1alpha1.Routine{{Name: "*"}},
				Role:       ptr.To("myrole"),
				Privileges: v1alpha1.GrantPrivileges{privExecute},
			},
			wantRevoke:            `REVOKE ALL PRIVILEGES ON ALL ROUTINES IN SCHEMA "myschema" FROM "myrole"`,
			wantGrant:             `GRANT EXECUTE ON ALL ROUTINES IN SCHEMA "myschema" TO "myrole" `,
			wantDelete:            `REVOKE ALL PRIVILEGES ON ALL ROUTINES IN SCHEMA "myschema" FROM "myrole"`,
			wantSelectContains:    []string{"SELECT COUNT(*) > 0 AND COUNT(*) =", "SELECT COUNT(*) FROM pg_proc ap"},
			wantSelectNotContains: []string{"sub.signature = ANY("},
		},
		"RoutineWildcardCountsRoutinesWithNoACL": {
			// A routine that has never been granted on has proacl NULL, so
			// aclexplode yields no rows for it. It must still be counted on the
			// expected side: that is the mismatch that reports
			// ResourceExists=false and re-runs the GRANT for routines created
			// after it. Filtering the expected count to proacl IS NOT NULL
			// makes such routines invisible to Observe, so the resource reports
			// Ready while they hold no grant. Verified on PostgreSQL 18.6.
			reason: "the expected count must not be filtered by proacl, or routines created after the GRANT never receive it",
			gp: v1alpha1.GrantParameters{
				Database:   ptr.To("mydb"),
				Schema:     ptr.To("myschema"),
				Routines:   []v1alpha1.Routine{{Name: "*"}},
				Role:       ptr.To("myrole"),
				Privileges: v1alpha1.GrantPrivileges{privExecute},
			},
			wantSelectNotContains: []string{"proacl IS NOT NULL"},
		},
		"WildcardCarriesWithGrantOption": {
			reason: "withOption must reach both the emitted GRANT and the is_grantable filter Observe compares on",
			gp: v1alpha1.GrantParameters{
				Database:   ptr.To("mydb"),
				Schema:     ptr.To("myschema"),
				Tables:     []string{"*"},
				Role:       ptr.To("myrole"),
				Privileges: v1alpha1.GrantPrivileges{privSelect},
				WithOption: func() *v1alpha1.GrantOption { o := v1alpha1.GrantOptionGrant; return &o }(),
			},
			wantGrant: `GRANT SELECT ON ALL TABLES IN SCHEMA "myschema" TO "myrole" WITH GRANT OPTION`,
		},
		"WildcardAllPrivilegesExcludesMaintainBeforePG17": {
			// Asserted as substrings, not an exact string: ExpandPrivileges
			// iterates a map, so the privilege order varies between calls.
			reason:        "MAINTAIN arrived in PG17, so expanding ALL on an older server must not emit it on the wildcard path either",
			serverVersion: 150000,
			gp: v1alpha1.GrantParameters{
				Database:   ptr.To("mydb"),
				Schema:     ptr.To("myschema"),
				Tables:     []string{"*"},
				Role:       ptr.To("myrole"),
				Privileges: v1alpha1.GrantPrivileges{privAll},
			},
			wantGrantContains:     []string{"SELECT", "TRUNCATE", `ON ALL TABLES IN SCHEMA "myschema"`},
			wantGrantNotContains:  []string{"MAINTAIN"},
			wantSelectNotContains: []string{"MAINTAIN"},
		},
		"WildcardAllPrivilegesIncludesMaintainOnPG17": {
			reason:        "The counterpart: the gate must not swallow MAINTAIN on a server that has it",
			serverVersion: 170000,
			gp: v1alpha1.GrantParameters{
				Database:   ptr.To("mydb"),
				Schema:     ptr.To("myschema"),
				Tables:     []string{"*"},
				Role:       ptr.To("myrole"),
				Privileges: v1alpha1.GrantPrivileges{privAll},
			},
			wantGrantContains: []string{"MAINTAIN"},
		},
		"NamedTablesAreUnaffectedByWildcardSupport": {
			// The named form keeps revoking only the listed privileges: it
			// coexists with column grants on the same table.
			reason: "The wildcard branch must not disturb the explicit object-list form, which stays the default path",
			gp: v1alpha1.GrantParameters{
				Database:   ptr.To("mydb"),
				Schema:     ptr.To("myschema"),
				Tables:     []string{objMyTable},
				Role:       ptr.To("myrole"),
				Privileges: v1alpha1.GrantPrivileges{privSelect},
			},
			wantRevoke: `REVOKE SELECT ON TABLE "myschema"."mytable" FROM "myrole"`,
			wantGrant:  `GRANT SELECT ON TABLE "myschema"."mytable" TO "myrole" `,
			wantDelete: `REVOKE SELECT ON TABLE "myschema"."mytable" FROM "myrole"`,
			// The placeholder index is incidental.
			wantSelectContains:    []string{"c.relname = ANY(", "cardinality("},
			wantSelectNotContains: []string{"ON ALL TABLES IN SCHEMA", "FROM pg_class ac"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			if len(tc.wantSelectContains) > 0 || len(tc.wantSelectNotContains) > 0 {
				var q xsql.Query
				if err := selectGrantQueryWithVersion(tc.gp, &q, tc.serverVersion); err != nil {
					t.Fatalf("selectGrantQuery: %v", err)
				}
				for _, want := range tc.wantSelectContains {
					if !strings.Contains(q.String, want) {
						t.Errorf("%s\nwant query to contain %q\ngot: %s", tc.reason, want, q.String)
					}
				}
				for _, notWant := range tc.wantSelectNotContains {
					if strings.Contains(q.String, notWant) {
						t.Errorf("%s\nwant query NOT to contain %q\ngot: %s", tc.reason, notWant, q.String)
					}
				}
			}

			if tc.wantRevoke != "" || tc.wantGrant != "" || len(tc.wantGrantContains) > 0 || len(tc.wantGrantNotContains) > 0 {
				var ql []xsql.Query
				if err := createGrantQueriesWithVersion(tc.gp, &ql, tc.serverVersion); err != nil {
					t.Fatalf("createGrantQueries: %v", err)
				}
				if len(ql) < 2 {
					t.Fatalf("expected at least 2 queries, got %d", len(ql))
				}
				if tc.wantRevoke != "" {
					if diff := cmp.Diff(tc.wantRevoke, ql[0].String); diff != "" {
						t.Errorf("%s\ncreateGrantQueries REVOKE (-want +got):\n%s", tc.reason, diff)
					}
				}
				if tc.wantGrant != "" {
					if diff := cmp.Diff(tc.wantGrant, ql[1].String); diff != "" {
						t.Errorf("%s\ncreateGrantQueries GRANT (-want +got):\n%s", tc.reason, diff)
					}
				}
				for _, want := range tc.wantGrantContains {
					if !strings.Contains(ql[1].String, want) {
						t.Errorf("%s\nwant GRANT to contain %q\ngot: %s", tc.reason, want, ql[1].String)
					}
				}
				for _, notWant := range tc.wantGrantNotContains {
					if strings.Contains(ql[1].String, notWant) {
						t.Errorf("%s\nwant GRANT NOT to contain %q\ngot: %s", tc.reason, notWant, ql[1].String)
					}
				}
			}

			if tc.wantDelete != "" {
				var q xsql.Query
				if err := deleteGrantQuery(tc.gp, &q); err != nil {
					t.Fatalf("deleteGrantQuery: %v", err)
				}
				if diff := cmp.Diff(tc.wantDelete, q.String); diff != "" {
					t.Errorf("%s\ndeleteGrantQuery (-want +got):\n%s", tc.reason, diff)
				}
			}
		})
	}
}

// TestRelationGrantQueryParameters guards the seam that sharing one query
// string between the named and wildcard forms introduces: placeholders and the
// parameter slice are positional and independent, so a mismatch survives every
// string assertion and only fails against a real server.
func TestRelationGrantQueryParameters(t *testing.T) {
	placeholder := regexp.MustCompile(`\$(\d+)`)

	cases := map[string]struct {
		reason        string
		gp            v1alpha1.GrantParameters
		wantParams    int
		wantGrantable bool
	}{
		"NamedTablesBindTheObjectList": {
			reason: "The named form binds schema, role, grantable, privileges and the object list",
			gp: v1alpha1.GrantParameters{
				Database:   ptr.To("mydb"),
				Schema:     ptr.To("myschema"),
				Tables:     []string{objMyTable},
				Role:       ptr.To("myrole"),
				Privileges: v1alpha1.GrantPrivileges{privSelect},
			},
			wantParams: 5,
		},
		"WildcardTablesBindNoObjectList": {
			reason: "The wildcard form has no object list to bind, so it must not leave a dangling placeholder",
			gp: v1alpha1.GrantParameters{
				Database:   ptr.To("mydb"),
				Schema:     ptr.To("myschema"),
				Tables:     []string{"*"},
				Role:       ptr.To("myrole"),
				Privileges: v1alpha1.GrantPrivileges{privSelect},
			},
			wantParams: 4,
		},
		"WildcardSequencesBindNoObjectList": {
			reason: "Sequences share the same builder, so they share the same contract",
			gp: v1alpha1.GrantParameters{
				Database:   ptr.To("mydb"),
				Schema:     ptr.To("myschema"),
				Sequences:  []string{"*"},
				Role:       ptr.To("myrole"),
				Privileges: v1alpha1.GrantPrivileges{privSelect},
			},
			wantParams: 4,
		},
		"WildcardWithGrantOptionBindsGrantableTrue": {
			reason: "withOption: GRANT must reach $3, or Observe compares against the wrong half of the ACL",
			gp: v1alpha1.GrantParameters{
				Database:   ptr.To("mydb"),
				Schema:     ptr.To("myschema"),
				Tables:     []string{"*"},
				Role:       ptr.To("myrole"),
				Privileges: v1alpha1.GrantPrivileges{privSelect},
				WithOption: func() *v1alpha1.GrantOption { o := v1alpha1.GrantOptionGrant; return &o }(),
			},
			wantParams:    4,
			wantGrantable: true,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var q xsql.Query
			if err := selectGrantQueryWithVersion(tc.gp, &q, 0); err != nil {
				t.Fatalf("selectGrantQuery: %v", err)
			}

			if len(q.Parameters) != tc.wantParams {
				t.Fatalf("%s\nwant %d parameters, got %d", tc.reason, tc.wantParams, len(q.Parameters))
			}

			// PostgreSQL rejects both gaps and overruns.
			seen := map[int]bool{}
			for _, m := range placeholder.FindAllStringSubmatch(q.String, -1) {
				n, err := strconv.Atoi(m[1])
				if err != nil {
					t.Fatalf("unparsable placeholder %q", m[0])
				}
				if n < 1 || n > len(q.Parameters) {
					t.Errorf("%s\nplaceholder $%d has no parameter (%d bound)\nquery: %s",
						tc.reason, n, len(q.Parameters), q.String)
					continue
				}
				seen[n] = true
			}
			for i := 1; i <= len(q.Parameters); i++ {
				if !seen[i] {
					t.Errorf("%s\nparameter $%d is bound but never referenced\nquery: %s",
						tc.reason, i, q.String)
				}
			}

			// Positional contract: $1 schema, $2 role, $3 grantable.
			if got := *q.Parameters[0].(*string); got != "myschema" {
				t.Errorf("%s\n$1 should be the schema, got %q", tc.reason, got)
			}
			if got := *q.Parameters[1].(*string); got != "myrole" {
				t.Errorf("%s\n$2 should be the role, got %q", tc.reason, got)
			}
			if got, ok := q.Parameters[2].(bool); !ok || got != tc.wantGrantable {
				t.Errorf("%s\n$3 should be the grantable flag %v, got %#v", tc.reason, tc.wantGrantable, q.Parameters[2])
			}
		})
	}
}
