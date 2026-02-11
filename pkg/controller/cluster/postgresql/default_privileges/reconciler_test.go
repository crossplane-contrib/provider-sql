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
	"database/sql"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/crossplane-contrib/provider-sql/apis/cluster/postgresql/v1alpha1"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"

	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
)

type mockDB struct {
	MockExec                 func(ctx context.Context, q xsql.Query) error
	MockExecTx               func(ctx context.Context, ql []xsql.Query) error
	MockScan                 func(ctx context.Context, q xsql.Query, dest ...interface{}) error
	MockQuery                func(ctx context.Context, q xsql.Query) (*sql.Rows, error)
	MockGetConnectionDetails func(username, password string) managed.ConnectionDetails
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

func TestConnect(t *testing.T) {
	errBoom := errors.New("boom")

	type fields struct {
		kube  client.Client
		track func(context.Context, resource.LegacyManaged) error
		newDB func(creds map[string][]byte, database string, sslmode string) xsql.DB
	}

	type args struct {
		ctx context.Context
		mg  *v1alpha1.DefaultPrivileges
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
				mg: &v1alpha1.DefaultPrivileges{},
			},
			want: errors.Wrap(errBoom, errTrackPCUsage),
		},
		"ErrGetProviderConfig": {
			reason: "An error should be returned if we can't get our ProviderConfig",
			fields: fields{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(errBoom),
				},
				track: func(ctx context.Context, mg resource.LegacyManaged) error { return nil },
			},
			args: args{
				mg: &v1alpha1.DefaultPrivileges{
					Spec: v1alpha1.DefaultPrivilegesSpec{
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
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
				track: func(ctx context.Context, mg resource.LegacyManaged) error { return nil },
			},
			args: args{
				mg: &v1alpha1.DefaultPrivileges{
					Spec: v1alpha1.DefaultPrivilegesSpec{
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
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
							o.Spec.Credentials.ConnectionSecretRef = &xpv1.SecretReference{}
						case *corev1.Secret:
							return errBoom
						}
						return nil
					}),
				},
				track: func(ctx context.Context, mg resource.LegacyManaged) error { return nil },
			},
			args: args{
				mg: &v1alpha1.DefaultPrivileges{
					Spec: v1alpha1.DefaultPrivilegesSpec{
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
						},
					},
				},
			},
			want: errors.Wrap(errBoom, errGetSecret),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := &connector{kube: tc.fields.kube, track: tc.fields.track, newDB: tc.fields.newDB}
			_, err := e.Connect(tc.args.ctx, tc.args.mg)
			if diff := cmp.Diff(tc.want, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Connect(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
		})
	}
}

func TestConnectDatabaseSelection(t *testing.T) {
	type args struct {
		mg *v1alpha1.DefaultPrivileges
	}

	type want struct {
		database string
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"UsesForProviderDatabase": {
			reason: "Connect should use forProvider.database when specified, not the ProviderConfig default",
			args: args{
				mg: &v1alpha1.DefaultPrivileges{
					Spec: v1alpha1.DefaultPrivilegesSpec{
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
						},
						ForProvider: v1alpha1.DefaultPrivilegesParameters{
							Database: ptr.To("mydb"),
						},
					},
				},
			},
			want: want{database: "mydb"},
		},
		"FallsBackToProviderConfigDefault": {
			reason: "Connect should use the ProviderConfig's DefaultDatabase when forProvider.database is nil",
			args: args{
				mg: &v1alpha1.DefaultPrivileges{
					Spec: v1alpha1.DefaultPrivilegesSpec{
						ResourceSpec: xpv1.ResourceSpec{
							ProviderConfigReference: &xpv1.Reference{},
						},
						ForProvider: v1alpha1.DefaultPrivilegesParameters{},
					},
				},
			},
			want: want{database: "default-db"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var gotDatabase string
			e := &connector{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
						switch o := obj.(type) {
						case *v1alpha1.ProviderConfig:
							o.Spec.DefaultDatabase = "default-db"
							o.Spec.Credentials.ConnectionSecretRef = &xpv1.SecretReference{}
						case *corev1.Secret:
							// Return empty secret data
						}
						return nil
					}),
				},
				track: func(ctx context.Context, mg resource.LegacyManaged) error { return nil },
				newDB: func(creds map[string][]byte, database string, sslmode string) xsql.DB {
					gotDatabase = database
					return mockDB{}
				},
			}
			_, err := e.Connect(context.Background(), tc.args.mg)
			if err != nil {
				t.Fatalf("unexpected error: %v", err)
			}
			if diff := cmp.Diff(tc.want.database, gotDatabase); diff != "" {
				t.Errorf("\n%s\ne.Connect(...) database: -want, +got:\n%s\n", tc.reason, diff)
			}
		})
	}
}

