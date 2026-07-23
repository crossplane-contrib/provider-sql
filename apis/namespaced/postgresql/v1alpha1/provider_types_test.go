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

package v1alpha1

import (
	"testing"
	"time"

	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"

	"github.com/crossplane-contrib/provider-sql/pkg/clients/pool"
)

func durPtr(d time.Duration) *metav1.Duration { return &metav1.Duration{Duration: d} }

func TestConnectionPoolToPoolConfig(t *testing.T) {
	cases := map[string]struct {
		reason string
		p      *ConnectionPool
		want   pool.Config
	}{
		"NilReceiver": {
			reason: "A nil ConnectionPool (connectionPool omitted entirely) should yield pool.Default.",
			p:      nil,
			want:   pool.Default,
		},
		"EmptyStruct": {
			reason: "An empty ConnectionPool (all fields omitted) should yield pool.Default.",
			p:      &ConnectionPool{},
			want:   pool.Default,
		},
		"MaxOpenConnectionsOnly": {
			reason: "Setting only MaxOpenConnections should override just that field.",
			p:      &ConnectionPool{MaxOpenConnections: new(42)},
			want: pool.Config{
				MaxOpenConns:    42,
				MaxIdleConns:    pool.Default.MaxIdleConns,
				ConnMaxLifetime: pool.Default.ConnMaxLifetime,
				ConnMaxIdleTime: pool.Default.ConnMaxIdleTime,
			},
		},
		"MaxIdleConnectionsOnly": {
			reason: "Setting only MaxIdleConnections should override just that field.",
			p:      &ConnectionPool{MaxIdleConnections: new(7)},
			want: pool.Config{
				MaxOpenConns:    pool.Default.MaxOpenConns,
				MaxIdleConns:    7,
				ConnMaxLifetime: pool.Default.ConnMaxLifetime,
				ConnMaxIdleTime: pool.Default.ConnMaxIdleTime,
			},
		},
		"MaxConnLifetimeOnly": {
			reason: "Setting only MaxConnLifetime should override just that field.",
			p:      &ConnectionPool{MaxConnLifetime: durPtr(30 * time.Minute)},
			want: pool.Config{
				MaxOpenConns:    pool.Default.MaxOpenConns,
				MaxIdleConns:    pool.Default.MaxIdleConns,
				ConnMaxLifetime: 30 * time.Minute,
				ConnMaxIdleTime: pool.Default.ConnMaxIdleTime,
			},
		},
		"MaxConnIdleTimeOnly": {
			reason: "Setting only MaxConnIdleTime should override just that field.",
			p:      &ConnectionPool{MaxConnIdleTime: durPtr(2 * time.Minute)},
			want: pool.Config{
				MaxOpenConns:    pool.Default.MaxOpenConns,
				MaxIdleConns:    pool.Default.MaxIdleConns,
				ConnMaxLifetime: pool.Default.ConnMaxLifetime,
				ConnMaxIdleTime: 2 * time.Minute,
			},
		},
		"AllFieldsSet": {
			reason: "Setting every field should override all of them.",
			p: &ConnectionPool{
				MaxOpenConnections: new(20),
				MaxIdleConnections: new(10),
				MaxConnLifetime:    durPtr(2 * time.Hour),
				MaxConnIdleTime:    durPtr(15 * time.Minute),
			},
			want: pool.Config{
				MaxOpenConns:    20,
				MaxIdleConns:    10,
				ConnMaxLifetime: 2 * time.Hour,
				ConnMaxIdleTime: 15 * time.Minute,
			},
		},
		"ExplicitZeroMaxOpenConnectionsIsNotDefaulted": {
			reason: "An explicit 0 (unlimited) must be distinguished from an unset field and not be defaulted away.",
			p:      &ConnectionPool{MaxOpenConnections: new(0)},
			want: pool.Config{
				MaxOpenConns:    0,
				MaxIdleConns:    pool.Default.MaxIdleConns,
				ConnMaxLifetime: pool.Default.ConnMaxLifetime,
				ConnMaxIdleTime: pool.Default.ConnMaxIdleTime,
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			got := tc.p.ToPoolConfig()
			if got != tc.want {
				t.Errorf("\n%s\nToPoolConfig(): got %+v, want %+v", tc.reason, got, tc.want)
			}
		})
	}
}
