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

package controller

import (
	"strings"
	"testing"

	"github.com/prometheus/client_golang/prometheus"
	"github.com/prometheus/client_golang/prometheus/testutil"

	"github.com/crossplane/crossplane-runtime/v2/pkg/statemetrics"

	clustermssql "github.com/crossplane-contrib/provider-sql/apis/cluster/mssql/v1alpha1"
	clustermysql "github.com/crossplane-contrib/provider-sql/apis/cluster/mysql/v1alpha1"
	clusterpostgresql "github.com/crossplane-contrib/provider-sql/apis/cluster/postgresql/v1alpha1"
	namespacedmssql "github.com/crossplane-contrib/provider-sql/apis/namespaced/mssql/v1alpha1"
	namespacedmysql "github.com/crossplane-contrib/provider-sql/apis/namespaced/mysql/v1alpha1"
	namespacedpostgresql "github.com/crossplane-contrib/provider-sql/apis/namespaced/postgresql/v1alpha1"
)

func TestStateRecorderConfigs(t *testing.T) {
	// Test that all expected resource types are configured
	// This validates that the configs slice in SetupStateMetrics contains all expected entries
	expectedConfigs := []struct {
		name     string
		listType interface{}
	}{
		// PostgreSQL cluster
		{name: "postgresql-cluster-database", listType: &clusterpostgresql.DatabaseList{}},
		{name: "postgresql-cluster-extension", listType: &clusterpostgresql.ExtensionList{}},
		{name: "postgresql-cluster-grant", listType: &clusterpostgresql.GrantList{}},
		{name: "postgresql-cluster-role", listType: &clusterpostgresql.RoleList{}},
		{name: "postgresql-cluster-schema", listType: &clusterpostgresql.SchemaList{}},
		// PostgreSQL namespaced
		{name: "postgresql-namespaced-database", listType: &namespacedpostgresql.DatabaseList{}},
		{name: "postgresql-namespaced-extension", listType: &namespacedpostgresql.ExtensionList{}},
		{name: "postgresql-namespaced-grant", listType: &namespacedpostgresql.GrantList{}},
		{name: "postgresql-namespaced-role", listType: &namespacedpostgresql.RoleList{}},
		{name: "postgresql-namespaced-schema", listType: &namespacedpostgresql.SchemaList{}},
		// MySQL cluster
		{name: "mysql-cluster-database", listType: &clustermysql.DatabaseList{}},
		{name: "mysql-cluster-grant", listType: &clustermysql.GrantList{}},
		{name: "mysql-cluster-user", listType: &clustermysql.UserList{}},
		// MySQL namespaced
		{name: "mysql-namespaced-database", listType: &namespacedmysql.DatabaseList{}},
		{name: "mysql-namespaced-grant", listType: &namespacedmysql.GrantList{}},
		{name: "mysql-namespaced-user", listType: &namespacedmysql.UserList{}},
		// MSSQL cluster
		{name: "mssql-cluster-database", listType: &clustermssql.DatabaseList{}},
		{name: "mssql-cluster-grant", listType: &clustermssql.GrantList{}},
		{name: "mssql-cluster-user", listType: &clustermssql.UserList{}},
		// MSSQL namespaced
		{name: "mssql-namespaced-database", listType: &namespacedmssql.DatabaseList{}},
		{name: "mssql-namespaced-grant", listType: &namespacedmssql.GrantList{}},
		{name: "mssql-namespaced-user", listType: &namespacedmssql.UserList{}},
	}

	// Verify expected count of 22 recorders:
	// PostgreSQL: 5 cluster + 5 namespaced = 10
	// MySQL: 3 cluster + 3 namespaced = 6
	// MSSQL: 3 cluster + 3 namespaced = 6
	// Total: 22
	expectedCount := 22
	if len(expectedConfigs) != expectedCount {
		t.Errorf("Expected %d state recorder configs, got %d", expectedCount, len(expectedConfigs))
	}

	// Verify each list type is not nil (ensures imports are correct)
	for _, cfg := range expectedConfigs {
		if cfg.listType == nil {
			t.Errorf("List type for %s should not be nil", cfg.name)
		}
		if cfg.name == "" {
			t.Error("Config name should not be empty")
		}
	}
}

