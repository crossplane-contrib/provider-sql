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
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
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

	"github.com/crossplane-contrib/provider-sql/apis/cluster/mysql/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/mysql"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
	"github.com/crossplane-contrib/provider-sql/pkg/controller/cluster/mysql/tls"
)

const (
	errTrackPCUsage = "cannot track ProviderConfig usage"
	errGetPC        = "cannot get ProviderConfig"
	errNoSecretRef  = "ProviderConfig does not reference a credentials Secret"
	errGetSecret    = "cannot get credentials Secret"
	errTLSConfig    = "cannot load TLS config"

	errSelectDB = "cannot select database"
	errCreateDB = "cannot create database"
	errDropDB   = "cannot drop database"
	errUpdateDB = "cannot update database"

	maxConcurrency = 5
)

// Setup adds a controller that reconciles Database managed resources.
func Setup(mgr ctrl.Manager, o xpcontroller.Options) error {
	name := managed.ControllerName(v1alpha1.DatabaseGroupKind)

	t := resource.NewLegacyProviderConfigUsageTracker(mgr.GetClient(), &v1alpha1.ProviderConfigUsage{})

	reconcilerOptions := []managed.ReconcilerOption{
		managed.WithTypedExternalConnector(&connector{kube: mgr.GetClient(), track: t.Track, newDB: mysql.New}),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithPollInterval(o.PollInterval),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))),
	}
	if o.Features.Enabled(feature.EnableBetaManagementPolicies) {
		reconcilerOptions = append(reconcilerOptions, managed.WithManagementPolicies())
	}
	r := managed.NewReconciler(mgr,
		resource.ManagedKind(v1alpha1.DatabaseGroupVersionKind),
		reconcilerOptions...,
	)

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		For(&v1alpha1.Database{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: maxConcurrency,
		}).
		Complete(r)
}

// SetupGated adds a controller that reconciles Database managed resources
// with gated initialization, waiting for the resource's CRD to be available.
func SetupGated(mgr ctrl.Manager, o xpcontroller.Options) error {
	o.Gate.Register(func() {
		if err := Setup(mgr, o); err != nil {
			mgr.GetLogger().Error(err, "unable to setup controller", "gvk", v1alpha1.DatabaseGroupVersionKind)
		}
	}, v1alpha1.DatabaseGroupVersionKind)
	return nil
}

type connector struct {
	kube  client.Client
	track func(ctx context.Context, mg resource.LegacyManaged) error
	newDB func(creds map[string][]byte, tls *string, binlog *bool) xsql.DB
}

var _ managed.TypedExternalConnector[*v1alpha1.Database] = &connector{}

func (c *connector) Connect(ctx context.Context, mg *v1alpha1.Database) (managed.TypedExternalClient[*v1alpha1.Database], error) {
	if err := c.track(ctx, mg); err != nil {
		return nil, errors.Wrap(err, errTrackPCUsage)
	}

	// ProviderConfigReference could theoretically be nil, but in practice the
	// DefaultProviderConfig initializer will set it before we get here.
	providerConfigName := mg.GetProviderConfigReference().Name
	pc := &v1alpha1.ProviderConfig{}
	if err := c.kube.Get(ctx, types.NamespacedName{Name: providerConfigName}, pc); err != nil {
		return nil, errors.Wrap(err, errGetPC)
	}

	// We don't need to check the credentials source because we currently only
	// support one source (MySQLConnectionSecret), which is required and
	// enforced by the ProviderConfig schema.
	ref := pc.Spec.Credentials.ConnectionSecretRef
	if ref == nil {
		return nil, errors.New(errNoSecretRef)
	}

	s := &corev1.Secret{}
	if err := c.kube.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, s); err != nil {
		return nil, errors.Wrap(err, errGetSecret)
	}

	tlsName, err := tls.LoadConfig(ctx, c.kube, providerConfigName, pc.Spec.TLS, pc.Spec.TLSConfig)
	if err != nil {
		return nil, errors.Wrap(err, errTLSConfig)
	}

	return &external{db: c.newDB(s.Data, tlsName, mg.Spec.ForProvider.BinLog)}, nil
}

