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

package postgresql

import (
	ctrl "sigs.k8s.io/controller-runtime"

	xpcontroller "github.com/crossplane/crossplane-runtime/v2/pkg/controller"

	"github.com/crossplane-contrib/provider-sql/pkg/controller/namespaced/postgresql/config"
	"github.com/crossplane-contrib/provider-sql/pkg/controller/namespaced/postgresql/database"
	"github.com/crossplane-contrib/provider-sql/pkg/controller/namespaced/postgresql/default_privileges"
	"github.com/crossplane-contrib/provider-sql/pkg/controller/namespaced/postgresql/extension"
	"github.com/crossplane-contrib/provider-sql/pkg/controller/namespaced/postgresql/grant"
	"github.com/crossplane-contrib/provider-sql/pkg/controller/namespaced/postgresql/role"
	schemacontroller "github.com/crossplane-contrib/provider-sql/pkg/controller/namespaced/postgresql/schema"
)

// Setup creates all PostgreSQL controllers with the supplied logger and adds
// them to the supplied manager.
func Setup(mgr ctrl.Manager, o xpcontroller.Options) error {
	for _, setup := range []func(ctrl.Manager, xpcontroller.Options) error{
		config.Setup,
		database.Setup,
		role.Setup,
		grant.Setup,
		extension.Setup,
		schemacontroller.Setup,
		default_privileges.Setup,
	} {
		if err := setup(mgr, o); err != nil {
			return err
		}
	}
	return nil
}

// SetupGated creates all PostgreSQL controllers with gated initialization,
// waiting for their required CRDs to be available before starting.
func SetupGated(mgr ctrl.Manager, o xpcontroller.Options) error {
	for _, setup := range []func(ctrl.Manager, xpcontroller.Options) error{
		config.SetupGated,
		database.SetupGated,
		role.SetupGated,
		grant.SetupGated,
		extension.SetupGated,
		schemacontroller.SetupGated,
		default_privileges.SetupGated,
	} {
		if err := setup(mgr, o); err != nil {
			return err
		}
	}
	return nil
}
