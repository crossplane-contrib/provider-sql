/*
Copyright 2022 The Crossplane Authors.

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

package role

import (
	"context"
	"testing"

	"github.com/pkg/errors"

	"github.com/crossplane-contrib/provider-sql/pkg/clients/hana"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/hana/role"

	"github.com/crossplane-contrib/provider-sql/apis/hana/v1alpha1"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/resource"
	"github.com/crossplane/crossplane-runtime/pkg/test"
	"github.com/google/go-cmp/cmp"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
)

type mockClient struct {
	MockRead   func(ctx context.Context, parameters *v1alpha1.RoleParameters) (observed *v1alpha1.RoleObservation, err error)
	MockCreate func(ctx context.Context, parameters *v1alpha1.RoleParameters, args ...any) error
	MockDelete func(ctx context.Context, parameters *v1alpha1.RoleParameters) error
}

func (m mockClient) Read(ctx context.Context, parameters *v1alpha1.RoleParameters) (observed *v1alpha1.RoleObservation, err error) {
	return m.MockRead(ctx, parameters)
}

func (m mockClient) Create(ctx context.Context, parameters *v1alpha1.RoleParameters, args ...any) error {
	return m.MockCreate(ctx, parameters, args)
}

func (m mockClient) Delete(ctx context.Context, parameters *v1alpha1.RoleParameters) error {
	return m.MockDelete(ctx, parameters)
}

func TestConnect(t *testing.T) {
	errBoom := errors.New("boom")

	type fields struct {
		kube      client.Client
		usage     resource.Tracker
		newClient func(creds map[string][]byte) role.Client
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
		"ErrNotRole": {
			reason: "An error should be returned if the managed resource is not a *Role",
			args: args{
				mg: nil,
			},
			want: errors.New(errNotRole),
		},
		"ErrTrackProviderConfigUsage": {
			reason: "An error should be returned if we can't track our ProviderConfig usage",
			fields: fields{
				usage: resource.TrackerFn(func(ctx context.Context, mg resource.Managed) error { return errBoom }),
			},
			args: args{
				mg: &v1alpha1.Role{},
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
				mg: &v1alpha1.Role{
					Spec: v1alpha1.RoleSpec{
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
					// We call get to populate the Database struct, then again
					// to populate the (empty) ProviderConfig struct, resulting
					// in a ProviderConfig with a nil connection secret.
					MockGet: test.NewMockGetFn(nil),
				},
				usage: resource.TrackerFn(func(ctx context.Context, mg resource.Managed) error { return nil }),
			},
			args: args{
				mg: &v1alpha1.Role{
					Spec: v1alpha1.RoleSpec{
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
				mg: &v1alpha1.Role{
					Spec: v1alpha1.RoleSpec{
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
			e := &connector{kube: tc.fields.kube, usage: tc.fields.usage, newClient: tc.fields.newClient}
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
		client hana.QueryClient[v1alpha1.RoleParameters, v1alpha1.RoleObservation]
	}

	type args struct {
		ctx context.Context
		mg  resource.Managed
	}

	type want struct {
		c   managed.ExternalObservation
		err error
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   want
	}{
		"ErrNotRole": {
			reason: "An error should be returned if the managed resource is not a *Role",
			args: args{
				mg: nil,
			},
			want: want{
				err: errors.New(errNotRole),
			},
		},
		"ErrObserve": {
			reason: "Any errors encountered while observing the role should be returned",
			fields: fields{
				client: mockClient{
					MockRead: func(ctx context.Context, parameters *v1alpha1.RoleParameters) (observed *v1alpha1.RoleObservation, err error) {
						return nil, errBoom
					},
				},
			},
			args: args{
				mg: &v1alpha1.Role{
					Spec: v1alpha1.RoleSpec{
						ForProvider: v1alpha1.RoleParameters{
							RoleName: "DEMO_ROLE",
						},
					},
				},
			},
			want: want{
				err: errors.Wrap(errBoom, errSelectRole),
			},
		},
		"Success": {
			reason: "No error should be returned when we successfully observe a role",
			fields: fields{
				client: mockClient{
					MockRead: func(ctx context.Context, parameters *v1alpha1.RoleParameters) (observed *v1alpha1.RoleObservation, err error) {
						return &v1alpha1.RoleObservation{
							RoleName: "",
							Schema:   "",
						}, nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Role{
					Spec: v1alpha1.RoleSpec{
						ForProvider: v1alpha1.RoleParameters{
							RoleName: "DEMO_ROLE",
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
			e := external{client: tc.fields.client}
			got, err := e.Observe(tc.args.ctx, tc.args.mg)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Read(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.c, got); diff != "" {
				t.Errorf("\n%s\ne.Read(...): -want, +got:\n%s\n", tc.reason, diff)
			}
		})
	}
}

func TestCreate(t *testing.T) {
	errBoom := errors.New("boom")

	type fields struct {
		client hana.QueryClient[v1alpha1.RoleParameters, v1alpha1.RoleObservation]
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
		"ErrNotRole": {
			reason: "An error should be returned if the managed resource is not a *Role",
			args: args{
				mg: nil,
			},
			want: want{
				err: errors.New(errNotRole),
			},
		},
		"ErrCreate": {
			reason: "Any errors encountered while creating the role should be returned",
			fields: fields{
				client: mockClient{
					MockCreate: func(ctx context.Context, parameters *v1alpha1.RoleParameters, args ...any) error {
						return errBoom
					},
				},
			},
			args: args{
				mg: &v1alpha1.Role{
					Spec: v1alpha1.RoleSpec{
						ForProvider: v1alpha1.RoleParameters{
							RoleName: "DEMO_ROLE",
						},
					},
				},
			},
			want: want{
				err: errors.Wrap(errBoom, errCreateRole),
			},
		},
		"Success": {
			reason: "No error should be returned when we successfully create a role",
			fields: fields{
				client: mockClient{
					MockCreate: func(ctx context.Context, parameters *v1alpha1.RoleParameters, args ...any) error {
						return nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Role{
					Spec: v1alpha1.RoleSpec{
						ForProvider: v1alpha1.RoleParameters{
							RoleName: "DEMO_ROLE",
						},
					},
				},
			},
			want: want{
				err: nil,
				c:   managed.ExternalCreation{ConnectionDetails: managed.ConnectionDetails{}},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			e := external{client: tc.fields.client}
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

func TestDelete(t *testing.T) {
	errBoom := errors.New("boom")

	type fields struct {
		client hana.QueryClient[v1alpha1.RoleParameters, v1alpha1.RoleObservation]
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
		"ErrNotRole": {
			reason: "An error should be returned if the managed resource is not a *Role",
			args: args{
				mg: nil,
			},
			want: want{
				err: errors.New(errNotRole),
			},
		},
		"ErrDelete": {
			reason: "Any errors encountered while deleting the role should be returned",
			fields: fields{
				client: mockClient{
					MockDelete: func(ctx context.Context, parameters *v1alpha1.RoleParameters) error {
						return errBoom
					},
				},
			},
			args: args{
				mg: &v1alpha1.Role{
					Spec: v1alpha1.RoleSpec{
						ForProvider: v1alpha1.RoleParameters{
							RoleName: "DEMO_ROLE",
						},
					},
				},
			},
			want: want{
				err: errors.Wrap(errBoom, errDropRole),
			},
		},
		"Success": {
			reason: "No error should be returned when we successfully delete a role",
			fields: fields{
				client: mockClient{
					MockDelete: func(ctx context.Context, parameters *v1alpha1.RoleParameters) error {
						return nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.Role{
					Spec: v1alpha1.RoleSpec{
						ForProvider: v1alpha1.RoleParameters{
							RoleName: "DEMO_ROLE",
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
			e := external{client: tc.fields.client}
			err := e.Delete(tc.args.ctx, tc.args.mg)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Delete(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
		})
	}
}