type external struct{ db xsql.DB }

var _ managed.TypedExternalClient[*v1alpha1.Database] = &external{}

func (c *external) Observe(ctx context.Context, mg *v1alpha1.Database) (managed.ExternalObservation, error) {
	var name, charset, collation string
	query := "SELECT schema_name, default_character_set_name, default_collation_name FROM information_schema.schemata WHERE schema_name = ?"
	err := c.db.Scan(ctx, xsql.Query{String: query, Parameters: []interface{}{meta.GetExternalName(mg)}}, &name, &charset, &collation)
	if xsql.IsNoRows(err) {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errSelectDB)
	}

	upToDate := true
	lateInitialized := false

	if mg.Spec.ForProvider.DefaultCharacterSet != nil {
		if *mg.Spec.ForProvider.DefaultCharacterSet != charset {
			upToDate = false
		}
	} else if charset != "" {
		mg.Spec.ForProvider.DefaultCharacterSet = &charset
		lateInitialized = true
	}

	if mg.Spec.ForProvider.DefaultCollation != nil {
		if *mg.Spec.ForProvider.DefaultCollation != collation {
			upToDate = false
		}
	} else if collation != "" {
		mg.Spec.ForProvider.DefaultCollation = &collation
		lateInitialized = true
	}

	mg.SetConditions(xpv1.Available())

	return managed.ExternalObservation{
		ResourceExists:          true,
		ResourceLateInitialized: lateInitialized,
		ResourceUpToDate:        upToDate,
	}, nil
}

func (c *external) Create(ctx context.Context, mg *v1alpha1.Database) (managed.ExternalCreation, error) {
	query := "CREATE DATABASE " + mysql.QuoteIdentifier(meta.GetExternalName(mg))
	query += charsetClause(mg.Spec.ForProvider.DefaultCharacterSet, mg.Spec.ForProvider.DefaultCollation)

	if err := mysql.ExecWrapper(ctx, c.db, mysql.ExecQuery{Query: query, ErrorValue: errCreateDB}); err != nil {
		return managed.ExternalCreation{}, err
	}

	return managed.ExternalCreation{}, nil
}

func (c *external) Update(ctx context.Context, mg *v1alpha1.Database) (managed.ExternalUpdate, error) {
	query := "ALTER DATABASE " + mysql.QuoteIdentifier(meta.GetExternalName(mg))
	query += charsetClause(mg.Spec.ForProvider.DefaultCharacterSet, mg.Spec.ForProvider.DefaultCollation)

	if err := mysql.ExecWrapper(ctx, c.db, mysql.ExecQuery{Query: query, ErrorValue: errUpdateDB}); err != nil {
		return managed.ExternalUpdate{}, err
	}

	return managed.ExternalUpdate{}, nil
}

func (c *external) Disconnect(ctx context.Context) error {
	return nil
}

// charsetClause builds the CHARACTER SET / COLLATE suffix for CREATE or ALTER DATABASE.
func charsetClause(charset, collation *string) string {
	var clause string
	if charset != nil {
		clause += " CHARACTER SET " + mysql.QuoteValue(*charset)
	}
	if collation != nil {
		clause += " COLLATE " + mysql.QuoteValue(*collation)
	}
	return clause
}

func (c *external) Delete(ctx context.Context, mg *v1alpha1.Database) (managed.ExternalDelete, error) {
	query := "DROP DATABASE IF EXISTS " + mysql.QuoteIdentifier(meta.GetExternalName(mg))

	if err := mysql.ExecWrapper(ctx, c.db, mysql.ExecQuery{Query: query, ErrorValue: errDropDB}); err != nil {
		return managed.ExternalDelete{}, err
	}

	return managed.ExternalDelete{}, nil
}
