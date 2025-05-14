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
	"sort"
	"testing"

	"github.com/crossplane-contrib/provider-sql/apis/postgresql/v1alpha1"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/lib/pq"
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
		newDB func(creds map[string][]byte, database string, sslmode string) xsql.DB
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
	goa := v1alpha1.GrantOptionAdmin
	gog := v1alpha1.GrantOptionGrant

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
		"ErrBadGrant": {
			reason: "An error should be returned if the managed resource has no identifiable grant type",
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("test-example"),
							Tables:     []string{"test-example"},
							Role:       ptr.To("test-example"),
							Privileges: v1alpha1.GrantPrivileges{"ALL"},
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
							Privileges: v1alpha1.GrantPrivileges{"ALL"},
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
							Privileges: v1alpha1.GrantPrivileges{"ALL"},
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
							Privileges: v1alpha1.GrantPrivileges{"ALL"},
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
							Privileges: v1alpha1.GrantPrivileges{"ALL"},
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
							Privileges: v1alpha1.GrantPrivileges{"ALL"},
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
							Privileges: v1alpha1.GrantPrivileges{"ALL"},
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
							Privileges: v1alpha1.GrantPrivileges{"ALL"},
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
							Privileges:          v1alpha1.GrantPrivileges{"ALL"},
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
							Privileges:     v1alpha1.GrantPrivileges{"ALL"},
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
	goa := v1alpha1.GrantOptionAdmin

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
		"ErrBadGrant": {
			reason: "An error should be returned if the managed resource has no identifiable grant type",
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("test-example"),
							Tables:     []string{"test-example"},
							Role:       ptr.To("test-example"),
							Privileges: v1alpha1.GrantPrivileges{"ALL"},
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
							Privileges: v1alpha1.GrantPrivileges{"ALL"},
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
							Privileges: v1alpha1.GrantPrivileges{"ALL"},
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
							Privileges: v1alpha1.GrantPrivileges{"ALL"},
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
							Privileges: v1alpha1.GrantPrivileges{"ALL"},
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
							Privileges: v1alpha1.GrantPrivileges{"ALL"},
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
							Privileges: v1alpha1.GrantPrivileges{"ALL"},
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
							Privileges: v1alpha1.GrantPrivileges{"ALL"},
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
							Privileges:          v1alpha1.GrantPrivileges{"ALL"},
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
							Privileges:     v1alpha1.GrantPrivileges{"ALL"},
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
		"ErrNoOp": {
			reason: "Update is a no-op, make sure we dont throw an error *Grant",
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
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
		"ErrBadGrant": {
			reason: "An error should be returned if the managed resource has no identifiable grant type",
			args: args{
				mg: &v1alpha1.Grant{
					Spec: v1alpha1.GrantSpec{
						ForProvider: v1alpha1.GrantParameters{
							Database:   ptr.To("test-example"),
							Tables:     []string{"test-example"},
							Role:       ptr.To("test-example"),
							Privileges: v1alpha1.GrantPrivileges{"ALL"},
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
							Privileges: v1alpha1.GrantPrivileges{"ALL"},
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
							Privileges: v1alpha1.GrantPrivileges{"ALL"},
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
							Privileges: v1alpha1.GrantPrivileges{"ALL"},
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
							Privileges: v1alpha1.GrantPrivileges{"ALL"},
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
							Privileges: v1alpha1.GrantPrivileges{"ALL"},
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
							Privileges: v1alpha1.GrantPrivileges{"ALL"},
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
							Privileges: v1alpha1.GrantPrivileges{"ALL"},
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
							Privileges:          v1alpha1.GrantPrivileges{"ALL"},
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
							Privileges:     v1alpha1.GrantPrivileges{"ALL"},
						},
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
