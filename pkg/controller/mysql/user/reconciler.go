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

package user

import (
	"context"
	"fmt"
	"strings"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	xpcontroller "github.com/crossplane/crossplane-runtime/pkg/controller"
	"github.com/crossplane/crossplane-runtime/pkg/event"
	"github.com/crossplane/crossplane-runtime/pkg/feature"
	"github.com/crossplane/crossplane-runtime/pkg/meta"
	"github.com/crossplane/crossplane-runtime/pkg/password"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/resource"

	"github.com/crossplane-contrib/provider-sql/apis/mysql/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/mysql"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
	"github.com/crossplane-contrib/provider-sql/pkg/controller/mysql/tls"
)

const (
	errTrackPCUsage = "cannot track ProviderConfig usage"
	errGetPC        = "cannot get ProviderConfig"
	errNoSecretRef  = "ProviderConfig does not reference a credentials Secret"
	errGetSecret    = "cannot get credentials Secret"
	errTLSConfig    = "cannot load TLS config"

	errNotUser                 = "managed resource is not a User custom resource"
	errSelectUser              = "cannot select user"
	errCreateUser              = "cannot create user"
	errDropUser                = "cannot drop user"
	errUpdateUser              = "cannot update user"
	errGetPasswordSecretFailed = "cannot get password secret"
	errCompareResourceOptions  = "cannot compare desired and observed resource options"

	maxConcurrency = 5
)

// Setup adds a controller that reconciles User managed resources.
func Setup(mgr ctrl.Manager, o xpcontroller.Options) error {
	name := managed.ControllerName(v1alpha1.UserGroupKind)

	t := resource.NewProviderConfigUsageTracker(mgr.GetClient(), &v1alpha1.ProviderConfigUsage{})
	reconcilerOptions := []managed.ReconcilerOption{
		managed.WithExternalConnecter(&connector{kube: mgr.GetClient(), usage: t, newDB: mysql.New}),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithPollInterval(o.PollInterval),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))),
	}
	if o.Features.Enabled(feature.EnableBetaManagementPolicies) {
		reconcilerOptions = append(reconcilerOptions, managed.WithManagementPolicies())
	}
	r := managed.NewReconciler(mgr,
		resource.ManagedKind(v1alpha1.UserGroupVersionKind),
		reconcilerOptions...,
	)
	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		For(&v1alpha1.User{}).
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

func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) {
	cr, ok := mg.(*v1alpha1.User)
	if !ok {
		return nil, errors.New(errNotUser)
	}

	if err := c.usage.Track(ctx, mg); err != nil {
		return nil, errors.Wrap(err, errTrackPCUsage)
	}

	// ProviderConfigReference could theoretically be nil, but in practice the
	// DefaultProviderConfig initializer will set it before we get here.
	providerConfigName := cr.GetProviderConfigReference().Name
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

	return &external{
		db:   c.newDB(s.Data, tlsName, cr.Spec.ForProvider.BinLog),
		kube: c.kube,
	}, nil
}

type external struct {
	db   xsql.DB
	kube client.Client
}

func handleClause(clause string, value *int, out *[]string) {
	// If clause is not set (nil pointer), do not push a setting.
	// This means the default is applied.
	if value == nil {
		return
	}

	*out = append(*out, fmt.Sprintf("%s %d", clause, *value))
}

func resourceOptionsToClauses(r *v1alpha1.ResourceOptions) []string {
	// Never copy user inputted data to this string. These values are
	// passed directly into the query.
	ro := []string{}

	if r == nil {
		return ro
	}

	handleClause("MAX_QUERIES_PER_HOUR", r.MaxQueriesPerHour, &ro)
	handleClause("MAX_UPDATES_PER_HOUR", r.MaxUpdatesPerHour, &ro)
	handleClause("MAX_CONNECTIONS_PER_HOUR", r.MaxConnectionsPerHour, &ro)
	handleClause("MAX_USER_CONNECTIONS", r.MaxUserConnections, &ro)

	return ro
}

func changedResourceOptions(existing []string, desired []string) ([]string, error) {
	out := []string{}

	// Make sure existing observation has at least as many items as
	// desired. If it does not, then we cannot safely compare
	// resource options.
	if len(existing) < len(desired) {
		return nil, errors.New(errCompareResourceOptions)
	}

	// The input slices here are outputted by resourceOptionsToClauses above.
	// Because these are created by repeated calls to negateClause in the
	// same order, we can rely on each clause being in the same array
	// position in the 'desired' and 'existing' inputs.

	for i, v := range desired {
		if v != existing[i] {
			out = append(out, v)
		}
	}
	return out, nil
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.User)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotUser)
	}

	username, host := mysql.SplitUserHost(meta.GetExternalName(cr))

	observed := &v1alpha1.UserParameters{
		ResourceOptions: &v1alpha1.ResourceOptions{},
	}

	query := "SELECT " +
		"max_questions, " +
		"max_updates, " +
		"max_connections, " +
		"max_user_connections " +
		"FROM mysql.user WHERE User = ? AND Host = ?"
	err := c.db.Scan(ctx,
		xsql.Query{
			String: query,
			Parameters: []interface{}{
				username,
				host,
			},
		},
		&observed.ResourceOptions.MaxQueriesPerHour,
		&observed.ResourceOptions.MaxUpdatesPerHour,
		&observed.ResourceOptions.MaxConnectionsPerHour,
		&observed.ResourceOptions.MaxUserConnections,
	)
	if xsql.IsNoRows(err) {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errSelectUser)
	}

	_, pwdChanged, err := c.getPassword(ctx, cr)
	if err != nil {
		return managed.ExternalObservation{}, err
	}

	cr.Status.AtProvider.ResourceOptionsAsClauses = resourceOptionsToClauses(observed.ResourceOptions)

	cr.SetConditions(xpv1.Available())

	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: !pwdChanged && upToDate(observed, &cr.Spec.ForProvider),
	}, nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.User)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotUser)
	}

	cr.SetConditions(xpv1.Creating())

	username, host := mysql.SplitUserHost(meta.GetExternalName(cr))
	pw, _, err := c.getPassword(ctx, cr)
	if err != nil {
		return managed.ExternalCreation{}, err
	}

	if pw == "" {
		pw, err = password.Generate()
		if err != nil {
			return managed.ExternalCreation{}, err
		}
	}

	ro := resourceOptionsToClauses(cr.Spec.ForProvider.ResourceOptions)
	if err := c.executeCreateUserQuery(ctx, username, host, ro, pw); err != nil {
		return managed.ExternalCreation{}, err
	}

	if len(ro) != 0 {
		cr.Status.AtProvider.ResourceOptionsAsClauses = ro
	}

	return managed.ExternalCreation{
		ConnectionDetails: c.db.GetConnectionDetails(username, pw),
	}, nil
}

