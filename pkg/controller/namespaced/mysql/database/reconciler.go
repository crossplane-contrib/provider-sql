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

	namespacedv1alpha1 "github.com/crossplane-contrib/provider-sql/apis/namespaced/mysql/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/mysql"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
	"github.com/crossplane-contrib/provider-sql/pkg/controller/namespaced/mysql/provider"
	"github.com/crossplane-contrib/provider-sql/pkg/controller/namespaced/mysql/tls"
)

const (
	errTrackPCUsage = "cannot track ProviderConfig usage"
	errTLSConfig    = "cannot load TLS config"

	errNotDatabase = "managed resource is not a Database custom resource"
	errSelectDB    = "cannot select database"
	errCreateDB    = "cannot create database"
	errDropDB      = "cannot drop database"

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
	name := managed.ControllerName(namespacedv1alpha1.DatabaseGroupKind)

	t := resource.NewProviderConfigUsageTracker(mgr.GetClient(), &namespacedv1alpha1.ProviderConfigUsage{})
	trk := &tracker{tracker: t}

	reconcilerOptions := []managed.ReconcilerOption{
		managed.WithTypedExternalConnector(&connector{kube: mgr.GetClient(), usage: trk, newDB: mysql.New}),
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
	kube  client.Client
	usage resource.Tracker
	newDB func(creds map[string][]byte, tls *string, binlog *bool) xsql.DB
}

var _ managed.TypedExternalConnector[resource.Managed] = &connector{}

func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.TypedExternalClient[resource.Managed], error) {
	cr, ok := mg.(*namespacedv1alpha1.Database)
	if !ok {
		return nil, errors.New(errNotDatabase)
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

	tlsName, err := tls.LoadConfig(ctx, c.kube, providerInfo.ProviderConfigName, providerInfo.TLS, providerInfo.TLSConfig)
	if err != nil {
		return nil, errors.Wrap(err, errTLSConfig)
	}

	return &external{db: c.newDB(providerInfo.SecretData, tlsName, cr.Spec.ForProvider.BinLog)}, nil
}

type external struct{ db xsql.DB }

var _ managed.TypedExternalClient[resource.Managed] = &external{}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*namespacedv1alpha1.Database)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotDatabase)
	}

	var name string
	query := "SELECT schema_name FROM information_schema.schemata WHERE schema_name = ?"
	err := c.db.Scan(ctx, xsql.Query{String: query, Parameters: []interface{}{meta.GetExternalName(cr)}}, &name)
	if xsql.IsNoRows(err) {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errSelectDB)
	}

	cr.SetConditions(xpv1.Available())

	return managed.ExternalObservation{
		ResourceExists: true,

		// TODO(negz): Support these when we have anything to update.
		ResourceLateInitialized: false,
		ResourceUpToDate:        true,
	}, nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*namespacedv1alpha1.Database)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotDatabase)
	}

	query := "CREATE DATABASE " + mysql.QuoteIdentifier(meta.GetExternalName(cr))

	if err := mysql.ExecWrapper(ctx, c.db, mysql.ExecQuery{Query: query, ErrorValue: errCreateDB}); err != nil {
		return managed.ExternalCreation{}, err
	}

	return managed.ExternalCreation{}, nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	// TODO(negz): Support updates once we have anything to update.
	return managed.ExternalUpdate{}, nil
}

func (c *external) Disconnect(ctx context.Context) error {
	return nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) (managed.ExternalDelete, error) {
	cr, ok := mg.(*namespacedv1alpha1.Database)
	if !ok {
		return managed.ExternalDelete{}, errors.New(errNotDatabase)
	}

	query := "DROP DATABASE IF EXISTS " + mysql.QuoteIdentifier(meta.GetExternalName(cr))

	if err := mysql.ExecWrapper(ctx, c.db, mysql.ExecQuery{Query: query, ErrorValue: errDropDB}); err != nil {
		return managed.ExternalDelete{}, err
	}

	return managed.ExternalDelete{}, nil
}
