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

package dbschema

import (
	"context"
	"github.com/crossplane-contrib/provider-sql/apis/hana/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/hana"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/hana/dbschema"
	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/resource"
	"github.com/crossplane/crossplane-runtime/pkg/test"
	"github.com/google/go-cmp/cmp"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"testing"
)

type mockClient struct {
	MockRead   func(ctx context.Context, parameters *v1alpha1.DbSchemaParameters) (observed *v1alpha1.DbSchemaObservation, err error)
	MockCreate func(ctx context.Context, parameters *v1alpha1.DbSchemaParameters, args ...any) error
	MockDelete func(ctx context.Context, parameters *v1alpha1.DbSchemaParameters) error
}

func (m mockClient) Read(ctx context.Context, parameters *v1alpha1.DbSchemaParameters) (observed *v1alpha1.DbSchemaObservation, err error) {
	return m.MockRead(ctx, parameters)
}

func (m mockClient) Create(ctx context.Context, parameters *v1alpha1.DbSchemaParameters, args ...any) error {
	return m.MockCreate(ctx, parameters, args)
}

func (m mockClient) Delete(ctx context.Context, parameters *v1alpha1.DbSchemaParameters) error {
	return m.MockDelete(ctx, parameters)
}

func TestConnect(t *testing.T) {
	errBoom := errors.New("boom")

	type fields struct {
		kube      client.Client
		usage     resource.Tracker
		newClient func(creds map[string][]byte) dbschema.Client
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
		"ErrNotSchema": {
			reason: "An error should be returned if the managed resource is not a *Schema",
			args: args{
				mg: nil,
			},
			want: errors.New(errNotDbSchema),
		},
		"ErrTrackProviderConfigUsage": {
			reason: "An error should be returned if we can't track our ProviderConfig usage",
			fields: fields{
				usage: resource.TrackerFn(func(ctx context.Context, mg resource.Managed) error { return errBoom }),
			},
			args: args{
				mg: &v1alpha1.DbSchema{},
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
				mg: &v1alpha1.DbSchema{
					Spec: v1alpha1.DbSchemaSpec{
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
				mg: &v1alpha1.DbSchema{
					Spec: v1alpha1.DbSchemaSpec{
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
				mg: &v1alpha1.DbSchema{
					Spec: v1alpha1.DbSchemaSpec{
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
		client hana.QueryClient[v1alpha1.DbSchemaParameters, v1alpha1.DbSchemaObservation]
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
		"ErrNotSchema": {
			reason: "An error should be returned if the managed resource is not a *Schema",
			args: args{
				mg: nil,
			},
			want: want{
				err: errors.New(errNotDbSchema),
			},
		},
		"ErrObserve": {
			reason: "Any errors encountered while observing the schema should be returned",
			fields: fields{
				client: mockClient{
					MockRead: func(ctx context.Context, parameters *v1alpha1.DbSchemaParameters) (observed *v1alpha1.DbSchemaObservation, err error) {
						return nil, errBoom
					},
				},
			},
			args: args{
				mg: &v1alpha1.DbSchema{
					Spec: v1alpha1.DbSchemaSpec{
						ForProvider: v1alpha1.DbSchemaParameters{
							SchemaName: "DEMO_SCHEMA",
						},
					},
				},
			},
			want: want{
				err: errors.Wrap(errBoom, errSelectSchema),
			},
		},
		"Success": {
			reason: "No error should be returned when we successfully observe a schema",
			fields: fields{
				client: mockClient{
					MockRead: func(ctx context.Context, parameters *v1alpha1.DbSchemaParameters) (observed *v1alpha1.DbSchemaObservation, err error) {
						return &v1alpha1.DbSchemaObservation{
							SchemaName: "",
							Owner:      "",
						}, nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.DbSchema{
					Spec: v1alpha1.DbSchemaSpec{
						ForProvider: v1alpha1.DbSchemaParameters{
							SchemaName: "DEMO_SCHEMA",
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
		client hana.QueryClient[v1alpha1.DbSchemaParameters, v1alpha1.DbSchemaObservation]
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
		"ErrNotSchema": {
			reason: "An error should be returned if the managed resource is not a *Schema",
			args: args{
				mg: nil,
			},
			want: want{
				err: errors.New(errNotDbSchema),
			},
		},
		"ErrCreate": {
			reason: "Any errors encountered while creating the schema should be returned",
			fields: fields{
				client: mockClient{
					MockCreate: func(ctx context.Context, parameters *v1alpha1.DbSchemaParameters, args ...any) error {
						return errBoom
					},
				},
			},
			args: args{
				mg: &v1alpha1.DbSchema{
					Spec: v1alpha1.DbSchemaSpec{
						ForProvider: v1alpha1.DbSchemaParameters{
							SchemaName: "DEMO_SCHEMA",
						},
					},
				},
			},
			want: want{
				err: errors.Wrap(errBoom, errCreateSchema),
			},
		},
		"Success": {
			reason: "No error should be returned when we successfully create a schema",
			fields: fields{
				client: mockClient{
					MockCreate: func(ctx context.Context, parameters *v1alpha1.DbSchemaParameters, args ...any) error {
						return nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.DbSchema{
					Spec: v1alpha1.DbSchemaSpec{
						ForProvider: v1alpha1.DbSchemaParameters{
							SchemaName: "DEMO_SCHEMA",
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
		client hana.QueryClient[v1alpha1.DbSchemaParameters, v1alpha1.DbSchemaObservation]
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
		"ErrNotSchema": {
			reason: "An error should be returned if the managed resource is not a *Schema",
			args: args{
				mg: nil,
			},
			want: want{
				err: errors.New(errNotDbSchema),
			},
		},
		"ErrDelete": {
			reason: "Any errors encountered while deleting the schema should be returned",
			fields: fields{
				client: mockClient{
					MockDelete: func(ctx context.Context, parameters *v1alpha1.DbSchemaParameters) error {
						return errBoom
					},
				},
			},
			args: args{
				mg: &v1alpha1.DbSchema{
					Spec: v1alpha1.DbSchemaSpec{
						ForProvider: v1alpha1.DbSchemaParameters{
							SchemaName: "DEMO_SCHEMA",
						},
					},
				},
			},
			want: want{
				err: errors.Wrap(errBoom, errDropSchema),
			},
		},
		"Success": {
			reason: "No error should be returned when we successfully delete a schema",
			fields: fields{
				client: mockClient{
					MockDelete: func(ctx context.Context, parameters *v1alpha1.DbSchemaParameters) error {
						return nil
					},
				},
			},
			args: args{
				mg: &v1alpha1.DbSchema{
					Spec: v1alpha1.DbSchemaSpec{
						ForProvider: v1alpha1.DbSchemaParameters{
							SchemaName: "DEMO_SCHEMA",
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
