/*
Copyright 2025 The Crossplane Authors.

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

package setup

import (
	"fmt"

	xpcontroller "github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/feature"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"
	"github.com/crossplane/crossplane-runtime/v2/pkg/statemetrics"
	"k8s.io/apimachinery/pkg/runtime/schema"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"
)

const maxConcurrency = 5

// ControllerConfig holds the resource-specific parameters needed to set up a managed resource controller.
type ControllerConfig struct {
	GVK       schema.GroupVersionKind
	Resource  client.Object
	List      resource.ManagedList
	Connector managed.ReconcilerOption
}

// Setup registers a managed resource controller with safe-start support.
// The actual controller creation is deferred until the corresponding CRD becomes available.
func Setup(mgr ctrl.Manager, o xpcontroller.Options, cfg ControllerConfig) error {
	o.Gate.Register(func() {
		name := managed.ControllerName(cfg.GVK.GroupKind().String())

		opts := []managed.ReconcilerOption{
			cfg.Connector,
			managed.WithLogger(o.Logger.WithValues("controller", name)),
			managed.WithPollInterval(o.PollInterval),
			managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))),
		}
		if o.Features.Enabled(feature.EnableBetaManagementPolicies) {
			opts = append(opts, managed.WithManagementPolicies())
		}

		r := managed.NewReconciler(mgr, resource.ManagedKind(cfg.GVK), opts...)

		if err := mgr.Add(statemetrics.NewMRStateRecorder(
			mgr.GetClient(), o.Logger, o.MetricOptions.MRStateMetrics,
			cfg.List, o.MetricOptions.PollStateMetricInterval,
		)); err != nil {
			panic(fmt.Errorf("cannot setup %s controller: %w", cfg.GVK.Kind, err))
		}

		if err := ctrl.NewControllerManagedBy(mgr).
			Named(name).
			For(cfg.Resource).
			WithOptions(controller.Options{
				MaxConcurrentReconciles: maxConcurrency,
			}).
			Complete(r); err != nil {
			panic(fmt.Errorf("cannot setup %s controller: %w", cfg.GVK.Kind, err))
		}
	}, cfg.GVK)
	return nil
}
