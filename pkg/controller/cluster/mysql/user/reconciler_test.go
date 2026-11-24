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

	"github.com/crossplane-contrib/provider-sql/apis/cluster/mysql/v1alpha1"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	v1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"k8s.io/utils/ptr"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"
	xpv2 "github.com/crossplane/crossplane/apis/v2/core/v2"

	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
)

type mockDB struct {
	MockExec             func(ctx context.Context, q xsql.Query) error
	MockExecTx           func(ctx context.Context, ql []xsql.Query) error
	MockScan             func(ctx context.Context, q xsql.Query, dest ...interface{}) error
	MockGetServerVersion func(ctx context.Context) (int, error)
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
		xpv2.CredentialsSecretUserKey:     []byte(username),
		xpv2.CredentialsSecretPasswordKey: []byte(password),
		xpv2.CredentialsSecretEndpointKey: []byte("localhost"),
		xpv2.CredentialsSecretPortKey:     []byte("3306"),
	}
}
func (m mockDB) GetServerVersion(ctx context.Context) (int, error) {
	return m.MockGetServerVersion(ctx)
}

func ptrString(s string) *string { return &s }

func TestConnect(t *testing.T) {
	errBoom := errors.New("boom")
	nopUsage := func(ctx context.Context, mg resource.LegacyManaged) error { return nil }

	type fields struct {
		kube  client.Client
		track func(context.Context, resource.LegacyManaged) error
		newDB func(creds map[string][]byte, tls *string, binlog *bool, cleartext bool, _ xsql.PasswordProvider) xsql.DB
	}

	type args struct {
		ctx context.Context
		mg  *v1alpha1.User
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
				track: nopUsage,
			},
			args: args{
				mg: &v1alpha1.User{
					Spec: v1alpha1.UserSpec{
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
					// We call get to populate the User struct, then again
					// to populate the (empty) ProviderConfig struct, resulting
					// in a ProviderConfig with a nil connection secret.
					MockGet: test.NewMockGetFn(nil),
				},
				track: nopUsage,
			},
			args: args{
				mg: &v1alpha1.User{
					Spec: v1alpha1.UserSpec{
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
				mg: &v1alpha1.User{
					Spec: v1alpha1.UserSpec{
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
			e := &connector{kube: tc.fields.kube, track: tc.fields.track, newDB: tc.fields.newDB}
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
		mg  *v1alpha1.User
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
						secret.Data[xpv2.CredentialsSecretPasswordKey] = []byte(key.Name)
						secret.DeepCopyInto(obj.(*corev1.Secret))
						return nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.User{
					Spec: v1alpha1.UserSpec{
						ForProvider: v1alpha1.UserParameters{
							PasswordSecretRef: &xpv2.SecretKeySelector{
								SecretReference: xpv2.SecretReference{
									Name: "example",
								},
								Key: "password",
							},
						},
						ClusterManagedResourceSpec: xpv2.ClusterManagedResourceSpec{
							WriteConnectionSecretToReference: &xpv2.SecretReference{
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
		"AuthenticationPluginPasswordPluginConverges": {
			reason: "When the spec requests a password-based plugin, Observe must hydrate the observed plugin so ResourceUpToDate converges \u2014 otherwise the User reconciles forever",
			fields: fields{
				db: mockDB{
					MockScan: func(ctx context.Context, q xsql.Query, dest ...interface{}) error {
						if p, ok := dest[4].(*string); ok { // dest[4] = &pluginName
							*p = "mysql_native_password"
						}
						return nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.User{
					Spec: v1alpha1.UserSpec{
						ForProvider: v1alpha1.UserParameters{
							AuthenticationPlugin: &v1alpha1.AuthenticationPlugin{
								Name: "mysql_native_password",
							},
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
		mg  *v1alpha1.User
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
						xpv2.CredentialsSecretUserKey:     []byte("example"),
						xpv2.CredentialsSecretPasswordKey: []byte(""),
						xpv2.CredentialsSecretEndpointKey: []byte("localhost"),
						xpv2.CredentialsSecretPortKey:     []byte("3306"),
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
						xpv2.CredentialsSecretUserKey:     []byte("example"),
						xpv2.CredentialsSecretPasswordKey: []byte(""),
						xpv2.CredentialsSecretEndpointKey: []byte("localhost"),
						xpv2.CredentialsSecretPortKey:     []byte("3306"),
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
							PasswordSecretRef: &xpv2.SecretKeySelector{
								SecretReference: xpv2.SecretReference{
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
						xpv2.CredentialsSecretUserKey:     []byte("example"),
						xpv2.CredentialsSecretPasswordKey: []byte("test1234"),
						xpv2.CredentialsSecretEndpointKey: []byte("localhost"),
						xpv2.CredentialsSecretPortKey:     []byte("3306"),
					},
				},
			},
		},
		"AuthenticationPluginWithAuthString": {
			reason:    "When AuthenticationPlugin is set the user must be created via IDENTIFIED WITH ... AS, with no password",
			comparePw: true,
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error {
						if !strings.HasPrefix(q.String, "CREATE USER ") {
							return errors.New("unexpected query: " + q.String)
						}
						if !strings.Contains(q.String, "IDENTIFIED WITH `AWSAuthenticationPlugin` AS 'RDS'") {
							return errors.New("expected IDENTIFIED WITH clause, got: " + q.String)
						}
						if strings.Contains(q.String, "IDENTIFIED BY") {
							return errors.New("password clause should not be emitted, got: " + q.String)
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
							AuthenticationPlugin: &v1alpha1.AuthenticationPlugin{
								Name:       "AWSAuthenticationPlugin",
								AuthString: ptrString("RDS"),
							},
						},
					},
				},
			},
			want: want{
				err: nil,
				c: managed.ExternalCreation{
					ConnectionDetails: managed.ConnectionDetails{
						xpv2.CredentialsSecretUserKey:     []byte("example"),
						xpv2.CredentialsSecretPasswordKey: []byte(""),
						xpv2.CredentialsSecretEndpointKey: []byte("localhost"),
						xpv2.CredentialsSecretPortKey:     []byte("3306"),
					},
				},
			},
		},
		"AuthenticationPluginNoAuthString": {
			reason:    "When AuthenticationPlugin has no AuthString the AS clause must be omitted",
			comparePw: true,
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error {
						if !strings.HasPrefix(q.String, "CREATE USER ") {
							return errors.New("unexpected query: " + q.String)
						}
						if !strings.Contains(q.String, "IDENTIFIED WITH `auth_socket`") {
							return errors.New("expected IDENTIFIED WITH clause, got: " + q.String)
						}
						if strings.Contains(q.String, " AS ") {
							return errors.New("AS clause should not be emitted, got: " + q.String)
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
							AuthenticationPlugin: &v1alpha1.AuthenticationPlugin{
								Name: "auth_socket",
							},
						},
					},
				},
			},
			want: want{
				err: nil,
				c: managed.ExternalCreation{
					ConnectionDetails: managed.ConnectionDetails{
						xpv2.CredentialsSecretUserKey:     []byte("example"),
						xpv2.CredentialsSecretPasswordKey: []byte(""),
						xpv2.CredentialsSecretEndpointKey: []byte("localhost"),
						xpv2.CredentialsSecretPortKey:     []byte("3306"),
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
		mg   *v1alpha1.User
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
							PasswordSecretRef: &xpv2.SecretKeySelector{
								SecretReference: xpv2.SecretReference{
									Name: "connection-secret",
								},
								Key: xpv2.CredentialsSecretPasswordKey,
							},
						},
						ClusterManagedResourceSpec: xpv2.ClusterManagedResourceSpec{
							WriteConnectionSecretToReference: &xpv2.SecretReference{
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
						secret.Data[xpv2.CredentialsSecretPasswordKey] = []byte(key.Name)
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
							PasswordSecretRef: &xpv2.SecretKeySelector{
								SecretReference: xpv2.SecretReference{
									Name: "connection-secret",
								},
								Key: xpv2.CredentialsSecretPasswordKey,
							},
						},
						ClusterManagedResourceSpec: xpv2.ClusterManagedResourceSpec{
							WriteConnectionSecretToReference: &xpv2.SecretReference{
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
						secret.Data[xpv2.CredentialsSecretPasswordKey] = []byte("samesame")
						secret.DeepCopyInto(obj.(*corev1.Secret))
						return nil
					},
				},
			},
			want: want{},
		},
		"AuthenticationPluginSkipsAlterPassword": {
			reason: "When AuthenticationPlugin is already applied (observed == desired), Update must be a no-op for the password path — no ALTER USER ... IDENTIFIED BY",
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error {
						if strings.Contains(q.String, "IDENTIFIED BY") {
							return errors.New("ALTER USER ... IDENTIFIED BY must not be run for plugin-auth users, got: " + q.String)
						}
						if strings.Contains(q.String, "IDENTIFIED WITH") {
							return errors.New("ALTER USER ... IDENTIFIED WITH must not run when observed plugin matches desired, got: " + q.String)
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
							AuthenticationPlugin: &v1alpha1.AuthenticationPlugin{
								Name:       "AWSAuthenticationPlugin",
								AuthString: ptrString("RDS"),
							},
						},
					},
					Status: v1alpha1.UserStatus{
						AtProvider: v1alpha1.UserObservation{
							AuthenticationPlugin: &v1alpha1.AuthenticationPlugin{
								Name:       "AWSAuthenticationPlugin",
								AuthString: ptrString("RDS"),
							},
						},
					},
				},
			},
			want: want{},
		},
		"AuthenticationPluginDriftPasswordToPlugin": {
			reason: "When observed has no plugin (password user) and desired has AuthenticationPlugin, Update must emit ALTER USER ... IDENTIFIED WITH",
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error {
						if !strings.Contains(q.String, "IDENTIFIED WITH `AWSAuthenticationPlugin` AS 'RDS'") {
							return errors.New("expected ALTER USER ... IDENTIFIED WITH `AWSAuthenticationPlugin` AS 'RDS', got: " + q.String)
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
							AuthenticationPlugin: &v1alpha1.AuthenticationPlugin{
								Name:       "AWSAuthenticationPlugin",
								AuthString: ptrString("RDS"),
							},
						},
					},
					// Status.AtProvider.AuthenticationPlugin intentionally nil —
					// represents an existing password-auth user that's now being
					// switched to plugin auth.
				},
			},
			want: want{
				c: managed.ExternalUpdate{
					ConnectionDetails: managed.ConnectionDetails{
						xpv2.CredentialsSecretUserKey:     []byte("example"),
						xpv2.CredentialsSecretPasswordKey: []byte(""),
						xpv2.CredentialsSecretEndpointKey: []byte("localhost"),
						xpv2.CredentialsSecretPortKey:     []byte("3306"),
					},
				},
			},
		},
		"AuthenticationPluginDriftAuthStringChange": {
			reason: "When observed plugin matches desired name but authString differs, Update must emit ALTER USER ... IDENTIFIED WITH for the new authString",
			fields: fields{
				db: &mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error {
						if !strings.Contains(q.String, "IDENTIFIED WITH `AWSAuthenticationPlugin` AS 'RDS2'") {
							return errors.New("expected ALTER USER with new authString RDS2, got: " + q.String)
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
							AuthenticationPlugin: &v1alpha1.AuthenticationPlugin{
								Name:       "AWSAuthenticationPlugin",
								AuthString: ptrString("RDS2"),
							},
						},
					},
					Status: v1alpha1.UserStatus{
						AtProvider: v1alpha1.UserObservation{
							AuthenticationPlugin: &v1alpha1.AuthenticationPlugin{
								Name:       "AWSAuthenticationPlugin",
								AuthString: ptrString("RDS"),
							},
						},
					},
				},
			},
			want: want{
				c: managed.ExternalUpdate{
					ConnectionDetails: managed.ConnectionDetails{
						xpv2.CredentialsSecretUserKey:     []byte("example"),
						xpv2.CredentialsSecretPasswordKey: []byte(""),
						xpv2.CredentialsSecretEndpointKey: []byte("localhost"),
						xpv2.CredentialsSecretPortKey:     []byte("3306"),
					},
				},
			},
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
							PasswordSecretRef: &xpv2.SecretKeySelector{
								SecretReference: xpv2.SecretReference{
									Name: "example",
								},
								Key: "password-custom",
							},
						},
						ClusterManagedResourceSpec: xpv2.ClusterManagedResourceSpec{
							WriteConnectionSecretToReference: &xpv2.SecretReference{
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
							secret.Data[xpv2.CredentialsSecretPasswordKey] = []byte("oldpassword")
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
						xpv2.CredentialsSecretUserKey:     []byte("example"),
						xpv2.CredentialsSecretPasswordKey: []byte("newpassword"),
						xpv2.CredentialsSecretEndpointKey: []byte("localhost"),
						xpv2.CredentialsSecretPortKey:     []byte("3306"),
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
							PasswordSecretRef: &xpv2.SecretKeySelector{
								SecretReference: xpv2.SecretReference{
									Name: "connection-secret",
								},
								Key: xpv2.CredentialsSecretPasswordKey,
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
						secret.Data[xpv2.CredentialsSecretPasswordKey] = []byte("samesame")
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

// scanUserLimits answers the Observe query with the given mysql.user limits,
// in the order Observe selects them: max_questions, max_updates,
// max_connections, max_user_connections.
func scanUserLimits(limits [4]int) func(ctx context.Context, q xsql.Query, dest ...interface{}) error {
	return func(_ context.Context, _ xsql.Query, dest ...interface{}) error {
		for i, v := range limits {
			*dest[i].(**int) = ptr.To(v)
		}
		return nil
	}
}

func TestChangedResourceOptions(t *testing.T) {
	// Observe records every limit it reads from mysql.user.
	observed := resourceOptionsToClauses(&v1alpha1.ResourceOptions{
		MaxQueriesPerHour:     ptr.To(0),
		MaxUpdatesPerHour:     ptr.To(100),
		MaxConnectionsPerHour: ptr.To(0),
		MaxUserConnections:    ptr.To(4),
	})

	cases := map[string]struct {
		reason   string
		existing []string
		desired  *v1alpha1.ResourceOptions
		want     []string
		err      error
	}{
		"NilSpec": {
			reason:   "No option is managed when the spec has no resourceOptions",
			existing: observed,
			desired:  nil,
			want:     nil,
		},
		"NoOptionSet": {
			reason:   "No option is managed when resourceOptions sets none of them",
			existing: observed,
			desired:  &v1alpha1.ResourceOptions{},
			want:     nil,
		},
		"OneOptionUnchanged": {
			reason:   "An option set in the spec must be compared with the same option in the observation, not with the observed clause at the same index",
			existing: observed,
			desired:  &v1alpha1.ResourceOptions{MaxUserConnections: ptr.To(4)},
			want:     nil,
		},
		"TwoOptionsUnchanged": {
			reason:   "Options set in the spec must be compared by name when the spec sets only some of them",
			existing: observed,
			desired: &v1alpha1.ResourceOptions{
				MaxUpdatesPerHour:  ptr.To(100),
				MaxUserConnections: ptr.To(4),
			},
			want: nil,
		},
		"OneOptionChanged": {
			reason:   "An option set in the spec to a new value must be reported as changed",
			existing: observed,
			desired:  &v1alpha1.ResourceOptions{MaxUserConnections: ptr.To(5)},
			want:     []string{"MAX_USER_CONNECTIONS 5"},
		},
		"AllOptionsUnchanged": {
			reason:   "No option is reported when all four match the observation",
			existing: observed,
			desired: &v1alpha1.ResourceOptions{
				MaxQueriesPerHour:     ptr.To(0),
				MaxUpdatesPerHour:     ptr.To(100),
				MaxConnectionsPerHour: ptr.To(0),
				MaxUserConnections:    ptr.To(4),
			},
			want: nil,
		},
		"AllOptionsOneChanged": {
			reason:   "Only the option whose value differs must be reported",
			existing: observed,
			desired: &v1alpha1.ResourceOptions{
				MaxQueriesPerHour:     ptr.To(0),
				MaxUpdatesPerHour:     ptr.To(100),
				MaxConnectionsPerHour: ptr.To(10),
				MaxUserConnections:    ptr.To(4),
			},
			want: []string{"MAX_CONNECTIONS_PER_HOUR 10"},
		},
		"DesiredOptionNotObserved": {
			reason:   "An option set in the spec cannot be compared when it was not observed",
			existing: nil,
			desired:  &v1alpha1.ResourceOptions{MaxUserConnections: ptr.To(4)},
			err:      errors.New(errCompareResourceOptions),
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := changedResourceOptions(tc.existing, resourceOptionsToClauses(tc.desired))
			if diff := cmp.Diff(tc.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nchangedResourceOptions(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("\n%s\nchangedResourceOptions(...): -want, +got:\n%s\n", tc.reason, diff)
			}
		})
	}
}

// TestObserveUpdateResourceOptions runs Observe and then, if Observe reports
// the User as not up to date, Update, as the managed reconciler does. The
// database already holds the given limits, so ALTER USER must only be issued
// when an option set in the spec has a different value there.
func TestObserveUpdateResourceOptions(t *testing.T) {
	cases := map[string]struct {
		reason string
		// limits are max_questions, max_updates, max_connections and
		// max_user_connections as stored in mysql.user.
		limits      [4]int
		spec        *v1alpha1.ResourceOptions
		wantQueries []string
	}{
		"NoResourceOptions": {
			reason: "No ALTER USER must be issued when the spec has no resourceOptions",
			limits: [4]int{0, 0, 0, 10},
			spec:   nil,
		},
		"OneOptionMatches": {
			reason: "No ALTER USER must be issued when the only option set in the spec already has its value",
			limits: [4]int{0, 0, 0, 4},
			spec:   &v1alpha1.ResourceOptions{MaxUserConnections: ptr.To(4)},
		},
		"UnsetOptionsAreNotManaged": {
			reason: "Options left unset in the spec must not be changed, whatever their value",
			limits: [4]int{1000, 0, 0, 4},
			spec:   &v1alpha1.ResourceOptions{MaxUserConnections: ptr.To(4)},
		},
		"AllOptionsMatch": {
			reason: "No ALTER USER must be issued when all four options already have their values",
			limits: [4]int{1000, 100, 10, 4},
			spec: &v1alpha1.ResourceOptions{
				MaxQueriesPerHour:     ptr.To(1000),
				MaxUpdatesPerHour:     ptr.To(100),
				MaxConnectionsPerHour: ptr.To(10),
				MaxUserConnections:    ptr.To(4),
			},
		},
		"OneOptionDiffers": {
			reason:      "ALTER USER must set the option set in the spec when its value differs",
			limits:      [4]int{0, 0, 0, 4},
			spec:        &v1alpha1.ResourceOptions{MaxUserConnections: ptr.To(5)},
			wantQueries: []string{"ALTER USER 'example'@'%' WITH MAX_USER_CONNECTIONS 5"},
		},
		"AllOptionsOneDiffers": {
			reason: "ALTER USER must set the options from the spec when one of them differs",
			limits: [4]int{1000, 100, 10, 4},
			spec: &v1alpha1.ResourceOptions{
				MaxQueriesPerHour:     ptr.To(1000),
				MaxUpdatesPerHour:     ptr.To(100),
				MaxConnectionsPerHour: ptr.To(20),
				MaxUserConnections:    ptr.To(4),
			},
			wantQueries: []string{"ALTER USER 'example'@'%' WITH MAX_QUERIES_PER_HOUR 1000 MAX_UPDATES_PER_HOUR 100 MAX_CONNECTIONS_PER_HOUR 20 MAX_USER_CONNECTIONS 4"},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			var queries []string
			e := external{
				db: mockDB{
					MockScan: scanUserLimits(tc.limits),
					MockExec: func(_ context.Context, q xsql.Query) error {
						queries = append(queries, q.String)
						return nil
					},
				},
			}
			mg := &v1alpha1.User{
				ObjectMeta: v1.ObjectMeta{
					Annotations: map[string]string{
						meta.AnnotationKeyExternalName: "example",
					},
				},
				Spec: v1alpha1.UserSpec{
					ForProvider: v1alpha1.UserParameters{
						ResourceOptions: tc.spec,
					},
				},
			}

			obs, err := e.Observe(context.Background(), mg)
			if err != nil {
				t.Fatalf("\n%s\ne.Observe(...): unexpected error: %v", tc.reason, err)
			}
			if !obs.ResourceUpToDate {
				if _, err := e.Update(context.Background(), mg); err != nil {
					t.Fatalf("\n%s\ne.Update(...): unexpected error: %v", tc.reason, err)
				}
			}
			if diff := cmp.Diff(tc.wantQueries, queries, cmpopts.EquateEmpty()); diff != "" {
				t.Errorf("\n%s\nexecuted queries: -want, +got:\n%s\n", tc.reason, diff)
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
		mg  *v1alpha1.User
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   error
	}{
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
			_, err := e.Delete(tc.args.ctx, tc.args.mg)
			if diff := cmp.Diff(tc.want, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Delete(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
		})
	}
}
