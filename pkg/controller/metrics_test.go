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
)

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

	for i, gvk := range gvks {
		mrStateMetrics.Exists.WithLabelValues(gvk).Set(float64(i + 1))
		mrStateMetrics.Ready.WithLabelValues(gvk).Set(float64(i))
		mrStateMetrics.Synced.WithLabelValues(gvk).Set(float64(i))
	}

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

	gvk := "postgresql.sql.crossplane.io/v1alpha1, Kind=Database"
	mrStateMetrics.Exists.WithLabelValues(gvk).Set(1)

	families, err := reg.Gather()
	if err != nil {
		t.Fatalf("Failed to gather metrics: %v", err)
	}

	for _, family := range families {
		if family.GetName() == "crossplane_managed_resource_exists" {
			for _, metric := range family.GetMetric() {
				for _, label := range metric.GetLabel() {
					if label.GetName() == "gvk" {
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