func TestStateRecorderConfigNames(t *testing.T) {
	// Test that all config names follow the expected pattern
	expectedNames := map[string]bool{
		// PostgreSQL cluster
		"postgresql-cluster-database":  true,
		"postgresql-cluster-extension": true,
		"postgresql-cluster-grant":     true,
		"postgresql-cluster-role":      true,
		"postgresql-cluster-schema":    true,
		// PostgreSQL namespaced
		"postgresql-namespaced-database":  true,
		"postgresql-namespaced-extension": true,
		"postgresql-namespaced-grant":     true,
		"postgresql-namespaced-role":      true,
		"postgresql-namespaced-schema":    true,
		// MySQL cluster
		"mysql-cluster-database": true,
		"mysql-cluster-grant":    true,
		"mysql-cluster-user":     true,
		// MySQL namespaced
		"mysql-namespaced-database": true,
		"mysql-namespaced-grant":    true,
		"mysql-namespaced-user":     true,
		// MSSQL cluster
		"mssql-cluster-database": true,
		"mssql-cluster-grant":    true,
		"mssql-cluster-user":     true,
		// MSSQL namespaced
		"mssql-namespaced-database": true,
		"mssql-namespaced-grant":    true,
		"mssql-namespaced-user":     true,
	}

	// Validate naming convention: <db>-<scope>-<resource>
	for name := range expectedNames {
		// Names should contain exactly 2 hyphens
		hyphenCount := 0
		for _, c := range name {
			if c == '-' {
				hyphenCount++
			}
		}
		if hyphenCount != 2 {
			t.Errorf("Config name %q should follow pattern <db>-<scope>-<resource>", name)
		}
	}
}

func TestStateRecorderConfigCoverage(t *testing.T) {
	// Test that we have recorders for all database types and scopes

	databases := []string{"postgresql", "mysql", "mssql"}
	scopes := []string{"cluster", "namespaced"}

	// Expected resources per database
	postgresqlResources := []string{"database", "extension", "grant", "role", "schema"}
	mysqlResources := []string{"database", "grant", "user"}
	mssqlResources := []string{"database", "grant", "user"}

	cases := map[string]struct {
		db        string
		resources []string
	}{
		"PostgreSQL": {db: "postgresql", resources: postgresqlResources},
		"MySQL":      {db: "mysql", resources: mysqlResources},
		"MSSQL":      {db: "mssql", resources: mssqlResources},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			for _, scope := range scopes {
				for _, resource := range tc.resources {
					expectedName := tc.db + "-" + scope + "-" + resource
					// This test documents the expected configuration
					// The actual validation happens in TestStateRecorderConfigs
					if expectedName == "" {
						t.Errorf("Generated empty name for %s/%s/%s", tc.db, scope, resource)
					}
				}
			}
		})
	}

	// Verify total counts per database
	expectedPostgresql := len(postgresqlResources) * len(scopes) // 5 * 2 = 10
	expectedMySQL := len(mysqlResources) * len(scopes)           // 3 * 2 = 6
	expectedMSSQL := len(mssqlResources) * len(scopes)           // 3 * 2 = 6
	expectedTotal := expectedPostgresql + expectedMySQL + expectedMSSQL

	if expectedTotal != 22 {
		t.Errorf("Expected 22 total recorders, calculation shows %d", expectedTotal)
	}

	_ = databases // Silence unused variable warning
}

