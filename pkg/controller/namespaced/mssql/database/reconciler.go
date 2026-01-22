/*
Copyright 2021 The Crossplane Authors.

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

package database

import (
	"context"

	"github.com/pkg/errors"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	xpcontroller "github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/feature"
	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/v2/pkg/resource"

	namespacedv1alpha1 "github.com/crossplane-contrib/provider-sql/apis/namespaced/mssql/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/mssql"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
	"github.com/crossplane-contrib/provider-sql/pkg/controller/namespaced/mssql/provider"
)

const (
	errTrackUsage   = "cannot track ProviderConfig usage"
	errTrackPCUsage = "cannot track ProviderConfig usage"

	errSelectDB = "cannot select database"
	errCreateDB = "cannot create database"
	errDropDB   = "cannot drop database"

	maxConcurrency = 5
)

// Setup adds a controller that reconciles Database managed resources.
func Setup(mgr ctrl.Manager, o xpcontroller.Options) error {
	name := managed.ControllerName(namespacedv1alpha1.DatabaseGroupKind)

	t := resource.NewProviderConfigUsageTracker(mgr.GetClient(), &namespacedv1alpha1.ProviderConfigUsage{})

	reconcilerOptions := []managed.ReconcilerOption{
		managed.WithTypedExternalConnector(&connector{kube: mgr.GetClient(), track: t.Track, newClient: mssql.New}),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithPollInterval(o.PollInterval),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))),
	}
	if o.Features.Enabled(feature.EnableBetaManagementPolicies) {
		reconcilerOptions = append(reconcilerOptions, managed.WithManagementPolicies())
	}
	r := managed.NewReconciler(mgr,
		resource.ManagedKind(namespacedv1alpha1.DatabaseGroupVersionKind),
		reconcilerOptions...,
	)
	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		For(&namespacedv1alpha1.Database{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: maxConcurrency,
		}).
		Complete(r)
}

type connector struct {
	kube      client.Client
	track     func(ctx context.Context, mg resource.ModernManaged) error
	newClient func(creds map[string][]byte, database string) xsql.DB
}

var _ managed.TypedExternalConnector[*namespacedv1alpha1.Database] = &connector{}

func (c *connector) Connect(ctx context.Context, mg *namespacedv1alpha1.Database) (managed.TypedExternalClient[*namespacedv1alpha1.Database], error) {
	if err := c.track(ctx, mg); err != nil {
		return nil, errors.Wrap(err, errTrackPCUsage)
	}

	// ProviderConfigReference could theoretically be nil, but in practice the
	// DefaultProviderConfig initializer will set it before we get here.
	providerInfo, err := provider.GetProviderConfig(ctx, c.kube, mg)
	if err != nil {
		return nil, err
	}

	return &external{db: c.newClient(providerInfo.SecretData, "")}, nil
}

type external struct{ db xsql.DB }

var _ managed.TypedExternalClient[*namespacedv1alpha1.Database] = &external{}

func (c *external) Disconnect(ctx context.Context) error {
	// Do we need to implement this? Clean up any db connections?
	// The xsql.DB interface does not have a Disconnect method.
	return nil
}

func (c *external) Observe(ctx context.Context, mg *namespacedv1alpha1.Database) (managed.ExternalObservation, error) {
	var name string
	query := "SELECT name FROM master.sys.databases WHERE name = @p1"
	err := c.db.Scan(ctx, xsql.Query{String: query, Parameters: []interface{}{meta.GetExternalName(mg)}}, &name)
	if xsql.IsNoRows(err) {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errSelectDB)
	}

	mg.SetConditions(xpv1.Available())

	return managed.ExternalObservation{
		ResourceExists: true,

		// TODO(turkenh): Support these when we have anything to update.
		ResourceLateInitialized: false,
		ResourceUpToDate:        true,
	}, nil
}

func (c *external) Create(ctx context.Context, mg *namespacedv1alpha1.Database) (managed.ExternalCreation, error) {
	err := c.db.Exec(ctx, xsql.Query{String: "CREATE DATABASE " + mssql.QuoteIdentifier(meta.GetExternalName(mg))})
	return managed.ExternalCreation{}, errors.Wrap(err, errCreateDB)
}

func (c *external) Update(_ context.Context, _ *namespacedv1alpha1.Database) (managed.ExternalUpdate, error) {
	// TODO(turkenh): Support updates once we have anything to update.
	return managed.ExternalUpdate{}, nil
}

func (c *external) Delete(ctx context.Context, mg *namespacedv1alpha1.Database) (managed.ExternalDelete, error) {
	err := c.db.Exec(ctx, xsql.Query{String: "DROP DATABASE IF EXISTS " + mssql.QuoteIdentifier(meta.GetExternalName(mg))})
	return managed.ExternalDelete{}, errors.Wrap(err, errDropDB)
}
