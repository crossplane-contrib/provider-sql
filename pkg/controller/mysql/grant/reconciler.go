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
	"time"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"
	"sigs.k8s.io/controller-runtime/pkg/controller"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/event"
	"github.com/crossplane/crossplane-runtime/pkg/logging"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/resource"

	"github.com/crossplane-contrib/provider-sql/apis/mysql/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/mysql"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
)

const (
	errTrackPCUsage = "cannot track ProviderConfig usage"
	errGetPC        = "cannot get ProviderConfig"
	errNoSecretRef  = "ProviderConfig does not reference a credentials Secret"
	errGetSecret    = "cannot get credentials Secret"

	errNotGrant     = "managed resource is not a Grant custom resource"
	errCreateGrant  = "cannot create grant"
	errRevokeGrant  = "cannot revoke grant"
	errCurrentGrant = "cannot show current grants"
	errFlushPriv    = "cannot flush privileges"

	allPrivileges      = "ALL PRIVILEGES"
	errCodeNoSuchGrant = 1141
	maxConcurrency     = 5
)

var (
	grantRegex = regexp.MustCompile("^GRANT (.+) ON `(.+)`\\.(.+) TO .+")
)

// Setup adds a controller that reconciles Grant managed resources.
func Setup(mgr ctrl.Manager, l logging.Logger) error {
	name := managed.ControllerName(v1alpha1.GrantGroupKind)

	t := resource.NewProviderConfigUsageTracker(mgr.GetClient(), &v1alpha1.ProviderConfigUsage{})
	r := managed.NewReconciler(mgr,
		resource.ManagedKind(v1alpha1.GrantGroupVersionKind),
		managed.WithExternalConnecter(&connector{kube: mgr.GetClient(), usage: t, newDB: mysql.New}),
		managed.WithReferenceResolver(managed.NewAPISimpleReferenceResolver(mgr.GetClient())),
		managed.WithLogger(l.WithValues("controller", name)),
		managed.WithPollInterval(10*time.Minute),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))))

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
	newDB func(creds map[string][]byte, tls *string) xsql.DB
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

	s := &corev1.Secret{}
	if err := c.kube.Get(ctx, types.NamespacedName{Namespace: ref.Namespace, Name: ref.Name}, s); err != nil {
		return nil, errors.Wrap(err, errGetSecret)
	}

	return &external{
		db:   c.newDB(s.Data, pc.Spec.TLS),
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

	username := *cr.Spec.ForProvider.User
	dbname := *cr.Spec.ForProvider.Database
	table := defaultTable(cr.Spec.ForProvider.Table)

	privileges, result, err := c.getPrivileges(ctx, username, dbname, table)
	if err != nil {
		return managed.ExternalObservation{}, err
	}
	if result != nil {
		return *result, nil
	}

	if !privilegesEqual(cr.Spec.ForProvider.Privileges.ToStringSlice(), privileges) {
		return managed.ExternalObservation{
			ResourceExists:   true,
			ResourceUpToDate: false,
		}, nil
	}

	cr.SetConditions(xpv1.Available())

	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: true,
	}, nil
}

func defaultTable(table *string) string {
	if !(table == nil) {
		return mysql.QuoteIdentifier(*table)
	}
	return "*"
}

func privilegesEqual(a, b []string) bool {
	if len(a) != len(b) {
		return false
	}
	sort.Strings(a)
	sort.Strings(b)

	for i := range a {
		// Special case because ALL is an alias for "ALL PRIVILEGES"
		strA := strings.ReplaceAll(a[i], allPrivileges, "ALL")
		strB := strings.ReplaceAll(b[i], allPrivileges, "ALL")
		if strA != strB {
			return false
		}
	}
	return true
}

func parseGrant(grant, dbname string, table string) (privileges []string) {
	matches := grantRegex.FindStringSubmatch(grant)
	if len(matches) == 4 && matches[2] == dbname && matches[3] == table {
		return strings.Split(matches[1], ", ")
	}
	return nil
}

