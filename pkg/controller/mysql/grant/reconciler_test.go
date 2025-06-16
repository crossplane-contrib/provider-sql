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
	"strings"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/crossplane-contrib/provider-sql/apis/mysql/v1alpha1"
	"github.com/go-sql-driver/mysql"
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
		newDB func(creds map[string][]byte, tls *string, binlog *bool) xsql.DB
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
			e := &connector{kube: tc.fields.kube, usage: tc.fields.usage, newDB: tc.fields.newDB}
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
		o                  managed.ExternalObservation
		err                error
		observedPrivileges []string
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
			reason: "We should return ResourceExists: false when no grant is found, being privileges result empty",
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
				err: errors.Wrap(errBoom, errCurrentGrant),
			},
		},
		"SuccessNoUser": {
			reason: "We should return no error if the user doesn't exist",
			fields: fields{
				db: mockDB{
					MockQuery: func(ctx context.Context, q xsql.Query) (*sql.Rows, error) {
						return mockRowsToSQLRows(
							sqlmock.NewRows([]string{"Grants"}).
								AddRow("GRANT CREATE, DROP ON `success-db`.* TO 'no-user'@%").
								RowError(0, &mysql.MySQLError{Number: errCodeNoSuchGrant}),
						), nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("success-db"),
							User:       ptr.To("no-user"),
							Privileges: v1alpha1.GrantPrivileges{"DROP", "CREATE"},
						},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{ResourceExists: false},
			},
		},
		"Success": {
			reason: "We should return no error if we can successfully show our grants",
			fields: fields{
				db: mockDB{
					MockQuery: func(ctx context.Context, q xsql.Query) (*sql.Rows, error) {
						return mockRowsToSQLRows(
							sqlmock.NewRows(
								[]string{"Grants"},
							).AddRow("GRANT " + allPrivileges + " ON `success-db`.* TO 'success-user'@%"),
						), nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("success-db"),
							User:       ptr.To("success-user"),
							Privileges: v1alpha1.GrantPrivileges{"ALL"},
						},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
				},
				err:                nil,
				observedPrivileges: []string{allPrivileges},
			},
		},
		"SuccessGrantOptionNoDatabase": {
			reason: "We should return no error if we can successfully show our grants",
			fields: fields{
				db: mockDB{
					MockQuery: func(ctx context.Context, q xsql.Query) (*sql.Rows, error) {
						return mockRowsToSQLRows(
							sqlmock.NewRows(
								[]string{"Grants"},
							).AddRow("GRANT INSERT, SELECT ON *.* TO 'success-user'@% WITH GRANT OPTION"),
						), nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							User:       ptr.To("success-user"),
							Privileges: v1alpha1.GrantPrivileges{"INSERT", "SELECT", "GRANT OPTION"},
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
				observedPrivileges: []string{
					"GRANT OPTION",
					"INSERT",
					"SELECT",
				},
			},
		},
		"SuccessGrantOptionWithDatabase": {
			reason: "We should return no error if we can successfully show our grants",
			fields: fields{
				db: mockDB{
					MockQuery: func(ctx context.Context, q xsql.Query) (*sql.Rows, error) {
						return mockRowsToSQLRows(
							sqlmock.NewRows(
								[]string{"Grants"},
							).AddRow("GRANT INSERT, SELECT ON `success-db`.* TO 'success-user'@% WITH GRANT OPTION"),
						), nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("success-db"),
							User:       ptr.To("success-user"),
							Privileges: v1alpha1.GrantPrivileges{"INSERT", "SELECT", "GRANT OPTION"},
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
				observedPrivileges: []string{
					"GRANT OPTION",
					"INSERT",
					"SELECT",
				},
			},
		},
		"SuccessDiffGrants": {
			reason: "We should return no error if different grants exist for the provided database",
			fields: fields{
				db: mockDB{
					MockQuery: func(ctx context.Context, q xsql.Query) (*sql.Rows, error) {
						return mockRowsToSQLRows(
							sqlmock.NewRows(
								[]string{"Grants"},
							).AddRow("GRANT CREATE ON `success-db`.* TO 'diff-user'@%"),
						), nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("success-db"),
							User:       ptr.To("diff-user"),
							Privileges: v1alpha1.GrantPrivileges{"DROP"},
						},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: false,
				},
				observedPrivileges: []string{"CREATE"},
			},
		},
		"SuccessDiffGrantNoDatabaseNoTable": {
			reason: "We should return no error if different grants exist and no database and table are provided",
			fields: fields{
				db: mockDB{
					MockQuery: func(ctx context.Context, q xsql.Query) (*sql.Rows, error) {
						return mockRowsToSQLRows(
							sqlmock.NewRows([]string{"Grants"}).
								AddRow("GRANT INSERT ON *.* TO 'success-user'@%"),
						), nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							User:       ptr.To("success-user"),
							Privileges: v1alpha1.GrantPrivileges{"DROP", "CREATE"},
						},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: false,
				},
				observedPrivileges: []string{"INSERT"},
				err:                nil,
			},
		},
		"SuccessDiffGrantUsage": {
			reason: "We should return ResourceExists: false when a USAGE grant is found, since it is equivalent to having no grants",
			fields: fields{
				db: mockDB{
					MockQuery: func(ctx context.Context, q xsql.Query) (*sql.Rows, error) {
						return mockRowsToSQLRows(
							sqlmock.NewRows([]string{"Grants"}).
								AddRow("GRANT USAGE ON *.* TO 'success-user'@%"),
						), nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							User:       ptr.To("success-user"),
							Privileges: v1alpha1.GrantPrivileges{"DROP", "CREATE"},
						},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists: false,
				},
				err: nil,
			},
		},
		"SuccessManyGrants": {
			reason: "We should return no error if there are more than one grant for a user",
			fields: fields{
				db: mockDB{
					MockQuery: func(ctx context.Context, q xsql.Query) (*sql.Rows, error) {
						return mockRowsToSQLRows(
							sqlmock.NewRows([]string{"Grants"}).
								AddRow("GRANT CREATE, DROP ON `success-db`.* TO 'success-user'@%").
								AddRow("GRANT EVENT ON `success-db`.* TO 'success-user'@%"),
						), nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("success-db"),
							User:       ptr.To("success-user"),
							Privileges: v1alpha1.GrantPrivileges{"DROP", "CREATE"},
						},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
				},
				err:                nil,
				observedPrivileges: []string{"CREATE", "DROP"},
			},
		},
		"SuccessGrantNoDatabaseNoTable": {
			reason: "We should return no error if no database and table were provided and grants were equal to the ones in resource spec",
			fields: fields{
				db: mockDB{
					MockQuery: func(ctx context.Context, q xsql.Query) (*sql.Rows, error) {
						return mockRowsToSQLRows(
							sqlmock.NewRows([]string{"Grants"}).
								AddRow("GRANT CREATE, DROP ON *.* TO 'success-user'@%"),
						), nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							User:       ptr.To("success-user"),
							Privileges: v1alpha1.GrantPrivileges{"DROP", "CREATE"},
						},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
				},
				err:                nil,
				observedPrivileges: []string{"CREATE", "DROP"},
			},
		},
		"SuccessGrantWithTables": {
			reason: "We should see the grants in sync when using a table",
			fields: fields{
				db: mockDB{
					MockQuery: func(ctx context.Context, q xsql.Query) (*sql.Rows, error) {
						return mockRowsToSQLRows(
							sqlmock.NewRows([]string{"Grants"}).
								AddRow("GRANT CREATE, DROP ON `success-db`.`success-table` TO 'success-user'@%"),
						), nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("success-db"),
							User:       ptr.To("success-user"),
							Table:      ptr.To("success-table"),
							Privileges: v1alpha1.GrantPrivileges{"DROP", "CREATE"},
						},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: true,
				},
				err:                nil,
				observedPrivileges: []string{"CREATE", "DROP"},
			},
		},
		"SuccessDiffGrantWithTables": {
			reason: "We should see the grants out of sync when using a table",
			fields: fields{
				db: mockDB{
					MockQuery: func(ctx context.Context, q xsql.Query) (*sql.Rows, error) {
						return mockRowsToSQLRows(
							sqlmock.NewRows([]string{"Grants"}).
								AddRow("GRANT CREATE, DROP ON `success-db`.`success-table` TO 'success-user'@%"),
						), nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("success-db"),
							User:       ptr.To("success-user"),
							Table:      ptr.To("success-table"),
							Privileges: v1alpha1.GrantPrivileges{"INSERT", "CREATE"},
						},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{
					ResourceExists:   true,
					ResourceUpToDate: false,
				},
				err:                nil,
				observedPrivileges: []string{"CREATE", "DROP"},
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

			if tc.args.mg != nil {
				cr, _ := tc.args.mg.(*v1alpha1.Grant)
				if diff := cmp.Diff(tc.want.observedPrivileges, cr.Status.AtProvider.Privileges, equateSlices()...); diff != "" {
					t.Errorf("\n%s\ne.Observe(...): -want, +got:\n%s\n", tc.reason, diff)
				}
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
					MockExec: func(ctx context.Context, q xsql.Query) error {
						if strings.HasPrefix(q.String, "GRANT") {
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
							Database: ptr.To("test-example"),
							User:     ptr.To("test-example"),
						},
					},
				},
			},
			want: want{
				err: errors.Wrap(errBoom, errCreateGrant),
			},
		},
		"Success": {
			reason: "No error should be returned when we successfully create a grant",
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
							User:       ptr.To("test-example"),
							Privileges: v1alpha1.GrantPrivileges{"INSERT", "SELECT"},
						},
					},
				},
			},
			want: want{
				err: nil,
			},
		},
		"SuccessNoDatabase": {
			reason: "No error should be returned when we successfully create a grant with no database",
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error { return nil },
				},
			},
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							User:       ptr.To("test-example"),
							Privileges: v1alpha1.GrantPrivileges{"INSERT", "SELECT"},
						},
					},
				},
			},
			want: want{
				err: nil,
			},
		},
		"SuccessGrantOption": {
			reason: "No error should be returned when we successfully create a grant with grant option",
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error {
						if strings.HasPrefix(q.String, "GRANT") &&
							!strings.HasSuffix(q.String, "WITH GRANT OPTION") {
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
							Database:   ptr.To("test-example"),
							User:       ptr.To("test-example"),
							Privileges: v1alpha1.GrantPrivileges{"GRANT OPTION", "ALL"},
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
		"ErrExecRevokeNotRequired": {
			reason: "Any errors encountered while revoking a not required privilege from the desired ones should be returned",
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error {
						if strings.HasPrefix(q.String, "REVOKE") {
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
							Database:   ptr.To("test-example"),
							User:       ptr.To("test-example"),
							Privileges: v1alpha1.GrantPrivileges{"CREATE"},
						},
					},
					Status: v1alpha1.GrantStatus{
						AtProvider: v1alpha1.GrantObservation{
							Privileges: []string{"INSERT", "CREATE"},
						},
					},
				},
			},
			want: want{
				err: errors.Wrap(errBoom, errRevokeGrant),
			},
		},
		"ErrExecGrantMissing": {
			reason: "Any errors encountered while granting a missing privilege from the desired ones should be returned",
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error {
						if strings.HasPrefix(q.String, "GRANT") {
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
							Database:   ptr.To("test-example"),
							User:       ptr.To("test-example"),
							Privileges: v1alpha1.GrantPrivileges{"CREATE", "SELECT"},
						},
					},
					Status: v1alpha1.GrantStatus{
						AtProvider: v1alpha1.GrantObservation{
							Privileges: []string{"CREATE"},
						},
					},
				},
			},
			want: want{
				err: errors.Wrap(errBoom, errCreateGrant),
			},
		},
		"SuccessEqualObservedDesired": {
			reason: "No query should be executed and no error should be returned when there is no diff between desired and observed privileges",
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
							User:       ptr.To("test-example"),
							Privileges: v1alpha1.GrantPrivileges{"CREATE", "DROP"},
						},
					},
					Status: v1alpha1.GrantStatus{
						AtProvider: v1alpha1.GrantObservation{
							Privileges: []string{"DROP", "CREATE"},
						},
					},
				},
			},
			want: want{
				err: nil,
				c:   managed.ExternalUpdate{},
			},
		},
		"SuccessDiffObservedDesiredGrantMissing": {
			reason: "No error should be returned when granting a missing privilege from the desired ones",
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error {
						if strings.HasPrefix(q.String, "REVOKE") {
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
							Database:   ptr.To("test-example"),
							User:       ptr.To("test-example"),
							Privileges: v1alpha1.GrantPrivileges{"CREATE", "DROP"},
						},
					},
					Status: v1alpha1.GrantStatus{
						AtProvider: v1alpha1.GrantObservation{
							Privileges: []string{"DROP"},
						},
					},
				},
			},
			want: want{
				err: nil,
				c:   managed.ExternalUpdate{},
			},
		},
		"SuccessDiffObservedDesiredRevokeNotRequired": {
			reason: "No error should be returned when revoking a not required privilege from the desired ones",
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error {
						if strings.HasPrefix(q.String, "GRANT") {
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
							Database:   ptr.To("test-example"),
							User:       ptr.To("test-example"),
							Privileges: v1alpha1.GrantPrivileges{"DROP"},
						},
					},
					Status: v1alpha1.GrantStatus{
						AtProvider: v1alpha1.GrantObservation{
							Privileges: []string{"DROP", "SELECT"},
						},
					},
				},
			},
			want: want{
				err: nil,
				c:   managed.ExternalUpdate{},
			},
		},
		"SuccessGrantOption": {
			reason: "No error should be returned when we successfully create a grant with grant option",
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error {
						if strings.HasPrefix(q.String, "GRANT") &&
							!strings.HasSuffix(q.String, "WITH GRANT OPTION") {
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
							Database:   ptr.To("test-example"),
							User:       ptr.To("test-example"),
							Privileges: v1alpha1.GrantPrivileges{"GRANT OPTION", "ALL"},
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
						if strings.HasPrefix(q.String, "REVOKE") {
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
							Database: ptr.To("test-example"),
							User:     ptr.To("test-example"),
						},
					},
				},
			},
			want: errors.Wrap(errBoom, errRevokeGrant),
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
					MockExec: func(ctx context.Context, q xsql.Query) error { return nil },
				},
			},
			want: nil,
		},
		"SuccessGrantGone": {
			reason: "No error should be returned if the grant is already revoked",
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
						if strings.HasPrefix(q.String, "REVOKE") {
							return &mysql.MySQLError{Number: errCodeNoSuchGrant}
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
			if diff := cmp.Diff(tc.toGrant, gotToGrant, equateSlices()...); diff != "" {
				t.Errorf("\ndiffPermissions(...): -want toGrant, +got toGrant:\n%s", diff)
			}
			if diff := cmp.Diff(tc.toRevoke, gotToRevoke, equateSlices()...); diff != "" {
				t.Errorf("\ndiffPermissions(...): -want toRevoke, +got toRevoke:\n%s", diff)
			}
		})
	}
}

func equateSlices() []cmp.Option {
	return []cmp.Option{
		cmp.Transformer("mapAllPrivileges", func(s string) string {
			if s == "ALL PRIVILEGES" {
				return "ALL"
			}
			return s
		}),
		cmpopts.SortSlices(func(x, y string) bool {
			return x < y
		}),
	}
}
