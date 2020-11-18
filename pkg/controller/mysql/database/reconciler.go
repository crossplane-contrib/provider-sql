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
	"strings"

	"github.com/pkg/errors"
	v1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	runtimev1alpha1 "github.com/crossplane/crossplane-runtime/apis/core/v1alpha1"
	"github.com/crossplane/crossplane-runtime/pkg/event"
	"github.com/crossplane/crossplane-runtime/pkg/logging"
	"github.com/crossplane/crossplane-runtime/pkg/meta"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/resource"

	"github.com/crossplane-contrib/provider-sql/apis/mysql/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/sql"
)

const (
	errTrackPCUsage = "cannot track ProviderConfig usage"
	errGetPC        = "cannot get ProviderConfig"
	errNoSecretRef  = "ProviderConfig does not reference a credentials Secret"
	errGetSecret    = "cannot get credentials Secret"

	errNotDatabase = "managed resource is not a Database custom resource"
	errSelectDB    = "cannot select database"
	errCreateDB    = "cannot create database"
	errDropDB      = "cannot drop database"
)

// Setup adds a controller that reconciles Database managed resources.
func Setup(mgr ctrl.Manager, l logging.Logger) error {
	name := managed.ControllerName(v1alpha1.DatabaseGroupKind)

	t := resource.NewProviderConfigUsageTracker(mgr.GetClient(), &v1alpha1.ProviderConfigUsage{})
	r := managed.NewReconciler(mgr,
		resource.ManagedKind(v1alpha1.DatabaseGroupVersionKind),
		managed.WithExternalConnecter(&connector{kube: mgr.GetClient(), usage: t, newDB: sql.NewMySQLDB}),
		managed.WithLogger(l.WithValues("controller", name)),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))))

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		For(&v1alpha1.Database{}).
		Complete(r)
}

type connector struct {
	kube  client.Client
	usage resource.Tracker
	newDB func(creds map[string][]byte) sql.DB
}

func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) {
	cr, ok := mg.(*v1alpha1.Database)
	if !ok {
		return nil, errors.New(errNotDatabase)
	}

	if err := c.usage.Track(ctx, mg); err != nil {
		return nil, errors.Wrap(err, errTrackPCUsage)
	}

	// ProviderConfigReference could theoretically be nil, but in practice the
	// DefaultProviderConfig initializer will set it before we get here.
	pc := &v1alpha1.ProviderConfig{}
	if err := c.kube.Get(ctx, types.NamespacedName{Name: cr.GetProviderConfigReference().Name}, pc); err != nil {
		return nil, errors.Wrap(err, errGetPC)
	}

	// We don't need to check the credentials source because we currently only
	// support one source (MySQLConnectionSecret), which is required and
	// enforced by the ProviderConfig schema.
	ref := pc.Spec.Credentials.ConnectionSecretRef
	if ref == nil {
		return nil, errors.New(errNoSecretRef)
	}

	s := &v1.Secret{}
	if err := c.kube.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, s); err != nil {
		return nil, errors.Wrap(err, errGetSecret)
	}

	return &external{db: c.newDB(s.Data)}, nil
}

type external struct{ db sql.DB }

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.Database)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotDatabase)
	}

	var name string
	query := "SELECT schema_name FROM information_schema.schemata WHERE schema_name = ?"
	err := c.db.Scan(ctx, sql.Query{String: query, Parameters: []interface{}{meta.GetExternalName(cr)}}, &name)
	if sql.IsNoRows(err) {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errSelectDB)
	}

	cr.SetConditions(runtimev1alpha1.Available())

	return managed.ExternalObservation{
		ResourceExists: true,

		// TODO(negz): Support these when we have anything to update.
		ResourceLateInitialized: false,
		ResourceUpToDate:        true,
	}, nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {

	cr, ok := mg.(*v1alpha1.Database)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotDatabase)
	}

	err := c.db.Exec(ctx, sql.Query{String: "CREATE DATABASE " + quoteIdentifier(meta.GetExternalName(cr))})
	return managed.ExternalCreation{}, errors.Wrap(err, errCreateDB)
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	// TODO(negz): Support updates once we have anything to update.
	return managed.ExternalUpdate{}, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) error {
	cr, ok := mg.(*v1alpha1.Database)
	if !ok {
		return errors.New(errNotDatabase)
	}

	err := c.db.Exec(ctx, sql.Query{String: "DROP DATABASE " + quoteIdentifier(meta.GetExternalName(cr))})
	return errors.Wrap(resource.Ignore(c.db.IsDoesNotExist, err), errDropDB)
}

func quoteIdentifier(id string) string {
	return "`" + strings.ReplaceAll(id, "`", "``") + "`"
}
