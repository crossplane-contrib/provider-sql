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

package user

import (
	"context"
	"database/sql"
	"strings"
	"testing"

	"github.com/crossplane-contrib/provider-sql/apis/mysql/v1alpha1"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/meta"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/resource"
	"github.com/crossplane/crossplane-runtime/pkg/test"

	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
)

type mockDB struct {
	MockExec   func(ctx context.Context, q xsql.Query) error
	MockExecTx func(ctx context.Context, ql []xsql.Query) error
	MockScan   func(ctx context.Context, q xsql.Query, dest ...interface{}) error
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
	return &sql.Rows{}, nil
}
func (m mockDB) GetConnectionDetails(username, password string) managed.ConnectionDetails {
	return managed.ConnectionDetails{
		xpv1.ResourceCredentialsSecretUserKey:     []byte(username),
		xpv1.ResourceCredentialsSecretPasswordKey: []byte(password),
		xpv1.ResourceCredentialsSecretEndpointKey: []byte("localhost"),
		xpv1.ResourceCredentialsSecretPortKey:     []byte("3306"),
	}
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
		"ErrNotUser": {
			reason: "An error should be returned if the managed resource is not a *User",
			args: args{
				mg: nil,
			},
			want: errors.New(errNotUser),
		},
		"ErrTrackProviderConfigUsage": {
			reason: "An error should be returned if we can't track our ProviderConfig usage",
			fields: fields{
				usage: resource.TrackerFn(func(ctx context.Context, mg resource.Managed) error { return errBoom }),
			},
			args: args{
				mg: &v1alpha1.User{},
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
				mg: &v1alpha1.User{
					Spec: v1alpha1.UserSpec{
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
					// We call get to populate the User struct, then again
					// to populate the (empty) ProviderConfig struct, resulting
					// in a ProviderConfig with a nil connection secret.
					MockGet: test.NewMockGetFn(nil),
				},
				usage: resource.TrackerFn(func(ctx context.Context, mg resource.Managed) error { return nil }),
			},
			args: args{
				mg: &v1alpha1.User{
					Spec: v1alpha1.UserSpec{
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
				mg: &v1alpha1.User{
					Spec: v1alpha1.UserSpec{
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
		db   xsql.DB
		kube client.Client
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
		"ErrNotUser": {
			reason: "An error should be returned if the managed resource is not a *User",
			args: args{
				mg: nil,
			},
			want: want{
				err: errors.New(errNotUser),
			},
		},
		"ErrNoUser": {
			reason: "We should return ResourceExists: false when no user is found",
			fields: fields{
				db: mockDB{
					MockScan: func(ctx context.Context, q xsql.Query, dest ...interface{}) error { return sql.ErrNoRows },
				},
			},
			args: args{
				mg: &v1alpha1.User{
					Spec: v1alpha1.UserSpec{
						ForProvider: v1alpha1.UserParameters{},
					},
				},
			},
			want: want{
				o: managed.ExternalObservation{ResourceExists: false},
			},
		},
		"ErrSelectUser": {
			reason: "We should return any errors encountered while trying to select the user",
			fields: fields{
				db: mockDB{
					MockScan: func(ctx context.Context, q xsql.Query, dest ...interface{}) error { return errBoom },
				},
			},
			args: args{
				mg: &v1alpha1.User{},
			},
			want: want{
				err: errors.Wrap(errBoom, errSelectUser),
			},
		},
		"Success": {
			reason: "We should return no error if we can successfully select our user",
			fields: fields{
				db: mockDB{
					MockScan: func(ctx context.Context, q xsql.Query, dest ...interface{}) error { return nil },
				},
			},
			args: args{
				mg: &v1alpha1.User{
					Spec: v1alpha1.UserSpec{
						ForProvider: v1alpha1.UserParameters{},
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
		"PasswordChanged": {
			reason: "We should return ResourceUpToDate=false if the password changed",
			fields: fields{
				db: mockDB{
					MockScan: func(ctx context.Context, q xsql.Query, dest ...interface{}) error { return nil },
				},
				kube: &test.MockClient{
					MockGet: func(_ context.Context, key client.ObjectKey, obj client.Object) error {
						secret := corev1.Secret{
							Data: map[string][]byte{},
						}
						secret.Data[xpv1.ResourceCredentialsSecretPasswordKey] = []byte(key.Name)
						secret.DeepCopyInto(obj.(*corev1.Secret))
						return nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.User{
					Spec: v1alpha1.UserSpec{
						ForProvider: v1alpha1.UserParameters{
							PasswordSecretRef: &xpv1.SecretKeySelector{
								SecretReference: xpv1.SecretReference{
									Name: "example",
								},
								Key: "password",
							},
						},
						ResourceSpec: xpv1.ResourceSpec{
							WriteConnectionSecretToReference: &xpv1.SecretReference{
								Name: "connection-secret",
							},
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
			e := external{
				db:   tc.fields.db,
				kube: tc.fields.kube,
			}
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
		db   xsql.DB
		kube client.Client
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
		reason    string
		comparePw bool
		fields    fields
		args      args
		want      want
	}{
		"ErrNotUser": {
			reason: "An error should be returned if the managed resource is not a *User",
			args: args{
				mg: nil,
			},
			want: want{
				err: errors.New(errNotUser),
			},
		},
		"ErrExec": {
			reason: "Any errors encountered while creating the user should be returned",
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error {
						if strings.HasPrefix(q.String, "CREATE") {
							return errBoom
						}

						return nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.User{},
			},
			want: want{
				err: errors.Wrap(errBoom, errCreateUser),
			},
		},
		"Success": {
			reason: "No error should be returned when we successfully create a user",
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error { return nil },
				},
			},
			args: args{
				mg: &v1alpha1.User{
					ObjectMeta: v1.ObjectMeta{
						Annotations: map[string]string{
							meta.AnnotationKeyExternalName: "example",
						},
					},
					Spec: v1alpha1.UserSpec{
						ForProvider: v1alpha1.UserParameters{},
					},
				},
			},
			want: want{
				err: nil,
				c: managed.ExternalCreation{
					ConnectionDetails: managed.ConnectionDetails{
						xpv1.ResourceCredentialsSecretUserKey:     []byte("example"),
						xpv1.ResourceCredentialsSecretPasswordKey: []byte(""),
						xpv1.ResourceCredentialsSecretEndpointKey: []byte("localhost"),
						xpv1.ResourceCredentialsSecretPortKey:     []byte("3306"),
					},
				},
			},
		},
		"UserWithHost": {
			reason: "The username must be split if it contains a host",
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error { return nil },
				},
			},
			args: args{
				mg: &v1alpha1.User{
					ObjectMeta: v1.ObjectMeta{
						Annotations: map[string]string{
							meta.AnnotationKeyExternalName: "example@127.0.0.1",
						},
					},
				},
			},
			want: want{
				err: nil,
				c: managed.ExternalCreation{
					ConnectionDetails: managed.ConnectionDetails{
						xpv1.ResourceCredentialsSecretUserKey:     []byte("example"),
						xpv1.ResourceCredentialsSecretPasswordKey: []byte(""),
						xpv1.ResourceCredentialsSecretEndpointKey: []byte("localhost"),
						xpv1.ResourceCredentialsSecretPortKey:     []byte("3306"),
					},
				},
			},
		},
		"UserWithPasswordRef": {
			reason:    "The password must be read from the secret",
			comparePw: true,
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error { return nil },
				},
				kube: &test.MockClient{
					MockGet: func(_ context.Context, key client.ObjectKey, obj client.Object) error {
						switch key.Name {
						case "example":
							secret := corev1.Secret{
								Data: map[string][]byte{},
							}
							secret.Data["password-custom"] = []byte("test1234")
							secret.DeepCopyInto(obj.(*corev1.Secret))
							return nil
						default:
							return nil
						}
					},
				},
			},
			args: args{
				mg: &v1alpha1.User{
					ObjectMeta: v1.ObjectMeta{
						Annotations: map[string]string{
							meta.AnnotationKeyExternalName: "example",
						},
					},
					Spec: v1alpha1.UserSpec{
						ForProvider: v1alpha1.UserParameters{
							PasswordSecretRef: &xpv1.SecretKeySelector{
								SecretReference: xpv1.SecretReference{
									Name: "example",
								},
								Key: "password-custom",
							},
						},
					},
				},
			},
			want: want{
				err: nil,
				c: managed.ExternalCreation{
					ConnectionDetails: managed.ConnectionDetails{
						xpv1.ResourceCredentialsSecretUserKey:     []byte("example"),
						xpv1.ResourceCredentialsSecretPasswordKey: []byte("test1234"),
						xpv1.ResourceCredentialsSecretEndpointKey: []byte("localhost"),
						xpv1.ResourceCredentialsSecretPortKey:     []byte("3306"),
					},
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := external{
				db:   tc.fields.db,
				kube: tc.fields.kube,
			}
			got, err := e.Create(tc.args.ctx, tc.args.mg)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Create(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
			var opts []cmp.Option
			if !tc.comparePw {
				opts = append(opts, cmpopts.IgnoreMapEntries(func(key string, _ []byte) bool { return key == "password" }))
			}
			if diff := cmp.Diff(tc.want.c, got, opts...); diff != "" {
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
		ctx  context.Context
		mg   resource.Managed
		kube client.Client
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
		"ErrNotUser": {
			reason: "An error should be returned if the managed resource is not a *User",
			args: args{
				mg: nil,
			},
			want: want{
				err: errors.New(errNotUser),
			},
		},
		"ErrExec": {
			reason: "Any errors encountered while updating the user should be returned",
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error {
						if strings.HasPrefix(q.String, "ALTER") {
							return errBoom
						}

						return nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.User{
					Spec: v1alpha1.UserSpec{
						ForProvider: v1alpha1.UserParameters{
							PasswordSecretRef: &xpv1.SecretKeySelector{
								SecretReference: xpv1.SecretReference{
									Name: "connection-secret",
								},
								Key: xpv1.ResourceCredentialsSecretPasswordKey,
							},
						},
						ResourceSpec: xpv1.ResourceSpec{
							WriteConnectionSecretToReference: &xpv1.SecretReference{
								Name: "password-secret",
							},
						},
					},
				},
				kube: &test.MockClient{
					MockGet: func(_ context.Context, key client.ObjectKey, obj client.Object) error {
						secret := corev1.Secret{
							Data: map[string][]byte{},
						}
						secret.Data[xpv1.ResourceCredentialsSecretPasswordKey] = []byte(key.Name)
						secret.DeepCopyInto(obj.(*corev1.Secret))
						return nil
					},
				},
			},
			want: want{
				err: errors.Wrap(errBoom, errUpdateUser),
			},
		},
		"Success": {
			reason: "No error should be returned when we don't have to update a user",
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error { return nil },
				},
			},
			args: args{
				mg: &v1alpha1.User{
					ObjectMeta: v1.ObjectMeta{
						Annotations: map[string]string{
							meta.AnnotationKeyExternalName: "example",
						},
					},
				},
			},
			want: want{
				err: nil,
				c:   managed.ExternalUpdate{},
			},
		},
		"SamePassword": {
			reason: "No DB query should be executed if the password didn't change",
			fields: fields{
				db: &mockDB{},
			},
			args: args{
				mg: &v1alpha1.User{
					Spec: v1alpha1.UserSpec{
						ForProvider: v1alpha1.UserParameters{
							PasswordSecretRef: &xpv1.SecretKeySelector{
								SecretReference: xpv1.SecretReference{
									Name: "connection-secret",
								},
								Key: xpv1.ResourceCredentialsSecretPasswordKey,
							},
						},
						ResourceSpec: xpv1.ResourceSpec{
							WriteConnectionSecretToReference: &xpv1.SecretReference{
								Name: "connection-secret",
							},
						},
					},
				},
				kube: &test.MockClient{
					MockGet: func(_ context.Context, key client.ObjectKey, obj client.Object) error {
						secret := corev1.Secret{
							Data: map[string][]byte{},
						}
						secret.Data[xpv1.ResourceCredentialsSecretPasswordKey] = []byte("samesame")
						secret.DeepCopyInto(obj.(*corev1.Secret))
						return nil
					},
				},
			},
			want: want{},
		},
		"UpdatePassword": {
			reason: "The password must be updated",
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error { return nil },
				},
			},
			args: args{
				mg: &v1alpha1.User{
					ObjectMeta: v1.ObjectMeta{
						Annotations: map[string]string{
							meta.AnnotationKeyExternalName: "example",
						},
					},
					Spec: v1alpha1.UserSpec{
						ForProvider: v1alpha1.UserParameters{
							PasswordSecretRef: &xpv1.SecretKeySelector{
								SecretReference: xpv1.SecretReference{
									Name: "example",
								},
								Key: "password-custom",
							},
						},
						ResourceSpec: xpv1.ResourceSpec{
							WriteConnectionSecretToReference: &xpv1.SecretReference{
								Name: "connection-secret",
							},
						},
					},
				},
				kube: &test.MockClient{
					MockGet: func(_ context.Context, key client.ObjectKey, obj client.Object) error {
						switch key.Name {
						case "example":
							secret := corev1.Secret{
								Data: map[string][]byte{},
							}
							secret.Data["password-custom"] = []byte("newpassword")
							secret.DeepCopyInto(obj.(*corev1.Secret))
							return nil
						case "connection-secret":
							secret := corev1.Secret{
								Data: map[string][]byte{},
							}
							secret.Data[xpv1.ResourceCredentialsSecretPasswordKey] = []byte("oldpassword")
							secret.DeepCopyInto(obj.(*corev1.Secret))
							return nil
						default:
							return nil
						}
					},
				},
			},
			want: want{
				err: nil,
				c: managed.ExternalUpdate{
					ConnectionDetails: managed.ConnectionDetails{
						xpv1.ResourceCredentialsSecretUserKey:     []byte("example"),
						xpv1.ResourceCredentialsSecretPasswordKey: []byte("newpassword"),
						xpv1.ResourceCredentialsSecretEndpointKey: []byte("localhost"),
						xpv1.ResourceCredentialsSecretPortKey:     []byte("3306"),
					},
				},
			},
		},
		"NoUpdateQueryUnchangedResourceOptions": {
			reason: "We should not execute an SQL query if the resource options are unchanged.",
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error {
						if strings.HasPrefix(q.String, "ALTER") {
							return errBoom
						}

						return nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.User{
					ObjectMeta: v1.ObjectMeta{
						Annotations: map[string]string{
							meta.AnnotationKeyExternalName: "example",
						},
					},
					Spec: v1alpha1.UserSpec{
						ForProvider: v1alpha1.UserParameters{
							PasswordSecretRef: &xpv1.SecretKeySelector{
								SecretReference: xpv1.SecretReference{
									Name: "connection-secret",
								},
								Key: xpv1.ResourceCredentialsSecretPasswordKey,
							},
							ResourceOptions: &v1alpha1.ResourceOptions{
								MaxQueriesPerHour:     new(int),
								MaxUpdatesPerHour:     new(int),
								MaxConnectionsPerHour: new(int),
								MaxUserConnections:    new(int),
							},
						},
					},
					Status: v1alpha1.UserStatus{
						AtProvider: v1alpha1.UserObservation{
							ResourceOptionsAsClauses: []string{
								"MAX_QUERIES_PER_HOUR 0",
								"MAX_UPDATES_PER_HOUR 0",
								"MAX_CONNECTIONS_PER_HOUR 0",
								"MAX_USER_CONNECTIONS 0",
							},
						},
					},
				},
				kube: &test.MockClient{
					MockGet: func(_ context.Context, key client.ObjectKey, obj client.Object) error {
						secret := corev1.Secret{
							Data: map[string][]byte{},
						}
						secret.Data[xpv1.ResourceCredentialsSecretPasswordKey] = []byte("samesame")
						secret.DeepCopyInto(obj.(*corev1.Secret))
						return nil
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
				db:   tc.fields.db,
				kube: tc.args.kube,
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
		"ErrNotUser": {
			reason: "An error should be returned if the managed resource is not a *User",
			args: args{
				mg: nil,
			},
			want: errors.New(errNotUser),
		},
		"ErrDropUser": {
			reason: "Errors dropping a user should be returned",
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error {
						if strings.HasPrefix(q.String, "DROP") {
							return errBoom
						}

						return nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.User{},
			},
			want: errors.Wrap(errBoom, errDropUser),
		},
		"Success": {
			reason: "No error should be returned",
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error {
						return nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.User{
					Spec: v1alpha1.UserSpec{
						ForProvider: v1alpha1.UserParameters{},
					},
				},
			},
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