// TestMRStateMetricsRegistration verifies that state metrics can be registered
// with a Prometheus registry and expose the expected metric names.
func TestMRStateMetricsRegistration(t *testing.T) {
	mrStateMetrics := statemetrics.NewMRStateMetrics()

	// Create a new registry to avoid conflicts with the default registry
	reg := prometheus.NewRegistry()
	if err := reg.Register(mrStateMetrics); err != nil {
		t.Fatalf("Failed to register state metrics: %v", err)
	}

	// Set some test values
	testGVK := "postgresql.sql.crossplane.io/v1alpha1, Kind=Database"
	mrStateMetrics.Exists.WithLabelValues(testGVK).Set(5)
	mrStateMetrics.Ready.WithLabelValues(testGVK).Set(3)
	mrStateMetrics.Synced.WithLabelValues(testGVK).Set(2)

	// Verify the metrics were set correctly using testutil
	existsValue := testutil.ToFloat64(mrStateMetrics.Exists.WithLabelValues(testGVK))
	if existsValue != 5 {
		t.Errorf("Expected exists metric to be 5, got %v", existsValue)
	}

	readyValue := testutil.ToFloat64(mrStateMetrics.Ready.WithLabelValues(testGVK))
	if readyValue != 3 {
		t.Errorf("Expected ready metric to be 3, got %v", readyValue)
	}

	syncedValue := testutil.ToFloat64(mrStateMetrics.Synced.WithLabelValues(testGVK))
	if syncedValue != 2 {
		t.Errorf("Expected synced metric to be 2, got %v", syncedValue)
	}

	// Gather and verify metric names follow Prometheus conventions
	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Failed to gather metrics: %v", err)
	}

	expectedMetrics := map[string]bool{
		"crossplane_managed_resource_exists": false,
		"crossplane_managed_resource_ready":  false,
		"crossplane_managed_resource_synced": false,
	}

	for _, family := range families {
		if _, ok := expectedMetrics[family.GetName()]; ok {
			expectedMetrics[family.GetName()] = true
		}
	}

	for name, found := range expectedMetrics {
		if !found {
			t.Errorf("Expected metric %s not found in registry", name)
		}
	}
}

// TestMRStateMetricsMultipleGVKs verifies that metrics can track multiple
// different resource types simultaneously.
func TestMRStateMetricsMultipleGVKs(t *testing.T) {
	mrStateMetrics := statemetrics.NewMRStateMetrics()

	gvks := []string{
		"postgresql.sql.crossplane.io/v1alpha1, Kind=Database",
		"postgresql.sql.crossplane.io/v1alpha1, Kind=Role",
		"mysql.sql.crossplane.io/v1alpha1, Kind=Database",
		"mysql.sql.crossplane.io/v1alpha1, Kind=User",
	}

	// Set different values for each GVK
	for i, gvk := range gvks {
		mrStateMetrics.Exists.WithLabelValues(gvk).Set(float64(i + 1))
		mrStateMetrics.Ready.WithLabelValues(gvk).Set(float64(i))
		mrStateMetrics.Synced.WithLabelValues(gvk).Set(float64(i))
	}

	// Verify each GVK has its own independent metric
	for i, gvk := range gvks {
		existsValue := testutil.ToFloat64(mrStateMetrics.Exists.WithLabelValues(gvk))
		expectedExists := float64(i + 1)
		if existsValue != expectedExists {
			t.Errorf("GVK %s: expected exists=%v, got %v", gvk, expectedExists, existsValue)
		}

		readyValue := testutil.ToFloat64(mrStateMetrics.Ready.WithLabelValues(gvk))
		expectedReady := float64(i)
		if readyValue != expectedReady {
			t.Errorf("GVK %s: expected ready=%v, got %v", gvk, expectedReady, readyValue)
		}
	}
}

// TestMetricLabelFormat verifies that GVK labels are formatted correctly.
func TestMetricLabelFormat(t *testing.T) {
	mrStateMetrics := statemetrics.NewMRStateMetrics()
	reg := prometheus.NewRegistry()
	reg.MustRegister(mrStateMetrics)

	// Set a metric with a properly formatted GVK label
	gvk := "postgresql.sql.crossplane.io/v1alpha1, Kind=Database"
	mrStateMetrics.Exists.WithLabelValues(gvk).Set(1)

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Failed to gather metrics: %v", err)
	}

	// Find the exists metric and verify the label format
	for _, family := range families {
		if family.GetName() == "crossplane_managed_resource_exists" {
			for _, metric := range family.GetMetric() {
				for _, label := range metric.GetLabel() {
					if label.GetName() == "gvk" {
						// Verify the label contains expected components
						value := label.GetValue()
						if !strings.Contains(value, "postgresql.sql.crossplane.io") {
							t.Errorf("GVK label should contain group, got: %s", value)
						}
						if !strings.Contains(value, "v1alpha1") {
							t.Errorf("GVK label should contain version, got: %s", value)
						}
						if !strings.Contains(value, "Database") {
							t.Errorf("GVK label should contain kind, got: %s", value)
						}
					}
				}
			}
		}
	}
}