func (c *external) executeCreateUserQuery(ctx context.Context, username string, host string, resourceOptionsClauses []string, pw string) error {
	resourceOptions := ""
	if len(resourceOptionsClauses) != 0 {
		resourceOptions = fmt.Sprintf(" WITH %s", strings.Join(resourceOptionsClauses, " "))
	}

	query := fmt.Sprintf(
		"CREATE USER %s@%s IDENTIFIED BY %s%s",
		mysql.QuoteValue(username),
		mysql.QuoteValue(host),
		mysql.QuoteValue(pw),
		resourceOptions,
	)

	if err := mysql.ExecWrapper(ctx, c.db, mysql.ExecQuery{Query: query, ErrorValue: errCreateUser}); err != nil {
		return err
	}

	return nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.User)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotUser)
	}

	username, host := mysql.SplitUserHost(meta.GetExternalName(cr))

	ro := resourceOptionsToClauses(cr.Spec.ForProvider.ResourceOptions)
	rochanged, err := changedResourceOptions(cr.Status.AtProvider.ResourceOptionsAsClauses, ro)
	if err != nil {
		return managed.ExternalUpdate{}, errors.Wrap(err, errUpdateUser)
	}

	if len(rochanged) > 0 {
		resourceOptions := fmt.Sprintf("WITH %s", strings.Join(ro, " "))

		query := fmt.Sprintf(
			"ALTER USER %s@%s %s",
			mysql.QuoteValue(username),
			mysql.QuoteValue(host),
			resourceOptions,
		)
		if err := mysql.ExecWrapper(ctx, c.db, mysql.ExecQuery{Query: query, ErrorValue: errUpdateUser}); err != nil {
			return managed.ExternalUpdate{}, err
		}

		cr.Status.AtProvider.ResourceOptionsAsClauses = ro
	}

	connectionDetails, err := c.UpdatePassword(ctx, cr, username, host)
	if err != nil {
		return managed.ExternalUpdate{}, err
	}

	if len(connectionDetails) > 0 {
		return managed.ExternalUpdate{ConnectionDetails: connectionDetails}, nil
	}

	return managed.ExternalUpdate{}, nil
}

func (c *external) UpdatePassword(ctx context.Context, cr *v1alpha1.User, username, host string) (managed.ConnectionDetails, error) {
	pw, pwchanged, err := c.getPassword(ctx, cr)
	if err != nil {
		return managed.ConnectionDetails{}, err
	}

	if pwchanged {
		query := fmt.Sprintf("ALTER USER %s@%s IDENTIFIED BY %s", mysql.QuoteValue(username), mysql.QuoteValue(host), mysql.QuoteValue(pw))
		if err := mysql.ExecWrapper(ctx, c.db, mysql.ExecQuery{Query: query, ErrorValue: errUpdateUser}); err != nil {
			return managed.ConnectionDetails{}, err
		}

		return c.db.GetConnectionDetails(username, pw), nil
	}

	return managed.ConnectionDetails{}, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) error {
	cr, ok := mg.(*v1alpha1.User)
	if !ok {
		return errors.New(errNotUser)
	}

	cr.SetConditions(xpv1.Deleting())

	username, host := mysql.SplitUserHost(meta.GetExternalName(cr))

	query := fmt.Sprintf("DROP USER IF EXISTS %s@%s", mysql.QuoteValue(username), mysql.QuoteValue(host))
	if err := mysql.ExecWrapper(ctx, c.db, mysql.ExecQuery{Query: query, ErrorValue: errDropUser}); err != nil {
		return err
	}

	return nil
}

func upToDate(observed *v1alpha1.UserParameters, desired *v1alpha1.UserParameters) bool {
	if desired.ResourceOptions == nil {
		// Return true if there are no desired ResourceOptions
		return true
	}
	if observed.ResourceOptions.MaxQueriesPerHour != desired.ResourceOptions.MaxQueriesPerHour {
		return false
	}
	if observed.ResourceOptions.MaxUpdatesPerHour != desired.ResourceOptions.MaxUpdatesPerHour {
		return false
	}
	if observed.ResourceOptions.MaxConnectionsPerHour != desired.ResourceOptions.MaxConnectionsPerHour {
		return false
	}
	if observed.ResourceOptions.MaxUserConnections != desired.ResourceOptions.MaxUserConnections {
		return false
	}
	return true
}
