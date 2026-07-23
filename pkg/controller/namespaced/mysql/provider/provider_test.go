package provider

import (
	"context"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	"sigs.k8s.io/controller-runtime/pkg/client"

	"github.com/crossplane/crossplane-runtime/v2/apis/common"
	xpv2 "github.com/crossplane/crossplane-runtime/v2/apis/common/v2"
	"github.com/crossplane/crossplane-runtime/v2/pkg/test"

	"github.com/crossplane-contrib/provider-sql/apis/namespaced/mysql/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/pool"
	provErrors "github.com/crossplane-contrib/provider-sql/pkg/controller/namespaced/errors"
)

func TestGetProviderConfig(t *testing.T) {
	errBoom := errors.New("boom")

	type args struct {
		kube client.Client
		mg   *v1alpha1.User
	}

	cases := map[string]struct {
		reason  string
		args    args
		want    ProviderInfo
		wantErr error
	}{
		"ErrInvalidProviderConfigKind": {
			reason: "An error should be returned if the ProviderConfig kind is not valid",
			args: args{
				mg: &v1alpha1.User{
					Spec: v1alpha1.UserSpec{
						ManagedResourceSpec: xpv2.ManagedResourceSpec{
							ProviderConfigReference: &common.ProviderConfigReference{Kind: "NotValid"},
						},
					},
				},
			},
			wantErr: provErrors.InvalidProviderConfigKindError("NotValid"),
		},
		"ErrGetProviderConfig": {
			reason: "An error should be returned if we can't get our ProviderConfig",
			args: args{
				kube: &test.MockClient{MockGet: test.NewMockGetFn(errBoom)},
				mg: &v1alpha1.User{
					ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
					Spec: v1alpha1.UserSpec{
						ManagedResourceSpec: xpv2.ManagedResourceSpec{
							ProviderConfigReference: &common.ProviderConfigReference{Kind: v1alpha1.ProviderConfigKind},
						},
					},
				},
			},
			wantErr: provErrors.GetProviderConfigError(errBoom),
		},
		"ErrGetClusterProviderConfig": {
			reason: "An error should be returned if we can't get our ClusterProviderConfig",
			args: args{
				kube: &test.MockClient{MockGet: test.NewMockGetFn(errBoom)},
				mg: &v1alpha1.User{
					ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
					Spec: v1alpha1.UserSpec{
						ManagedResourceSpec: xpv2.ManagedResourceSpec{
							ProviderConfigReference: &common.ProviderConfigReference{Kind: v1alpha1.ClusterProviderConfigKind},
						},
					},
				},
			},
			wantErr: provErrors.GetClusterProviderConfigError(errBoom),
		},
		"ErrMissingConnectionSecret": {
			reason: "An error should be returned if our ProviderConfig doesn't specify a connection secret",
			args: args{
				kube: &test.MockClient{MockGet: test.NewMockGetFn(nil)},
				mg: &v1alpha1.User{
					ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
					Spec: v1alpha1.UserSpec{
						ManagedResourceSpec: xpv2.ManagedResourceSpec{
							ProviderConfigReference: &common.ProviderConfigReference{
								Kind: v1alpha1.ProviderConfigKind,
								Name: "example",
							},
						},
					},
				},
			},
			wantErr: provErrors.MissingSecretRefError(),
		},
		"ErrGetConnectionSecret": {
			reason: "An error should be returned if we can't get our ProviderConfig's connection secret",
			args: args{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
						switch o := obj.(type) {
						case *v1alpha1.ProviderConfig:
							o.Spec.Credentials.ConnectionSecretRef = common.LocalSecretReference{Name: "example"}
						case *corev1.Secret:
							return errBoom
						}
						return nil
					}),
				},
				mg: &v1alpha1.User{
					ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
					Spec: v1alpha1.UserSpec{
						ManagedResourceSpec: xpv2.ManagedResourceSpec{
							ProviderConfigReference: &common.ProviderConfigReference{
								Kind: v1alpha1.ProviderConfigKind,
								Name: "example",
							},
						},
					},
				},
			},
			wantErr: provErrors.GetSecretError(errBoom),
		},
		"SuccessProviderConfigDefaultPool": {
			reason: "A ProviderConfig with no connectionPool block should yield the default pool config",
			args: args{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
						switch o := obj.(type) {
						case *v1alpha1.ProviderConfig:
							o.Spec.Credentials.ConnectionSecretRef = common.LocalSecretReference{Name: "example"}
						case *corev1.Secret:
							o.Data = map[string][]byte{"username": []byte("u")}
						}
						return nil
					}),
				},
				mg: &v1alpha1.User{
					ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
					Spec: v1alpha1.UserSpec{
						ManagedResourceSpec: xpv2.ManagedResourceSpec{
							ProviderConfigReference: &common.ProviderConfigReference{
								Kind: v1alpha1.ProviderConfigKind,
								Name: "example",
							},
						},
					},
				},
			},
			want: ProviderInfo{
				SecretData: map[string][]byte{"username": []byte("u")},
				PoolConfig: pool.Default,
			},
		},
		"SuccessProviderConfigCustomPool": {
			reason: "A ProviderConfig's connectionPool block should override the pool defaults",
			args: args{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
						switch o := obj.(type) {
						case *v1alpha1.ProviderConfig:
							o.Spec.Credentials.ConnectionSecretRef = common.LocalSecretReference{Name: "example"}
							o.Spec.ConnectionPool = &v1alpha1.ConnectionPool{
								MaxOpenConnections: new(7),
								MaxConnLifetime:    &metav1.Duration{Duration: 30 * time.Minute},
							}
						case *corev1.Secret:
							o.Data = map[string][]byte{"username": []byte("u")}
						}
						return nil
					}),
				},
				mg: &v1alpha1.User{
					ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
					Spec: v1alpha1.UserSpec{
						ManagedResourceSpec: xpv2.ManagedResourceSpec{
							ProviderConfigReference: &common.ProviderConfigReference{
								Kind: v1alpha1.ProviderConfigKind,
								Name: "example",
							},
						},
					},
				},
			},
			want: ProviderInfo{
				SecretData: map[string][]byte{"username": []byte("u")},
				PoolConfig: pool.Config{
					MaxOpenConns:    7,
					MaxIdleConns:    pool.Default.MaxIdleConns,
					ConnMaxLifetime: 30 * time.Minute,
					ConnMaxIdleTime: pool.Default.ConnMaxIdleTime,
				},
			},
		},
		"SuccessClusterProviderConfigCustomPool": {
			reason: "A ClusterProviderConfig's connectionPool block should override the pool defaults",
			args: args{
				kube: &test.MockClient{
					MockGet: test.NewMockGetFn(nil, func(obj client.Object) error {
						switch o := obj.(type) {
						case *v1alpha1.ClusterProviderConfig:
							o.Spec.Credentials.ConnectionSecretRef.Name = "example"
							o.Spec.Credentials.ConnectionSecretRef.Namespace = "other"
							o.Spec.ConnectionPool = &v1alpha1.ConnectionPool{MaxIdleConnections: new(2)}
						case *corev1.Secret:
							o.Data = map[string][]byte{"username": []byte("u")}
						}
						return nil
					}),
				},
				mg: &v1alpha1.User{
					ObjectMeta: metav1.ObjectMeta{Namespace: "default"},
					Spec: v1alpha1.UserSpec{
						ManagedResourceSpec: xpv2.ManagedResourceSpec{
							ProviderConfigReference: &common.ProviderConfigReference{
								Kind: v1alpha1.ClusterProviderConfigKind,
								Name: "example",
							},
						},
					},
				},
			},
			want: ProviderInfo{
				SecretData: map[string][]byte{"username": []byte("u")},
				PoolConfig: pool.Config{
					MaxOpenConns:    pool.Default.MaxOpenConns,
					MaxIdleConns:    2,
					ConnMaxLifetime: pool.Default.ConnMaxLifetime,
					ConnMaxIdleTime: pool.Default.ConnMaxIdleTime,
				},
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got, err := GetProviderConfig(context.Background(), tc.args.kube, tc.args.mg)
			if diff := cmp.Diff(tc.wantErr, err, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("\n%s\nGetProviderConfig(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
			if tc.wantErr != nil {
				return
			}
			// ProviderConfigName is always the reference name; drop it from the
			// comparison since every case above shares "example" and it's not
			// the behavior under test.
			got.ProviderConfigName = ""
			if diff := cmp.Diff(tc.want, got); diff != "" {
				t.Errorf("\n%s\nGetProviderConfig(...): -want, +got:\n%s\n", tc.reason, diff)
			}
		})
	}
}
