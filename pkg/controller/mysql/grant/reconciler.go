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

package grant

import (
	"context"
	"fmt"
	"regexp"
	"sort"
	"strings"

	mysqldriver "github.com/go-sql-driver/mysql"
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

	errNotGrant     = "managed resource is not a Grant custom resource"
	errCreateGrant  = "cannot create grant"
	errRevokeGrant  = "cannot revoke grant"
	errCurrentGrant = "cannot show current grants"

	allPrivileges      = "ALL PRIVILEGES"
	errCodeNoSuchGrant = 1141
	maxConcurrency     = 5
)

var (
	grantRegex = regexp.MustCompile(`^GRANT (.+) ON (\S+)\.(\S+) TO \S+@\S+?(\sWITH GRANT OPTION)?$`)
)

// Setup adds a controller that reconciles Grant managed resources.
func Setup(mgr ctrl.Manager, o xpcontroller.Options) error {
	name := managed.ControllerName(v1alpha1.GrantGroupKind)

	t := resource.NewProviderConfigUsageTracker(mgr.GetClient(), &v1alpha1.ProviderConfigUsage{})
	reconcilerOptions := []managed.ReconcilerOption{
		managed.WithExternalConnecter(&connector{kube: mgr.GetClient(), usage: t, newDB: mysql.New}),
		managed.WithReferenceResolver(managed.NewAPISimpleReferenceResolver(mgr.GetClient())),
		managed.WithLogger(o.Logger.WithValues("controller", name)),
		managed.WithPollInterval(o.PollInterval),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))),
	}
	if o.Features.Enabled(feature.EnableBetaManagementPolicies) {
		reconcilerOptions = append(reconcilerOptions, managed.WithManagementPolicies())
	}
	r := managed.NewReconciler(mgr,
		resource.ManagedKind(v1alpha1.GrantGroupVersionKind),
		reconcilerOptions...,
	)
	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		For(&v1alpha1.Grant{}).
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
	cr, ok := mg.(*v1alpha1.Grant)
	if !ok {
		return nil, errors.New(errNotGrant)
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

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.Grant)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotGrant)
	}

	username, host := mysql.SplitUserHost(*cr.Spec.ForProvider.User)
	dbname := defaultIdentifier(cr.Spec.ForProvider.Database)
	table := defaultIdentifier(cr.Spec.ForProvider.Table)

	observedPrivileges, result, err := c.getPrivileges(ctx, username, host, dbname, table)
	if err != nil {
		return managed.ExternalObservation{}, err
	}
	if result != nil {
		return *result, nil
	}

	cr.Status.AtProvider.Privileges = observedPrivileges

	desiredPrivileges := cr.Spec.ForProvider.Privileges.ToStringSlice()
	toGrant, toRevoke := diffPermissions(desiredPrivileges, observedPrivileges)

	cr.SetConditions(xpv1.Available())

	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: len(toGrant) == 0 && len(toRevoke) == 0,
	}, nil
}

func defaultIdentifier(identifier *string) string {
	if identifier != nil && *identifier != "*" {
		return mysql.QuoteIdentifier(*identifier)
	}

	return "*"
}

func parseGrant(grant, dbname string, table string) (privileges []string) {
	matches := grantRegex.FindStringSubmatch(grant)
	if len(matches) == 5 && matches[2] == dbname && matches[3] == table {
		privileges := strings.Split(matches[1], ", ")

		if matches[4] != "" {
			privileges = append(privileges, "GRANT OPTION")
		}

		return privileges
	}

	return nil
}

func (c *external) getPrivileges(ctx context.Context, username, host, dbname, table string) ([]string, *managed.ExternalObservation, error) {
	privileges, err := c.parseGrantRows(ctx, username, host, dbname, table)
	if err != nil {
		var myErr *mysqldriver.MySQLError
		if errors.As(err, &myErr) && myErr.Number == errCodeNoSuchGrant {
			// The user doesn't (yet) exist and therefore no grants either
			return nil, &managed.ExternalObservation{ResourceExists: false}, nil
		}

		return nil, nil, errors.Wrap(err, errCurrentGrant)
	}

	// In mysql when all grants are revoked from user, it still grants usage (meaning no
	// privileges) on *.* So the grant can be considered as non existent, just like when
	// privileges slice is nil/empty
	// https://dev.mysql.com/doc/refman/8.0/en/privileges-provided.html#priv_usage
	var ret []string
	for _, p := range privileges {
		if p != "USAGE" {
			ret = append(ret, p)
		}
	}

	if ret == nil {
		return nil, &managed.ExternalObservation{ResourceExists: false}, nil
	}

	return ret, nil, nil
}

