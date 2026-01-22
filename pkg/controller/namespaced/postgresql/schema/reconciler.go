/*
Copyright 2024 The Crossplane Authors.

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

package schema

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
	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
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

	errSelectSchema = "cannot select schema"
	errCreateSchema = "cannot create schema"
	errDropSchema   = "cannot drop schema"
	errNoDatabase   = "database must be specified"
	errAlterSchema  = "cannot alter schema"

	maxConcurrency = 5
)

// Setup adds a controller that reconciles Database managed resources.
func Setup(mgr ctrl.Manager, o xpcontroller.Options) error {
	name := managed.ControllerName(namespacedv1alpha1.SchemaGroupKind)

	t := resource.NewProviderConfigUsageTracker(mgr.GetClient(), &namespacedv1alpha1.ProviderConfigUsage{})

	reconcilerOptions := []managed.ReconcilerOption{
		managed.WithTypedExternalConnector(&connector{kube: mgr.GetClient(), track: t.Track, newDB: postgresql.New}),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithPollInterval(o.PollInterval),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))),
	}
	if o.Features.Enabled(feature.EnableBetaManagementPolicies) {
		reconcilerOptions = append(reconcilerOptions, managed.WithManagementPolicies())
	}
	r := managed.NewReconciler(mgr,
		resource.ManagedKind(namespacedv1alpha1.SchemaGroupVersionKind),
		reconcilerOptions...,
	)
	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		For(&namespacedv1alpha1.Schema{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: maxConcurrency,
		}).
		Complete(r)
}

var _ managed.TypedExternalConnector[*namespacedv1alpha1.Schema] = &connector{}

type connector struct {
	kube  client.Client
	track func(ctx context.Context, mg resource.ModernManaged) error
	newDB func(creds map[string][]byte, database string, sslmode string) xsql.DB
}

func (c *connector) Connect(ctx context.Context, mg *namespacedv1alpha1.Schema) (managed.TypedExternalClient[*namespacedv1alpha1.Schema], error) {
	if err := c.track(ctx, mg); err != nil {
		return nil, errors.Wrap(err, errTrackPCUsage)
	}

	// ProviderConfigReference could theoretically be nil, but in practice the
	// DefaultProviderConfig initializer will set it before we get here.
	providerInfo, err := provider.GetProviderConfig(ctx, c.kube, mg)
	if err != nil {
		return nil, err
	}

	if mg.Spec.ForProvider.Database == nil {
		return nil, errors.New(errNoDatabase)
	}

	return &external{db: c.newDB(providerInfo.SecretData, *mg.Spec.ForProvider.Database, clients.ToString(providerInfo.SSLMode))}, nil
}

var _ managed.TypedExternalClient[*namespacedv1alpha1.Schema] = &external{}

type external struct{ db xsql.DB }

func (c *external) Disconnect(ctx context.Context) error {
	return nil
}

func (c *external) Observe(ctx context.Context, mg *namespacedv1alpha1.Schema) (managed.ExternalObservation, error) {
	// If the Schema exists, it will have all of these properties.
	observed := namespacedv1alpha1.SchemaParameters{
		Role: new(string),
	}

	query := "SELECT rolname FROM pg_catalog.pg_namespace JOIN pg_catalog.pg_roles ON (nspowner=pg_roles.oid) where nspname = $1"

	err := c.db.Scan(ctx, xsql.Query{
		String:     query,
		Parameters: []interface{}{meta.GetExternalName(mg)},
	},
		observed.Role,
	)

	// If the database we try to connect on does not exist then
	// there cannot be an schema in that database either.
	if xsql.IsNoRows(err) || postgresql.IsInvalidCatalog(err) {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errSelectSchema)
	}

	mg.SetConditions(xpv1.Available())

	return managed.ExternalObservation{
		ResourceExists:          true,
		ResourceLateInitialized: lateInit(observed, &mg.Spec.ForProvider),
		ResourceUpToDate:        upToDate(observed, mg.Spec.ForProvider),
	}, nil
}

func (c *external) Create(ctx context.Context, mg *namespacedv1alpha1.Schema) (managed.ExternalCreation, error) { //nolint:gocyclo
	var queries []xsql.Query

	mg.SetConditions(xpv1.Creating())

	createSchemaQueries(mg.Spec.ForProvider, &queries, meta.GetExternalName(mg))

	err := c.db.ExecTx(ctx, queries)
	return managed.ExternalCreation{}, errors.Wrap(err, errCreateSchema)
}

func (c *external) Update(ctx context.Context, mg *namespacedv1alpha1.Schema) (managed.ExternalUpdate, error) { //nolint:gocyclo
	if mg.Spec.ForProvider.Role == nil {
		return managed.ExternalUpdate{}, nil
	}

	var queries []xsql.Query
	updateSchemaQueries(mg.Spec.ForProvider, &queries, meta.GetExternalName(mg))

	err := c.db.ExecTx(ctx, queries)
	return managed.ExternalUpdate{}, errors.Wrap(err, errAlterSchema)
}

func (c *external) Delete(ctx context.Context, mg *namespacedv1alpha1.Schema) (managed.ExternalDelete, error) {
	err := c.db.Exec(ctx, xsql.Query{String: "DROP SCHEMA IF EXISTS " + pq.QuoteIdentifier(meta.GetExternalName(mg))})
	return managed.ExternalDelete{}, errors.Wrap(err, errDropSchema)
}

func upToDate(observed, desired namespacedv1alpha1.SchemaParameters) bool {
	if desired.Role == nil || (observed.Role != nil && *desired.Role == *observed.Role) {
		return true
	}
	return false
}

func lateInit(observed namespacedv1alpha1.SchemaParameters, desired *namespacedv1alpha1.SchemaParameters) bool {
	li := false

	if desired.Role == nil && observed.Role != nil {
		desired.Role = observed.Role
		li = true
	}

	return li
}

func createSchemaQueries(sp namespacedv1alpha1.SchemaParameters, ql *[]xsql.Query, en string) { // nolint: gocyclo

	var b strings.Builder
	b.WriteString("CREATE SCHEMA IF NOT EXISTS ")
	b.WriteString(pq.QuoteIdentifier(en))

	if sp.Role != nil {
		b.WriteString(" AUTHORIZATION ")
		b.WriteString(pq.QuoteIdentifier(*sp.Role))
		b.WriteString(";")
	}

	*ql = append(*ql,
		xsql.Query{String: b.String()},
	)

	if sp.RevokePublicOnSchema != nil && *sp.RevokePublicOnSchema {
		*ql = append(*ql,
			xsql.Query{String: "REVOKE ALL ON SCHEMA PUBLIC FROM PUBLIC;"},
		)
	}

}

func updateSchemaQueries(sp namespacedv1alpha1.SchemaParameters, ql *[]xsql.Query, en string) { // nolint: gocyclo

	var b strings.Builder
	b.WriteString("ALTER SCHEMA ")
	b.WriteString(pq.QuoteIdentifier(en))
	b.WriteString(" OWNER TO ")
	b.WriteString(pq.QuoteIdentifier(*sp.Role))

	*ql = append(*ql,
		xsql.Query{String: b.String()},
	)

	if sp.RevokePublicOnSchema != nil && *sp.RevokePublicOnSchema {
		*ql = append(*ql,
			xsql.Query{String: "REVOKE ALL ON SCHEMA PUBLIC FROM PUBLIC;"},
		)
	}
}
