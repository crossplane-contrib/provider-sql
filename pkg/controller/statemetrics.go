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
	"time"

	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/crossplane/crossplane-runtime/v2/pkg/logging"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"github.com/crossplane/crossplane-runtime/v2/pkg/statemetrics"

	clustermssql "github.com/crossplane-contrib/provider-sql/apis/cluster/mssql/v1alpha1"
	clustermysql "github.com/crossplane-contrib/provider-sql/apis/cluster/mysql/v1alpha1"
	clusterpostgresql "github.com/crossplane-contrib/provider-sql/apis/cluster/postgresql/v1alpha1"
	namespacedmssql "github.com/crossplane-contrib/provider-sql/apis/namespaced/mssql/v1alpha1"
	namespacedmysql "github.com/crossplane-contrib/provider-sql/apis/namespaced/mysql/v1alpha1"
	namespacedpostgresql "github.com/crossplane-contrib/provider-sql/apis/namespaced/postgresql/v1alpha1"
)

// stateRecorderConfig holds the configuration for a state recorder.
type stateRecorderConfig struct {
	name string
	list resource.ManagedList
}

// SetupStateMetrics adds MRStateRecorders for all managed resource types
// to the supplied manager. This enables Prometheus metrics for managed
// resource state (exists, ready, synced).
func SetupStateMetrics(mgr ctrl.Manager, log logging.Logger, metrics *statemetrics.MRStateMetrics, pollInterval time.Duration) error {
	configs := []stateRecorderConfig{
		// Cluster-scoped PostgreSQL resources
		{name: "postgresql-cluster-database", list: &clusterpostgresql.DatabaseList{}},
		{name: "postgresql-cluster-extension", list: &clusterpostgresql.ExtensionList{}},
		{name: "postgresql-cluster-grant", list: &clusterpostgresql.GrantList{}},
		{name: "postgresql-cluster-role", list: &clusterpostgresql.RoleList{}},
		{name: "postgresql-cluster-schema", list: &clusterpostgresql.SchemaList{}},

		// Namespaced PostgreSQL resources
		{name: "postgresql-namespaced-database", list: &namespacedpostgresql.DatabaseList{}},
		{name: "postgresql-namespaced-extension", list: &namespacedpostgresql.ExtensionList{}},
		{name: "postgresql-namespaced-grant", list: &namespacedpostgresql.GrantList{}},
		{name: "postgresql-namespaced-role", list: &namespacedpostgresql.RoleList{}},
		{name: "postgresql-namespaced-schema", list: &namespacedpostgresql.SchemaList{}},

		// Cluster-scoped MySQL resources
		{name: "mysql-cluster-database", list: &clustermysql.DatabaseList{}},
		{name: "mysql-cluster-grant", list: &clustermysql.GrantList{}},
		{name: "mysql-cluster-user", list: &clustermysql.UserList{}},

		// Namespaced MySQL resources
		{name: "mysql-namespaced-database", list: &namespacedmysql.DatabaseList{}},
		{name: "mysql-namespaced-grant", list: &namespacedmysql.GrantList{}},
		{name: "mysql-namespaced-user", list: &namespacedmysql.UserList{}},

		// Cluster-scoped MSSQL resources
		{name: "mssql-cluster-database", list: &clustermssql.DatabaseList{}},
		{name: "mssql-cluster-grant", list: &clustermssql.GrantList{}},
		{name: "mssql-cluster-user", list: &clustermssql.UserList{}},

		// Namespaced MSSQL resources
		{name: "mssql-namespaced-database", list: &namespacedmssql.DatabaseList{}},
		{name: "mssql-namespaced-grant", list: &namespacedmssql.GrantList{}},
		{name: "mssql-namespaced-user", list: &namespacedmssql.UserList{}},
	}

	for _, cfg := range configs {
		if err := mgr.Add(statemetrics.NewMRStateRecorder(
			mgr.GetClient(),
			log.WithValues("recorder", cfg.name),
			metrics,
			cfg.list,
			pollInterval,
		)); err != nil {
			return err
		}
	}

	return nil
}
