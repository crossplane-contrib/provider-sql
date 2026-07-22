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

	"github.com/crossplane/crossplane-runtime/v2/pkg/statemetrics"
	"github.com/pkg/errors"
	metav1 "k8s.io/apimachinery/pkg/apis/meta/v1"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	xpv1 "github.com/crossplane/crossplane-runtime/v2/apis/common/v1"
	xpcontroller "github.com/crossplane/crossplane-runtime/v2/pkg/controller"
	"github.com/crossplane/crossplane-runtime/v2/pkg/event"
	"github.com/crossplane/crossplane-runtime/v2/pkg/feature"
	"github.com/crossplane/crossplane-runtime/v2/pkg/meta"
	"github.com/crossplane/crossplane-runtime/v2/pkg/password"
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

	errSelectUser                = "cannot select user"
	errCreateUser                = "cannot create user"
	errDropUser                  = "cannot drop user"
	errUpdateUser                = "cannot update user"
	errGetPasswordSecretFailed   = "cannot get password secret"
	errGetConnectionSecretFailed = "cannot get connection secret"
	errCompareResourceOptions    = "cannot compare desired and observed resource options"

	maxConcurrency = 5
)

// Setup adds a controller that reconciles Database managed resources.
func Setup(mgr ctrl.Manager, o xpcontroller.Options) error {
	name := managed.ControllerName(namespacedv1alpha1.UserGroupKind)

	t := resource.NewProviderConfigUsageTracker(mgr.GetClient(), &namespacedv1alpha1.ProviderConfigUsage{})

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
		resource.ManagedKind(namespacedv1alpha1.UserGroupVersionKind),
		reconcilerOptions...,
	)
	if err := mgr.Add(statemetrics.NewMRStateRecorder(
		mgr.GetClient(), o.Logger, o.MetricOptions.MRStateMetrics,
		&namespacedv1alpha1.UserList{}, o.MetricOptions.PollStateMetricInterval,
	)); err != nil {
		return err
	}
	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		For(&namespacedv1alpha1.User{}).
		WithOptions(controller.Options{
			MaxConcurrentReconciles: maxConcurrency,
		}).
		Complete(r)
}

type connector struct {
	kube  client.Client
	track func(ctx context.Context, mg resource.ModernManaged) error
	newDB func(creds map[string][]byte, tls *string, binlog *bool) xsql.DB
}

var _ managed.TypedExternalConnector[*namespacedv1alpha1.User] = &connector{}

func (c *connector) Connect(ctx context.Context, mg *namespacedv1alpha1.User) (managed.TypedExternalClient[*namespacedv1alpha1.User], error) {
	if err := c.track(ctx, mg); err != nil {
		return nil, errors.Wrap(err, errTrackPCUsage)
	}

	// ProviderConfigReference could theoretically be nil, but in practice the
	// DefaultProviderConfig initializer will set it before we get here.
	providerInfo, err := provider.GetProviderConfig(ctx, c.kube, mg)
	if err != nil {
		return nil, err
	}

	tlsName, err := tls.LoadConfig(ctx, c.kube, providerInfo.ProviderConfigName, providerInfo.TLS, providerInfo.TLSConfig)
	if err != nil {
		return nil, errors.Wrap(err, errTLSConfig)
	}

	return &external{
		db:   c.newDB(providerInfo.SecretData, tlsName, mg.Spec.ForProvider.BinLog),
		kube: c.kube,
	}, nil
}

type external struct {
	db   xsql.DB
	kube client.Client
}

var _ managed.TypedExternalClient[*namespacedv1alpha1.User] = &external{}

func handleClause(clause string, value *int, out *[]string) {
	// If clause is not set (nil pointer), do not push a setting.
	// This means the default is applied.
	if value == nil {
		return
	}

	*out = append(*out, fmt.Sprintf("%s %d", clause, *value))
}

