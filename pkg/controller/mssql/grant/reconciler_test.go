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
	"database/sql"
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/resource"
	"github.com/crossplane/crossplane-runtime/pkg/test"

	"github.com/crossplane-contrib/provider-sql/apis/mssql/v1alpha1"
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
		usage resource.Tracker
		newDB func(creds map[string][]byte, database string) xsql.DB
	}

	type args struct {
		ctx context.Context
		mg  resource.Managed
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
		"ErrTrackProviderConfigUsage": {
			reason: "An error should be returned if we can't track our ProviderConfig usage",
			fields: fields{
				usage: resource.TrackerFn(func(ctx context.Context, mg resource.Managed) error { return errBoom }),
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
				usage: resource.TrackerFn(func(ctx context.Context, mg resource.Managed) error { return nil }),
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
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
				usage: resource.TrackerFn(func(ctx context.Context, mg resource.Managed) error { return nil }),
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
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
				usage: resource.TrackerFn(func(ctx context.Context, mg resource.Managed) error { return nil }),
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
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
			e := &connector{kube: tc.fields.kube, usage: tc.fields.usage, newClient: tc.fields.newDB}
			_, err := e.Connect(tc.args.ctx, tc.args.mg)
			if diff := cmp.Diff(tc.want, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Connect(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
		})
	}
}

func TestObserve(t *testing.T) {
	errBoom := errors.New("boom")

	type fields struct {
		db xsql.DB
	}

	type args struct {
		ctx context.Context
		mg  resource.Managed
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
		"SuccessNoGrant": {
			reason: "We should return ResourceExists: false when no grant is found",
			fields: fields{
				db: mockDB{
					MockQuery: func(ctx context.Context, q xsql.Query) (*sql.Rows, error) {
						return mockRowsToSQLRows(sqlmock.NewRows([]string{})), nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database: ptr.To("test-example"),
							User:     ptr.To("test-example"),
						},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{ResourceExists: false},
			},
		},
		"ErrSelectGrant": {
			reason: "We should return any errors encountered while trying to show the grants",
			fields: fields{
				db: mockDB{
					MockQuery: func(ctx context.Context, q xsql.Query) (*sql.Rows, error) { return &sql.Rows{}, errBoom },
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database: ptr.To("test-example"),
							User:     ptr.To("test-example"),
						},
					},
				},
			},
			want: want{
				err: errors.Wrap(errBoom, errCannotGetGrants),
			},
		},
		"Success": {
			reason: "We should return no error if we can successfully get our permissions",
			fields: fields{
				db: mockDB{
					MockQuery: func(ctx context.Context, q xsql.Query) (*sql.Rows, error) {
						if strings.Contains(q.String, "sys.schemas") {
							return nil, errBoom
						}
						return mockRowsToSQLRows(
							sqlmock.NewRows(
								[]string{"Grants"},
							).AddRow("CREATE TABLE"),
						), nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:    ptr.To("success-db"),
							User:        ptr.To("success-user"),
							Permissions: v1alpha1.GrantPermissions{"CREATE TABLE"},
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
		"SuccessSchema": {
			reason: "We should return no error if we can successfully get our permissions",
			fields: fields{
				db: mockDB{
					MockQuery: func(ctx context.Context, q xsql.Query) (*sql.Rows, error) {
						if !strings.Contains(q.String, "sys.schemas") {
							return nil, errBoom
						}
						return mockRowsToSQLRows(
							sqlmock.NewRows(
								[]string{"Grants"},
							).AddRow("ALTER"),
						), nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:    ptr.To("success-db"),
							User:        ptr.To("success-user"),
							Schema:      ptr.To("success-schema"),
							Permissions: v1alpha1.GrantPermissions{"ALTER"},
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
		"SuccessDiffPermissions": {
			reason: "We should return no error if different permissions exist",
			fields: fields{
				db: mockDB{
					MockQuery: func(ctx context.Context, q xsql.Query) (*sql.Rows, error) {
						return mockRowsToSQLRows(
							sqlmock.NewRows(
								[]string{"Grants"},
							).AddRow("CREATE TABLE"),
						), nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:    ptr.To("success-db"),
							User:        ptr.To("diff-user"),
							Permissions: v1alpha1.GrantPermissions{"DELETE"},
						},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: false,
				},
			},
		},
		"SuccessManyPermissions": {
			reason: "We should return no error if there are more than one permission for a user",
			fields: fields{
				db: mockDB{
					MockQuery: func(ctx context.Context, q xsql.Query) (*sql.Rows, error) {
						return mockRowsToSQLRows(
							sqlmock.NewRows([]string{"Grants"}).
								AddRow("CREATE").
								AddRow("DELETE").
								AddRow("EVENT"),
						), nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:    ptr.To("success-db"),
							User:        ptr.To("success-user"),
							Permissions: v1alpha1.GrantPermissions{"DELETE", "CREATE"},
						},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: false,
				},
				err: nil,
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

func TestCreate(t *testing.T) {
	errBoom := errors.New("boom")

	type fields struct {
		db xsql.DB
	}

	type args struct {
		ctx context.Context
		mg  resource.Managed
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
		"ErrExec": {
			reason: "Any errors encountered while creating the grant should be returned",
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error { return errBoom },
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database: ptr.To("test-example"),
							User:     ptr.To("test-example"),
						},
					},
				},
			},
			want: want{
				err: errors.Wrap(errBoom, errGrant),
			},
		},
		"Success": {
			reason: "No error should be returned when we successfully create a grant",
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error {
						if strings.Contains(q.String, "ON SCHEMA::") {
							return errBoom
						}
						return nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:    ptr.To("test-example"),
							User:        ptr.To("test-example"),
							Permissions: v1alpha1.GrantPermissions{"DELETE", "CREATE"},
						},
					},
				},
			},
			want: want{
				err: nil,
			},
		},
		"SuccessSchema": {
			reason: "No error should be returned when we successfully create a grant",
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error {
						if !strings.Contains(q.String, "ON SCHEMA::") {
							return errBoom
						}
						return nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:    ptr.To("test-example"),
							User:        ptr.To("test-example"),
							Schema:      ptr.To("success-schema"),
							Permissions: v1alpha1.GrantPermissions{"ALTER"},
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
	errBoom := errors.New("boom")

	type fields struct {
		db xsql.DB
	}

	type args struct {
		ctx context.Context
		mg  resource.Managed
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
		"ErrNotGrant": {
			reason: "An error should be returned if the managed resource is not a *Grant",
			args: args{
				mg: nil,
			},
			want: want{
				err: errors.New(errNotGrant),
			},
		},
		"ErrExec": {
			reason: "Any errors encountered while updating the grant should be returned",
			fields: fields{
				db: &mockDB{
					MockQuery: func(ctx context.Context, q xsql.Query) (*sql.Rows, error) {
						return mockRowsToSQLRows(sqlmock.NewRows([]string{})), nil
					},
					MockExec: func(ctx context.Context, q xsql.Query) error { return errBoom },
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:    ptr.To("test-example"),
							User:        ptr.To("test-example"),
							Permissions: v1alpha1.GrantPermissions{"DELETE", "CREATE"},
						},
					},
				},
			},
			want: want{
				err: errors.Wrap(errBoom, errGrant),
			},
		},
		"Success": {
			reason: "No error should be returned when we update a grant",
			fields: fields{
				db: &mockDB{
					MockQuery: func(ctx context.Context, q xsql.Query) (*sql.Rows, error) {
						return mockRowsToSQLRows(sqlmock.NewRows([]string{})), nil
					},
					MockExec: func(ctx context.Context, q xsql.Query) error {
						if strings.Contains(q.String, "ON SCHEMA::") {
							return errBoom
						}
						if strings.Contains(q.String, "CREATE, DELETE") {
							return nil
						}
						return errBoom
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:    ptr.To("test-example"),
							User:        ptr.To("test-example"),
							Permissions: v1alpha1.GrantPermissions{"CREATE", "DELETE"},
						},
					},
				},
			},
			want: want{
				err: nil,
				c:   managed.ExternalUpdate{},
			},
		},
		"SuccessSchema": {
			reason: "No error should be returned when we update a grant",
			fields: fields{
				db: &mockDB{
					MockQuery: func(ctx context.Context, q xsql.Query) (*sql.Rows, error) {
						return mockRowsToSQLRows(sqlmock.NewRows([]string{})), nil
					},
					MockExec: func(ctx context.Context, q xsql.Query) error {
						if !strings.Contains(q.String, "ON SCHEMA::") {
							return errBoom
						}
						if strings.Contains(q.String, "ALTER") {
							return nil
						}
						return errBoom
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:    ptr.To("test-example"),
							User:        ptr.To("test-example"),
							Schema:      ptr.To("success-schema"),
							Permissions: v1alpha1.GrantPermissions{"ALTER"},
						},
					},
				},
			},
			want: want{
				err: nil,
				c:   managed.ExternalUpdate{},
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
		mg  resource.Managed
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
							Database: ptr.To("test-example"),
							User:     ptr.To("test-example"),
						},
					},
				},
			},
			want: errors.Wrap(errBoom, errRevoke),
		},
		"Success": {
			reason: "No error should be returned if the grant was revoked",
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database: ptr.To("test-example"),
							User:     ptr.To("test-example"),
						},
					},
				},
			},
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error {
						if strings.Contains(q.String, "ON SCHEMA::") {
							return errBoom
						}
						return nil
					},
				},
			},
			want: nil,
		},
		"SuccessSchema": {
			reason: "No error should be returned if the grant was revoked",
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database: ptr.To("test-example"),
							User:     ptr.To("test-example"),
							Schema:   ptr.To("success-schema"),
						},
					},
				},
			},
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error {
						if !strings.Contains(q.String, "ON SCHEMA::") {
							return errBoom
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
			err := e.Delete(tc.args.ctx, tc.args.mg)
			if diff := cmp.Diff(tc.want, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Delete(...): -want error, +got error:\n%s\n", tc.reason, diff)
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

func Test_diffPermissions(t *testing.T) {
	type args struct {
		desired  []string
		observed []string
	}
	type want struct {
		toGrant  []string
		toRevoke []string
	}
	cases := map[string]struct {
		args
		want
	}{
		"AsDesired": {
			args: args{
				desired:  []string{"CREATE TABLE", "DELETE"},
				observed: []string{"CREATE TABLE", "DELETE"},
			},
			want: want{
				toGrant:  nil,
				toRevoke: nil,
			},
		},
		"AsDesiredOrderNotMatter": {
			args: args{
				desired:  []string{"CREATE TABLE", "DELETE"},
				observed: []string{"DELETE", "CREATE TABLE"},
			},
			want: want{
				toGrant:  nil,
				toRevoke: nil,
			},
		},
		"NeedsGrant": {
			args: args{
				desired:  []string{"CREATE TABLE", "DELETE"},
				observed: []string{"CREATE TABLE"},
			},
			want: want{
				toGrant: []string{"DELETE"},
			},
		},
		"NeedsRevoke": {
			args: args{
				desired:  []string{"CREATE TABLE"},
				observed: []string{"CREATE TABLE", "DELETE"},
			},
			want: want{
				toRevoke: []string{"DELETE"},
			},
		},
		"NeedsBoth": {
			args: args{
				desired:  []string{"CREATE TABLE"},
				observed: []string{"DELETE"},
			},
			want: want{
				toGrant:  []string{"CREATE TABLE"},
				toRevoke: []string{"DELETE"},
			},
		},
		"GrantAll": {
			args: args{
				desired: []string{"CREATE TABLE", "DELETE", "INSERT"},
			},
			want: want{
				toGrant: []string{"CREATE TABLE", "DELETE", "INSERT"},
			},
		},
		"RevokeAll": {
			args: args{
				observed: []string{"CREATE TABLE", "DELETE", "INSERT"},
			},
			want: want{
				toRevoke: []string{"CREATE TABLE", "DELETE", "INSERT"},
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			gotToGrant, gotToRevoke := diffPermissions(tc.desired, tc.observed)
			if diff := cmp.Diff(tc.toGrant, gotToGrant, equateSlices()); diff != "" {
				t.Errorf("\ndiffPermissions(...): -want toGrant, +got toGrant:\n%s", diff)
			}
			if diff := cmp.Diff(tc.toRevoke, gotToRevoke, equateSlices()); diff != "" {
				t.Errorf("\ndiffPermissions(...): -want toRevoke, +got toRevoke:\n%s", diff)
			}
		})
	}
}

func equateSlices() cmp.Option {
	return cmpopts.SortSlices(func(x, y string) bool {
		return x < y
	})
}