func TestObserve(t *testing.T) {
	errBoom := errors.New("boom")
	// goa := v1alpha1.GrantOptionAdmin
	gog := v1alpha1.GrantOptionGrant

	type fields struct {
		db xsql.DB
	}

	type args struct {
		ctx context.Context
		mg  *v1alpha1.DefaultPrivileges
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
		"SuccessNoGrant": {
			reason: "We should return ResourceExists: false when no default grant is found",
			fields: fields{
				db: mockDB{
					MockQuery: func(ctx context.Context, q xsql.Query) (*sql.Rows, error) {
						return mockRowsToSQLRows(sqlmock.NewRows([]string{})), nil
					},
					MockScan: func(ctx context.Context, q xsql.Query, dest ...interface{}) error {
						// Default value is empty, so we don't need to do anything here
						return nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.DefaultPrivileges{
					Spec: v1alpha1.DefaultPrivilegesSpec{
						ForProvider: v1alpha1.DefaultPrivilegesParameters{
							Database:   ptr.To("test-example"),
							Role:       ptr.To("test-example"),
							TargetRole: ptr.To("target-role"),
							ObjectType: ptr.To("TABLE"),
							Privileges: v1alpha1.GrantPrivileges{"ALL"},
						},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{ResourceExists: false},
			},
		},
		"ErrSelectGrant": {
			reason: "We should return any errors encountered while trying to show the default grant",
			fields: fields{
				db: mockDB{
					MockQuery: func(ctx context.Context, q xsql.Query) (*sql.Rows, error) {
						r := sqlmock.NewRows([]string{"PRIVILEGE"}).
							AddRow("UPDATE").
							AddRow("SELECT")
						return mockRowsToSQLRows(r), errBoom
					},
					MockScan: func(ctx context.Context, q xsql.Query, dest ...interface{}) error {
						return errBoom
					},
				},
			},
			args: args{
				mg: &v1alpha1.DefaultPrivileges{
					Spec: v1alpha1.DefaultPrivilegesSpec{
						ForProvider: v1alpha1.DefaultPrivilegesParameters{
							Database:   ptr.To("test-example"),
							Role:       ptr.To("test-example"),
							TargetRole: ptr.To("target-role"),
							ObjectType: ptr.To("TABLE"),
							Privileges: v1alpha1.GrantPrivileges{"CONNECT", "TEMPORARY"},
							WithOption: &gog,
						},
					},
				},
			},
			want: want{
				err: errors.Wrap(errBoom, errSelectDefaultPrivileges),
			},
		},
		"DefaultPrivilegesFound": {
			reason: "We should return no error if we can find the right permissions in the default grant",
			fields: fields{
				db: mockDB{
					MockQuery: func(ctx context.Context, q xsql.Query) (*sql.Rows, error) {
						r := sqlmock.NewRows([]string{"PRIVILEGE"}).
							AddRow("UPDATE").
							AddRow("SELECT")
						return mockRowsToSQLRows(r), nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.DefaultPrivileges{
					Spec: v1alpha1.DefaultPrivilegesSpec{
						ForProvider: v1alpha1.DefaultPrivilegesParameters{
							Database:   ptr.To("testdb"),
							Role:       ptr.To("testrole"),
							TargetRole: ptr.To("target-role"),
							ObjectType: ptr.To("TABLE"),
							Privileges: v1alpha1.GrantPrivileges{"SELECT", "UPDATE"},
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
		"ErrNoRole": {
			reason: "An error should be returned when role is nil",
			fields: fields{
				db: mockDB{},
			},
			args: args{
				mg: &v1alpha1.DefaultPrivileges{
					Spec: v1alpha1.DefaultPrivilegesSpec{
						ForProvider: v1alpha1.DefaultPrivilegesParameters{
							TargetRole: ptr.To("target-role"),
							ObjectType: ptr.To("table"),
							Privileges: v1alpha1.GrantPrivileges{"SELECT"},
						},
					},
				},
			},
			want: want{
				err: errors.New(errNoRole),
			},
		},
		"ErrNoTargetRole": {
			reason: "An error should be returned when targetRole is nil",
			fields: fields{
				db: mockDB{},
			},
			args: args{
				mg: &v1alpha1.DefaultPrivileges{
					Spec: v1alpha1.DefaultPrivilegesSpec{
						ForProvider: v1alpha1.DefaultPrivilegesParameters{
							Role:       ptr.To("testrole"),
							ObjectType: ptr.To("table"),
							Privileges: v1alpha1.GrantPrivileges{"SELECT"},
						},
					},
				},
			},
			want: want{
				err: errors.New(errNoTargetRole),
			},
		},
		"ErrNoObjectType": {
			reason: "An error should be returned when objectType is nil",
			fields: fields{
				db: mockDB{},
			},
			args: args{
				mg: &v1alpha1.DefaultPrivileges{
					Spec: v1alpha1.DefaultPrivilegesSpec{
						ForProvider: v1alpha1.DefaultPrivilegesParameters{
							Role:       ptr.To("testrole"),
							TargetRole: ptr.To("target-role"),
							Privileges: v1alpha1.GrantPrivileges{"SELECT"},
						},
					},
				},
			},
			want: want{
				err: errors.New(errNoObjectType),
			},
		},
		"PrivilegesMismatchTriggersRecreate": {
			reason: "When DB has different privileges than spec, ResourceExists should be false to trigger re-create",
			fields: fields{
				db: mockDB{
					MockQuery: func(ctx context.Context, q xsql.Query) (*sql.Rows, error) {
						r := sqlmock.NewRows([]string{"PRIVILEGE"}).
							AddRow("SELECT")
						return mockRowsToSQLRows(r), nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.DefaultPrivileges{
					Spec: v1alpha1.DefaultPrivilegesSpec{
						ForProvider: v1alpha1.DefaultPrivilegesParameters{
							Database:   ptr.To("testdb"),
							Role:       ptr.To("testrole"),
							TargetRole: ptr.To("target-role"),
							ObjectType: ptr.To("table"),
							Privileges: v1alpha1.GrantPrivileges{"SELECT", "UPDATE"},
						},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   false,
					ResourceUpToDate: false,
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := external{db: tc.fields.db}
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

func mockRowsToSQLRows(mockRows *sqlmock.Rows) *sql.Rows {
	db, mock, _ := sqlmock.New()
	mock.ExpectQuery("select").WillReturnRows(mockRows)
	rows, err := db.Query("select")
	if err != nil {
		println("%v", err)
		return nil
	}
	return rows
}

func TestCreate(t *testing.T) {
	errBoom := errors.New("boom")
	gog := v1alpha1.GrantOptionGrant

	type fields struct {
		db xsql.DB
	}

	type args struct {
		ctx context.Context
		mg  *v1alpha1.DefaultPrivileges
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
		"ErrExec": {
			reason: "Any errors encountered while creating the default grant should be returned",
			fields: fields{
				db: &mockDB{
					MockExecTx: func(ctx context.Context, ql []xsql.Query) error { return errBoom },
				},
			},
			args: args{
				mg: &v1alpha1.DefaultPrivileges{
					Spec: v1alpha1.DefaultPrivilegesSpec{
						ForProvider: v1alpha1.DefaultPrivilegesParameters{
							Database:   ptr.To("test-example"),
							Role:       ptr.To("test-example"),
							TargetRole: ptr.To("target-role"),
							ObjectType: ptr.To("TABLE"),
							Privileges: v1alpha1.GrantPrivileges{"ALL"},
						},
					},
				},
			},
			want: want{
				err: errors.Wrap(errBoom, errCreateDefaultPrivileges),
			},
		},
		"Success": {
			reason: "No error should be returned when we successfully create a default grant",
			fields: fields{
				db: &mockDB{
					MockExecTx: func(ctx context.Context, ql []xsql.Query) error {
						return nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.DefaultPrivileges{
					Spec: v1alpha1.DefaultPrivilegesSpec{
						ForProvider: v1alpha1.DefaultPrivilegesParameters{
							Database:   ptr.To("test-example"),
							Role:       ptr.To("test-example"),
							TargetRole: ptr.To("target-role"),
							Privileges: v1alpha1.GrantPrivileges{"SELECT", "UPDATE"},
							ObjectType: ptr.To("TABLE"),
						},
					},
				},
			},
			want: want{
				err: nil,
			},
		},
		"SuccessVerifySQL": {
			reason: "Create should execute a REVOKE followed by a GRANT in a transaction with correct role order",
			fields: fields{
				db: &mockDB{
					MockExecTx: func(ctx context.Context, ql []xsql.Query) error {
						if len(ql) != 2 {
							t.Errorf("expected 2 queries in transaction, got %d", len(ql))
							return nil
						}
						// First query: REVOKE
						if !strings.Contains(ql[0].String, "REVOKE ALL") {
							t.Errorf("first query should be REVOKE, got: %s", ql[0].String)
						}
						if !strings.Contains(ql[0].String, `FOR ROLE "target-role"`) {
							t.Errorf("REVOKE should use targetRole in FOR ROLE, got: %s", ql[0].String)
						}
						if !strings.Contains(ql[0].String, `FROM "grantee-role"`) {
							t.Errorf("REVOKE should use role in FROM, got: %s", ql[0].String)
						}
						// Second query: GRANT
						if !strings.Contains(ql[1].String, "GRANT SELECT,UPDATE") {
							t.Errorf("second query should be GRANT with privileges, got: %s", ql[1].String)
						}
						if !strings.Contains(ql[1].String, `FOR ROLE "target-role"`) {
							t.Errorf("GRANT should use targetRole in FOR ROLE, got: %s", ql[1].String)
						}
						if !strings.Contains(ql[1].String, `TO "grantee-role"`) {
							t.Errorf("GRANT should use role in TO, got: %s", ql[1].String)
						}
						if !strings.Contains(ql[1].String, `IN SCHEMA "public"`) {
							t.Errorf("GRANT should include IN SCHEMA, got: %s", ql[1].String)
						}
						if !strings.Contains(ql[1].String, "WITH GRANT OPTION") {
							t.Errorf("GRANT should include WITH GRANT OPTION, got: %s", ql[1].String)
						}
						return nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.DefaultPrivileges{
					Spec: v1alpha1.DefaultPrivilegesSpec{
						ForProvider: v1alpha1.DefaultPrivilegesParameters{
							Database:   ptr.To("testdb"),
							Role:       ptr.To("grantee-role"),
							TargetRole: ptr.To("target-role"),
							ObjectType: ptr.To("table"),
							Schema:     ptr.To("public"),
							Privileges: v1alpha1.GrantPrivileges{"SELECT", "UPDATE"},
							WithOption: &gog,
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
			e := external{db: tc.fields.db}
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
		mg  *v1alpha1.DefaultPrivileges
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
			reason: "Update is a no-op, make sure we dont throw an error *DefaultPrivileges",
			args: args{
				mg: &v1alpha1.DefaultPrivileges{
					Spec: v1alpha1.DefaultPrivilegesSpec{
						ForProvider: v1alpha1.DefaultPrivilegesParameters{
							Database:   ptr.To("test-example"),
							Role:       ptr.To("test-example"),
							Privileges: v1alpha1.GrantPrivileges{"ALL"},
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
		mg  *v1alpha1.DefaultPrivileges
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   error
	}{
		"ErrDropDefaultPrivileges": {
			reason: "Errors dropping default privileges should be returned",
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error {
						return errBoom
					},
				},
			},
			args: args{
				mg: &v1alpha1.DefaultPrivileges{
					Spec: v1alpha1.DefaultPrivilegesSpec{
						ForProvider: v1alpha1.DefaultPrivilegesParameters{
							Database:   ptr.To("test-example"),
							Role:       ptr.To("test-example"),
							Privileges: v1alpha1.GrantPrivileges{"ALL"},
							ObjectType: ptr.To("SEQUENCE"),
							TargetRole: ptr.To("target-role"),
						},
					},
				},
			},
			want: errors.Wrap(errBoom, errRevokeDefaultPrivileges),
		},
		"Success": {
			reason: "No error should be returned if the default grant was revoked",
			args: args{
				mg: &v1alpha1.DefaultPrivileges{
					Spec: v1alpha1.DefaultPrivilegesSpec{
						ForProvider: v1alpha1.DefaultPrivilegesParameters{
							Database:   ptr.To("test-example"),
							Role:       ptr.To("test-example"),
							Privileges: v1alpha1.GrantPrivileges{"ALL"},
							ObjectType: ptr.To("SEQUENCE"),
							TargetRole: ptr.To("target-role"),
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
		"SuccessVerifySQL": {
			reason: "Delete should generate correct REVOKE SQL with proper role placement",
			args: args{
				mg: &v1alpha1.DefaultPrivileges{
					Spec: v1alpha1.DefaultPrivilegesSpec{
						ForProvider: v1alpha1.DefaultPrivilegesParameters{
							Database:   ptr.To("testdb"),
							Role:       ptr.To("grantee-role"),
							ObjectType: ptr.To("table"),
							TargetRole: ptr.To("target-role"),
							Schema:     ptr.To("myschema"),
						},
					},
				},
			},
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error {
						if !strings.Contains(q.String, `FOR ROLE "target-role"`) {
							t.Errorf("REVOKE should use targetRole in FOR ROLE, got: %s", q.String)
						}
						if !strings.Contains(q.String, `FROM "grantee-role"`) {
							t.Errorf("REVOKE should use role in FROM, got: %s", q.String)
						}
						if !strings.Contains(q.String, `IN SCHEMA "myschema"`) {
							t.Errorf("REVOKE should include IN SCHEMA, got: %s", q.String)
						}
						if !strings.Contains(q.String, "REVOKE ALL ON tableS") {
							t.Errorf("REVOKE should target correct object type, got: %s", q.String)
						}
						return nil
					},
				},
			},
			want: nil,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := external{db: tc.fields.db}
			_, err := e.Delete(tc.args.ctx, tc.args.mg)
			if diff := cmp.Diff(tc.want, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Delete(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
		})
	}
}