func resourceOptionsToClauses(r *namespacedv1alpha1.ResourceOptions) []string {
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

func (c *external) Observe(ctx context.Context, mg *namespacedv1alpha1.User) (managed.ExternalObservation, error) {
	username, host := mysql.SplitUserHost(meta.GetExternalName(mg))

	observed := &namespacedv1alpha1.UserParameters{
		ResourceOptions: &namespacedv1alpha1.ResourceOptions{},
	}

	var pluginName, authString string
	query := "SELECT " +
		"max_questions, " +
		"max_updates, " +
		"max_connections, " +
		"max_user_connections, " +
		"plugin, " +
		"authentication_string " +
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
		&pluginName,
		&authString,
	)
	if xsql.IsNoRows(err) {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errSelectUser)
	}

	// Hydrate observed.AuthenticationPlugin when the spec opts into a plugin, or
	// when the user carries a non-default-password plugin. Password plugins
	// (caching_sha2_password, mysql_native_password, sha256_password) store an
	// opaque hash in authentication_string and are the implicit default when the
	// user was created with IDENTIFIED BY '<pwd>', so when the spec does NOT
	// request a plugin we leave AuthenticationPlugin nil to match a spec that
	// omits it. When the spec DOES request one — including a password plugin — we
	// must hydrate the observed value, otherwise upToDate() can never converge and
	// the resource reconciles forever.
	specifiesPlugin := mg.Spec.ForProvider.AuthenticationPlugin != nil
	if pluginName != "" && (specifiesPlugin || !isPasswordPlugin(pluginName)) {
		observed.AuthenticationPlugin = &namespacedv1alpha1.AuthenticationPlugin{Name: pluginName}
		if authString != "" {
			as := authString
			observed.AuthenticationPlugin.AuthString = &as
		}
	}

	// When the user is configured with a non-default auth plugin (e.g., AWS
	// IAM auth), there is no password to compare. Skip the password drift
	// check so the resource doesn't churn on every reconcile.
	pwdChanged := false
	if mg.Spec.ForProvider.AuthenticationPlugin == nil {
		_, pwdChanged, err = c.getPassword(ctx, mg)
		if err != nil {
			return managed.ExternalObservation{}, err
		}
	}

	mg.Status.AtProvider.ResourceOptionsAsClauses = resourceOptionsToClauses(observed.ResourceOptions)
	mg.Status.AtProvider.AuthenticationPlugin = observed.AuthenticationPlugin

	mg.SetConditions(xpv1.Available())

	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: !pwdChanged && upToDate(observed, &mg.Spec.ForProvider),
	}, nil
}

// isPasswordPlugin reports whether the given MySQL plugin name is one of the
// password-based authentication plugins. Users created with IDENTIFIED BY
// '<pwd>' are assigned a password plugin (caching_sha2_password is the MySQL
// 8.0 default; mysql_native_password and sha256_password are common). For
// these we treat AuthenticationPlugin as nil in the observed state because
// they represent "use a password" rather than an opt-in plugin choice.
func isPasswordPlugin(name string) bool {
	switch name {
	case "caching_sha2_password", "mysql_native_password", "sha256_password":
		return true
	}
	return false
}

// authPluginEqual reports whether observed and desired AuthenticationPlugin
// configurations match. nil/nil = match. Mismatched nil-ness = drift. Same
// nil-ness with non-nil values: names must match, and authStrings must both
// be nil OR both equal.
func authPluginEqual(observed, desired *namespacedv1alpha1.AuthenticationPlugin) bool {
	if desired == nil {
		return observed == nil
	}
	if observed == nil {
		return false
	}
	if observed.Name != desired.Name {
		return false
	}
	if (observed.AuthString == nil) != (desired.AuthString == nil) {
		return false
	}
	if observed.AuthString != nil && *observed.AuthString != *desired.AuthString {
		return false
	}
	return true
}

