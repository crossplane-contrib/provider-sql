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

package hana

import (
	"github.com/crossplane-contrib/provider-sql/pkg/controller/hana/dbschema"
	"github.com/crossplane-contrib/provider-sql/pkg/controller/hana/role"
	"github.com/crossplane-contrib/provider-sql/pkg/controller/hana/user"
	"github.com/crossplane-contrib/provider-sql/pkg/controller/hana/usergroup"
	ctrl "sigs.k8s.io/controller-runtime"

	"github.com/crossplane/crossplane-runtime/pkg/controller"
)

// Setup creates all Hana controllers with the supplied logger and adds
// them to the supplied manager.
func Setup(mgr ctrl.Manager, o controller.Options) error {
	for _, setup := range []func(ctrl.Manager, controller.Options) error{
		dbschema.Setup,
		user.Setup,
		usergroup.Setup,
		role.Setup,
	} {
		if err := setup(mgr, o); err != nil {
			return err
		}
	}
	return nil
}
