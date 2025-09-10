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

package extension

import (
	"context"
	"strings"

	"github.com/lib/pq"
	"github.com/pkg/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	xpcontroller "github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/feature"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"

	namespacedv1alpha1 "github.com/crossplane-contrib/provider-sql/apis/namespaced/postgresql/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/postgresql"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
	"github.com/crossplane-contrib/provider-sql/pkg/controller/namespaced/postgresql/provider"
)

const (
	errTrackPCUsage = "cannot track ProviderConfig usage"

	errNotExtension    = "managed resource is not a Extension custom resource"
	errSelectExtension = "cannot select extension"
	errCreateExtension = "cannot create extension"
	errDropExtension   = "cannot drop extension"

	maxConcurrency = 5
)

// TODO(nateinaction): This looks wrong, can tracker creation be improved?
type tracker struct {
	tracker *resource.ProviderConfigUsageTracker
}

var _ resource.Tracker = &tracker{}

func (t *tracker) Track(ctx context.Context, mg resource.Managed) error {
	return t.tracker.Track(ctx, mg.(resource.ModernManaged))
}

// Setup adds a controller that reconciles Database managed resources.
func Setup(mgr ctrl.Manager, o xpcontroller.Options) error {
	name := managed.ControllerName(namespacedv1alpha1.ExtensionGroupKind)

	t := resource.NewProviderConfigUsageTracker(mgr.GetClient(), &namespacedv1alpha1.ProviderConfigUsage{})
	trk := &tracker{tracker: t}

	reconcilerOptions := []managed.ReconcilerOption{
		managed.WithTypedExternalConnector(&connector{kube: mgr.GetClient(), usage: trk, newDB: postgresql.New}),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithPollInterval(o.PollInterval),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))),
	}
	if o.Features.Enabled(feature.EnableBetaManagementPolicies) {
		reconcilerOptions = append(reconcilerOptions, managed.WithManagementPolicies())
	}
	r := managed.NewReconciler(mgr,
		resource.ManagedKind(namespacedv1alpha1.ExtensionGroupVersionKind),
		reconcilerOptions...,
	)
	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		For(&namespacedv1alpha1.Extension{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: maxConcurrency,
		}).
		Complete(r)
}

type connector struct {
	kube  client.Client
	usage resource.Tracker
	newDB func(creds map[string][]byte, database string, sslmode string) xsql.DB
}

var _ managed.TypedExternalConnector[resource.Managed] = &connector{}

func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.TypedExternalClient[resource.Managed], error) {
	cr, ok := mg.(*namespacedv1alpha1.Extension)
	if !ok {
		return nil, errors.New(errNotExtension)
	}

	if err := c.usage.Track(ctx, mg); err != nil {
		return nil, errors.Wrap(err, errTrackPCUsage)
	}

	// ProviderConfigReference could theoretically be nil, but in practice the
	// DefaultProviderConfig initializer will set it before we get here.
	providerInfo, err := provider.GetProviderConfig(ctx, c.kube, cr)
	if err != nil {
		return nil, err
	}

	// We do not want to create an extension on the default DB
	// if the user was expecting a database name to be resolved.
	if cr.Spec.ForProvider.Database != nil {
		return &external{db: c.newDB(providerInfo.SecretData, *cr.Spec.ForProvider.Database, clients.ToString(providerInfo.SSLMode))}, nil
	}

	return &external{db: c.newDB(providerInfo.SecretData, providerInfo.DefaultDatabase, clients.ToString(providerInfo.SSLMode))}, nil
}

type external struct{ db xsql.DB }

var _ managed.TypedExternalClient[resource.Managed] = &external{}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*namespacedv1alpha1.Extension)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotExtension)
	}

	// If the Extension exists, it will have all of these properties.
	observed := namespacedv1alpha1.ExtensionParameters{
		Version: new(string),
	}

	query := "SELECT " +
		"extversion " +
		"FROM pg_extension " +
		"WHERE extname = $1"

	err := c.db.Scan(ctx, xsql.Query{
		String:     query,
		Parameters: []interface{}{cr.Spec.ForProvider.Extension},
	},
		observed.Version,
	)

	// If the database we try to connect on does not exist then
	// there cannot be an extension on that database either.
	if xsql.IsNoRows(err) || postgresql.IsInvalidCatalog(err) {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errSelectExtension)
	}

	cr.SetConditions(xpv1.Available())

	return managed.ExternalObservation{
		ResourceExists:          true,
		ResourceLateInitialized: lateInit(observed, &cr.Spec.ForProvider),
		ResourceUpToDate:        upToDate(observed, cr.Spec.ForProvider),
	}, nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) { //nolint:gocyclo
	cr, ok := mg.(*namespacedv1alpha1.Extension)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotExtension)
	}

	var b strings.Builder
	b.WriteString("CREATE EXTENSION IF NOT EXISTS ")
	b.WriteString(pq.QuoteIdentifier(cr.Spec.ForProvider.Extension))

	if cr.Spec.ForProvider.Version != nil {
		b.WriteString(" WITH VERSION ")
		b.WriteString(pq.QuoteIdentifier(*cr.Spec.ForProvider.Version))
	}

	return managed.ExternalCreation{}, errors.Wrap(c.db.Exec(ctx, xsql.Query{String: b.String()}), errCreateExtension)
}

func (c *external) Update(_ context.Context, mg resource.Managed) (managed.ExternalUpdate, error) { //nolint:gocyclo
	_, ok := mg.(*namespacedv1alpha1.Extension)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotExtension)
	}

	return managed.ExternalUpdate{}, nil
}

func (c *external) Disconnect(ctx context.Context) error {
	return nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) (managed.ExternalDelete, error) {
	cr, ok := mg.(*namespacedv1alpha1.Extension)
	if !ok {
		return managed.ExternalDelete{}, errors.New(errNotExtension)
	}

	err := c.db.Exec(ctx, xsql.Query{String: "DROP EXTENSION IF EXISTS " + pq.QuoteIdentifier(cr.Spec.ForProvider.Extension)})
	return managed.ExternalDelete{}, errors.Wrap(err, errDropExtension)
}

func upToDate(observed, desired namespacedv1alpha1.ExtensionParameters) bool {
	if desired.Version == nil || (observed.Version != nil && *desired.Version == *observed.Version) {
		return true
	}
	return false
}

func lateInit(observed namespacedv1alpha1.ExtensionParameters, desired *namespacedv1alpha1.ExtensionParameters) bool {
	li := false

	if desired.Version == nil && observed.Version != nil {
		desired.Version = observed.Version
		li = true
	}

	return li
}