func (c *external) Create(ctx context.Context, mg *namespacedv1alpha1.User) (managed.ExternalCreation, error) {
	mg.SetConditions(xpv1.Creating())

	username, host := mysql.SplitUserHost(meta.GetExternalName(mg))
	ro := resourceOptionsToClauses(mg.Spec.ForProvider.ResourceOptions)

	// When AuthenticationPlugin is set, the user has no password — skip
	// password generation, emit IDENTIFIED WITH ..., and return connection
	// details without a password.
	if ap := mg.Spec.ForProvider.AuthenticationPlugin; ap != nil {
		if err := c.executeCreateUserWithPluginQuery(ctx, username, host, ro, ap); err != nil {
			return managed.ExternalCreation{}, err
		}
		if len(ro) != 0 {
			mg.Status.AtProvider.ResourceOptionsAsClauses = ro
		}
		return managed.ExternalCreation{
			ConnectionDetails: c.db.GetConnectionDetails(username, ""),
		}, nil
	}

	pw, _, err := c.getPassword(ctx, mg)
	if err != nil {
		return managed.ExternalCreation{}, err
	}

	if pw == "" {
		pw, err = password.Generate()
		if err != nil {
			return managed.ExternalCreation{}, err
		}
	}

	if err := c.executeCreateUserQuery(ctx, username, host, ro, pw); err != nil {
		return managed.ExternalCreation{}, err
	}

	if len(ro) != 0 {
		mg.Status.AtProvider.ResourceOptionsAsClauses = ro
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

// executeCreateUserWithPluginQuery emits CREATE USER ... IDENTIFIED WITH
// <plugin> [AS '<authString>'] [WITH <resourceOptions>]. The plugin name is
// wrapped in QuoteIdentifier (backticks) since MySQL parses it as an
// identifier; the auth string is value-quoted as a regular string.
func (c *external) executeCreateUserWithPluginQuery(ctx context.Context, username string, host string, resourceOptionsClauses []string, ap *namespacedv1alpha1.AuthenticationPlugin) error {
	resourceOptions := ""
	if len(resourceOptionsClauses) != 0 {
		resourceOptions = fmt.Sprintf(" WITH %s", strings.Join(resourceOptionsClauses, " "))
	}

	authString := ""
	if ap.AuthString != nil && *ap.AuthString != "" {
		authString = fmt.Sprintf(" AS %s", mysql.QuoteValue(*ap.AuthString))
	}

	query := fmt.Sprintf(
		"CREATE USER %s@%s IDENTIFIED WITH %s%s%s",
		mysql.QuoteValue(username),
		mysql.QuoteValue(host),
		mysql.QuoteIdentifier(ap.Name),
		authString,
		resourceOptions,
	)

	if err := mysql.ExecWrapper(ctx, c.db, mysql.ExecQuery{Query: query, ErrorValue: errCreateUser}); err != nil {
		return err
	}

	return nil
}

func (c *external) Update(ctx context.Context, mg *namespacedv1alpha1.User) (managed.ExternalUpdate, error) {
	username, host := mysql.SplitUserHost(meta.GetExternalName(mg))

	if err := c.updateResourceOptions(ctx, mg, username, host); err != nil {
		return managed.ExternalUpdate{}, err
	}

	// Detect AuthenticationPlugin drift using the last-observed value cached
	// in Status.AtProvider by Observe. Three transitions are handled:
	//   1. password → plugin: ALTER USER ... IDENTIFIED WITH <plugin> [AS '<authString>']
	//   2. plugin → password: fall through to UpdatePassword below; it emits
	//      ALTER USER ... IDENTIFIED BY '<pw>' which both sets the password
	//      AND restores the default password plugin.
	//   3. plugin → different plugin or same plugin with different authString:
	//      ALTER USER ... IDENTIFIED WITH <plugin> [AS '<authString>']
	// The API-level XValidation rule enforces mutual exclusivity of
	// PasswordSecretRef and AuthenticationPlugin, so the spec always
	// represents one mode or the other.
	desiredPlugin := mg.Spec.ForProvider.AuthenticationPlugin
	observedPlugin := mg.Status.AtProvider.AuthenticationPlugin
	if !authPluginEqual(observedPlugin, desiredPlugin) && desiredPlugin != nil {
		if err := c.executeAlterUserWithPluginQuery(ctx, username, host, desiredPlugin); err != nil {
			return managed.ExternalUpdate{}, err
		}
		mg.Status.AtProvider.AuthenticationPlugin = desiredPlugin
		// Connection details for a plugin-auth user carry no password.
		return managed.ExternalUpdate{
			ConnectionDetails: c.db.GetConnectionDetails(username, ""),
		}, nil
	}

	connectionDetails, err := c.UpdatePassword(ctx, mg, username, host)
	if err != nil {
		return managed.ExternalUpdate{}, err
	}

	// If transitioning from plugin → password, clear the cached observed
	// plugin so the next reconcile starts clean.
	if desiredPlugin == nil && observedPlugin != nil {
		mg.Status.AtProvider.AuthenticationPlugin = nil
	}

	if len(connectionDetails) > 0 {
		return managed.ExternalUpdate{ConnectionDetails: connectionDetails}, nil
	}

	return managed.ExternalUpdate{}, nil
}

// updateResourceOptions emits ALTER USER ... WITH <opts> when the
// resource-options clauses observed by Status differ from the desired spec.
// Extracted from Update to keep that function below the gocyclo threshold.
func (c *external) updateResourceOptions(ctx context.Context, mg *namespacedv1alpha1.User, username, host string) error {
	ro := resourceOptionsToClauses(mg.Spec.ForProvider.ResourceOptions)
	rochanged, err := changedResourceOptions(mg.Status.AtProvider.ResourceOptionsAsClauses, ro)
	if err != nil {
		return errors.Wrap(err, errUpdateUser)
	}
	if len(rochanged) == 0 {
		return nil
	}

	query := fmt.Sprintf(
		"ALTER USER %s@%s WITH %s",
		mysql.QuoteValue(username),
		mysql.QuoteValue(host),
		strings.Join(ro, " "),
	)
	if err := mysql.ExecWrapper(ctx, c.db, mysql.ExecQuery{Query: query, ErrorValue: errUpdateUser}); err != nil {
		return err
	}
	mg.Status.AtProvider.ResourceOptionsAsClauses = ro
	return nil
}

// executeAlterUserWithPluginQuery emits ALTER USER ... IDENTIFIED WITH
// <plugin> [AS '<authString>']. Same identifier-vs-value quoting rules as
// the Create-time helper: plugin name is QuoteIdentifier (backticked) to
// guard against injection while still parsing as an identifier, authString
// is value-quoted as a regular string.
func (c *external) executeAlterUserWithPluginQuery(ctx context.Context, username string, host string, ap *namespacedv1alpha1.AuthenticationPlugin) error {
	authString := ""
	if ap.AuthString != nil && *ap.AuthString != "" {
		authString = fmt.Sprintf(" AS %s", mysql.QuoteValue(*ap.AuthString))
	}

	query := fmt.Sprintf(
		"ALTER USER %s@%s IDENTIFIED WITH %s%s",
		mysql.QuoteValue(username),
		mysql.QuoteValue(host),
		mysql.QuoteIdentifier(ap.Name),
		authString,
	)

	if err := mysql.ExecWrapper(ctx, c.db, mysql.ExecQuery{Query: query, ErrorValue: errUpdateUser}); err != nil {
		return err
	}
	return nil
}

func (c *external) UpdatePassword(ctx context.Context, cr *namespacedv1alpha1.User, username, host string) (managed.ConnectionDetails, error) {
	// Users authenticated via a non-default plugin (e.g., AWS IAM auth)
	// have no static password to manage. Skip the ALTER USER call so the
	// provider doesn't downgrade the user back to native password auth.
	if cr.Spec.ForProvider.AuthenticationPlugin != nil {
		return managed.ConnectionDetails{}, nil
	}

	pw, pwchanged, err := c.getPassword(ctx, cr)
	if err != nil {
		return managed.ConnectionDetails{}, err
	}

	if pwchanged {
		if pw == "" {
			pw, err = password.Generate()
			if err != nil {
				return managed.ConnectionDetails{}, err
			}
		}
		query := fmt.Sprintf("ALTER USER %s@%s IDENTIFIED BY %s", mysql.QuoteValue(username), mysql.QuoteValue(host), mysql.QuoteValue(pw))
		if err := mysql.ExecWrapper(ctx, c.db, mysql.ExecQuery{Query: query, ErrorValue: errUpdateUser}); err != nil {
			return managed.ConnectionDetails{}, err
		}
		now := metav1.Now()
		cr.Status.AtProvider.LastPasswordChange = &now

		return c.db.GetConnectionDetails(username, pw), nil
	}

	return managed.ConnectionDetails{}, nil
}

func (c *external) Disconnect(ctx context.Context) error {
	return nil
}

func (c *external) Delete(ctx context.Context, mg *namespacedv1alpha1.User) (managed.ExternalDelete, error) {
	mg.SetConditions(xpv1.Deleting())

	username, host := mysql.SplitUserHost(meta.GetExternalName(mg))

	query := fmt.Sprintf("DROP USER IF EXISTS %s@%s", mysql.QuoteValue(username), mysql.QuoteValue(host))
	if err := mysql.ExecWrapper(ctx, c.db, mysql.ExecQuery{Query: query, ErrorValue: errDropUser}); err != nil {
		return managed.ExternalDelete{}, err
	}

	return managed.ExternalDelete{}, nil
}

func upToDate(observed *namespacedv1alpha1.UserParameters, desired *namespacedv1alpha1.UserParameters) bool {
	// AuthenticationPlugin drift — checked first so a plugin change is detected
	// even when ResourceOptions are nil.
	if !authPluginEqual(observed.AuthenticationPlugin, desired.AuthenticationPlugin) {
		return false
	}
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