func (c *external) parseGrantRows(ctx context.Context, username, host, dbname, table string) ([]string, error) {
	query := fmt.Sprintf("SHOW GRANTS FOR %s@%s", mysql.QuoteValue(username), mysql.QuoteValue(host))
	rows, err := c.db.Query(ctx, xsql.Query{String: query})

	if err != nil {
		return nil, err
	}
	defer rows.Close() //nolint:errcheck

	var privileges []string
	for rows.Next() {
		var grant string
		if err := rows.Scan(&grant); err != nil {
			return nil, err
		}
		p := parseGrant(grant, dbname, table)

		if p != nil {
			// found the grant we were looking for
			privileges = p
			break
		}
	}

	if err := rows.Err(); err != nil {
		return nil, err
	}

	return privileges, nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.Grant)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotGrant)
	}

	username, host := mysql.SplitUserHost(*cr.Spec.ForProvider.User)
	dbname := defaultIdentifier(cr.Spec.ForProvider.Database)
	table := defaultIdentifier(cr.Spec.ForProvider.Table)

	privileges, grantOption := getPrivilegesString(cr.Spec.ForProvider.Privileges.ToStringSlice())
	query := createGrantQuery(privileges, dbname, username, host, table, grantOption)

	if err := mysql.ExecWrapper(ctx, c.db, mysql.ExecQuery{Query: query, ErrorValue: errCreateGrant}); err != nil {
		return managed.ExternalCreation{}, err
	}
	return managed.ExternalCreation{}, nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.Grant)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotGrant)
	}

	username, host := mysql.SplitUserHost(*cr.Spec.ForProvider.User)
	dbname := defaultIdentifier(cr.Spec.ForProvider.Database)
	table := defaultIdentifier(cr.Spec.ForProvider.Table)

	observed := cr.Status.AtProvider.Privileges
	desired := cr.Spec.ForProvider.Privileges.ToStringSlice()
	toGrant, toRevoke := diffPermissions(desired, observed)

	if len(toRevoke) > 0 {
		sort.Strings(toRevoke)
		privileges, grantOption := getPrivilegesString(toRevoke)
		query := createRevokeQuery(privileges, dbname, username, host, table, grantOption)
		if err := mysql.ExecWrapper(ctx, c.db,
			mysql.ExecQuery{
				Query: query, ErrorValue: errRevokeGrant,
			}); err != nil {
			return managed.ExternalUpdate{}, err
		}
	}

	if len(toGrant) > 0 {
		sort.Strings(toGrant)
		privileges, grantOption := getPrivilegesString(toGrant)
		query := createGrantQuery(privileges, dbname, username, host, table, grantOption)
		if err := mysql.ExecWrapper(ctx, c.db,
			mysql.ExecQuery{
				Query: query, ErrorValue: errCreateGrant,
			}); err != nil {
			return managed.ExternalUpdate{}, err
		}
	}
	return managed.ExternalUpdate{}, nil
}

// getPrivilegesString returns a privileges string without grant option item and a grantOption boolean
func getPrivilegesString(privileges []string) (string, bool) {
	privilegesWithoutGrantOption := []string{}
	grantOption := false
	for _, p := range privileges {
		if p == "GRANT OPTION" {
			grantOption = true
			continue
		}
		privilegesWithoutGrantOption = append(privilegesWithoutGrantOption, p)
	}
	out := strings.Join(privilegesWithoutGrantOption, ", ")
	return out, grantOption
}

func createRevokeQuery(privileges, dbname, username, host, table string, grantOption bool) string {
	result := fmt.Sprintf("REVOKE %s ON %s.%s FROM %s@%s",
		privileges,
		dbname,
		table,
		mysql.QuoteValue(username),
		mysql.QuoteValue(host),
	)

	if grantOption {
		result = fmt.Sprintf("%s WITH GRANT OPTION", result)
	}

	return result
}

func createGrantQuery(privileges, dbname, username, host, table string, grantOption bool) string {
	result := fmt.Sprintf("GRANT %s ON %s.%s TO %s@%s",
		privileges,
		dbname,
		table,
		mysql.QuoteValue(username),
		mysql.QuoteValue(host),
	)

	if grantOption {
		result = fmt.Sprintf("%s WITH GRANT OPTION", result)
	}

	return result
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) error {
	cr, ok := mg.(*v1alpha1.Grant)
	if !ok {
		return errors.New(errNotGrant)
	}

	username, host := mysql.SplitUserHost(*cr.Spec.ForProvider.User)
	dbname := defaultIdentifier(cr.Spec.ForProvider.Database)
	table := defaultIdentifier(cr.Spec.ForProvider.Table)

	privileges, grantOption := getPrivilegesString(cr.Spec.ForProvider.Privileges.ToStringSlice())
	query := createRevokeQuery(privileges, dbname, username, host, table, grantOption)

	if err := mysql.ExecWrapper(ctx, c.db, mysql.ExecQuery{Query: query, ErrorValue: errRevokeGrant}); err != nil {
		var myErr *mysqldriver.MySQLError
		if errors.As(err, &myErr) && myErr.Number == errCodeNoSuchGrant {
			// MySQL automatically deletes related grants if the user has been deleted
			return nil
		}

		return err
	}

	return nil
}

func diffPermissions(desired, observed []string) ([]string, []string) {
	desiredMap := make(map[string]struct{}, len(desired))
	observedMap := make(map[string]struct{}, len(observed))

	for _, desiredPrivilege := range desired {
		// Special case because ALL is an alias for "ALL PRIVILEGES"
		desiredPrivilegeMapped := strings.ReplaceAll(desiredPrivilege, allPrivileges, "ALL")
		desiredMap[desiredPrivilegeMapped] = struct{}{}
	}
	for _, observedPrivilege := range observed {
		// Special case because ALL is an alias for "ALL PRIVILEGES"
		observedPrivilegeMapped := strings.ReplaceAll(observedPrivilege, allPrivileges, "ALL")
		observedMap[observedPrivilegeMapped] = struct{}{}
	}

	var toGrant []string
	var toRevoke []string

	for desiredPrivilege := range desiredMap {
		if _, ok := observedMap[desiredPrivilege]; !ok {
			toGrant = append(toGrant, desiredPrivilege)
		}
	}

	for observedPrivilege := range observedMap {
		if _, ok := desiredMap[observedPrivilege]; !ok {
			toRevoke = append(toRevoke, observedPrivilege)
		}
	}

	return toGrant, toRevoke
}