func (c *external) getPrivileges(ctx context.Context, username, dbname string, table string) ([]string, *managed.ExternalObservation, error) {
	username, host := mysql.SplitUserHost(username)
	query := fmt.Sprintf("SHOW GRANTS FOR %s@%s", mysql.QuoteValue(username), mysql.QuoteValue(host))
	rows, err := c.db.Query(ctx, xsql.Query{String: query})
	if err != nil {
		return nil, nil, errors.Wrap(err, errCurrentGrant)
	}
	defer rows.Close() //nolint:errcheck

	var privileges []string
	for rows.Next() {
		var grant string
		if err := rows.Scan(&grant); err != nil {
			return nil, nil, errors.Wrap(err, errCurrentGrant)
		}
		p := parseGrant(grant, dbname, table)

		if p != nil {
			// found the grant we were looking for
			privileges = p
			break
		}
	}

	if err := rows.Err(); err != nil {
		var myErr *mysqldriver.MySQLError
		if errors.As(err, &myErr) && myErr.Number == errCodeNoSuchGrant {
			// The user doesn't (yet) exist and therefore no grants either
			return nil, &managed.ExternalObservation{ResourceExists: false}, nil
		}
		return nil, nil, errors.Wrap(err, errCurrentGrant)
	}
	if privileges == nil {
		return nil, &managed.ExternalObservation{ResourceExists: false}, nil
	}
	return privileges, nil, nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.Grant)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotGrant)
	}

	username := *cr.Spec.ForProvider.User
	dbname := *cr.Spec.ForProvider.Database
	table := defaultTable(cr.Spec.ForProvider.Table)

	privileges := strings.Join(cr.Spec.ForProvider.Privileges.ToStringSlice(), ", ")

	query := createGrantQuery(privileges, dbname, username, table)
	if err := c.db.Exec(ctx, xsql.Query{String: query}); err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, errCreateGrant)
	}
	err := c.db.Exec(ctx, xsql.Query{String: "FLUSH PRIVILEGES"})
	return managed.ExternalCreation{}, errors.Wrap(err, errFlushPriv)
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.Grant)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotGrant)
	}

	username := *cr.Spec.ForProvider.User
	dbname := *cr.Spec.ForProvider.Database
	table := defaultTable(cr.Spec.ForProvider.Table)

	privileges := strings.Join(cr.Spec.ForProvider.Privileges.ToStringSlice(), ", ")
	username, host := mysql.SplitUserHost(username)

	// Remove current grants since it's not possible to update grants.
	// This might leave applications with no access to the DB for a short time
	// until the privileges are granted again.
	// Using a transaction is unfortunately not possible because a GRANT triggers
	// an implicit commit: https://dev.mysql.com/doc/refman/8.0/en/implicit-commit.html
	query := fmt.Sprintf("REVOKE ALL ON %s.%s FROM %s@%s",
		mysql.QuoteIdentifier(dbname),
		table,
		mysql.QuoteValue(username),
		mysql.QuoteValue(host),
	)
	if err := c.db.Exec(ctx, xsql.Query{String: query}); err != nil {
		return managed.ExternalUpdate{}, errors.Wrap(err, errRevokeGrant)
	}

	query = createGrantQuery(privileges, dbname, username, table)
	if err := c.db.Exec(ctx, xsql.Query{String: query}); err != nil {
		return managed.ExternalUpdate{}, err
	}
	err := c.db.Exec(ctx, xsql.Query{String: "FLUSH PRIVILEGES"})
	return managed.ExternalUpdate{}, errors.Wrap(err, errFlushPriv)
}

func createGrantQuery(privileges, dbname, username string, table string) string {
	username, host := mysql.SplitUserHost(username)
	result := fmt.Sprintf("GRANT %s ON %s.%s TO %s@%s",
		privileges,
		mysql.QuoteIdentifier(dbname),
		table,
		mysql.QuoteValue(username),
		mysql.QuoteValue(host),
	)

	return result
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) error {
	cr, ok := mg.(*v1alpha1.Grant)
	if !ok {
		return errors.New(errNotGrant)
	}

	username := *cr.Spec.ForProvider.User
	dbname := *cr.Spec.ForProvider.Database
	table := defaultTable(cr.Spec.ForProvider.Table)

	privileges := strings.Join(cr.Spec.ForProvider.Privileges.ToStringSlice(), ", ")
	username, host := mysql.SplitUserHost(username)

	query := fmt.Sprintf("REVOKE %s ON %s.%s FROM %s@%s",
		privileges,
		mysql.QuoteIdentifier(dbname),
		table,
		mysql.QuoteValue(username),
		mysql.QuoteValue(host),
	)

	if err := c.db.Exec(ctx, xsql.Query{String: query}); err != nil {
		return errors.Wrap(err, errRevokeGrant)
	}
	err := c.db.Exec(ctx, xsql.Query{String: "FLUSH PRIVILEGES"})
	return errors.Wrap(err, errFlushPriv)
}
