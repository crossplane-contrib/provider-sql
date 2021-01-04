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

package role

import (
	"context"
	"strings"
	"time"

	"github.com/lib/pq"
	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/event"
	"github.com/crossplane/crossplane-runtime/pkg/logging"
	"github.com/crossplane/crossplane-runtime/pkg/meta"
	"github.com/crossplane/crossplane-runtime/pkg/password"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/resource"

	"github.com/crossplane-contrib/provider-sql/apis/postgresql/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/postgresql"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
)

const (
	errTrackPCUsage = "cannot track ProviderConfig usage"
	errGetPC        = "cannot get ProviderConfig"
	errNoSecretRef  = "ProviderConfig does not reference a credentials Secret"
	errGetSecret    = "cannot get credentials Secret"

	errNotRole                 = "managed resource is not a Role custom resource"
	errSelectRole              = "cannot select role"
	errCreateRole              = "cannot create role"
	errDropRole                = "cannot drop role"
	errUpdateRole              = "cannot update role"
	errGetPasswordSecretFailed = "cannot get password secret"
)

// Setup adds a controller that reconciles Role managed resources.
func Setup(mgr ctrl.Manager, l logging.Logger) error {
	name := managed.ControllerName(v1alpha1.RoleGroupKind)

	t := resource.NewProviderConfigUsageTracker(mgr.GetClient(), &v1alpha1.ProviderConfigUsage{})
	r := managed.NewReconciler(mgr,
		resource.ManagedKind(v1alpha1.RoleGroupVersionKind),
		managed.WithExternalConnecter(&connector{kube: mgr.GetClient(), usage: t, newDB: postgresql.New}),
		managed.WithLogger(l.WithValues("controller", name)),
		managed.WithShortWait(10*time.Second),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))))

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		For(&v1alpha1.Role{}).
		Complete(r)
}

type connector struct {
	kube  client.Client
	usage resource.Tracker
	newDB func(creds map[string][]byte) xsql.DB
}

func (c *connector) Connect(ctx context.Context, mg resource.Managed) (managed.ExternalClient, error) {
	cr, ok := mg.(*v1alpha1.Role)
	if !ok {
		return nil, errors.New(errNotRole)
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
	// support one source (PostgreSQLConnectionSecret), which is required and
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
		db:   c.newDB(s.Data),
		kube: c.kube,
	}, nil
}

type external struct {
	db   xsql.DB
	kube client.Client
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.Role)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotRole)
	}

	var name string

	query := "SELECT " +
		"rolname " +
		// "rolsuper, " +
		// "rolinherit, " +
		// "rolcreaterole, " +
		// "rolcreatedb, " +
		// "rolcanlogin, " +
		// "rolreplication, " +
		// "rolbypassrls " +
		"FROM pg_roles WHERE rolname = $1"

	err := c.db.Scan(ctx, xsql.Query{
		String: query,
		Parameters: []interface{}{
			meta.GetExternalName(cr),
		},
	}, &name)
	if xsql.IsNoRows(err) {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errSelectRole)
	}

	cr.SetConditions(xpv1.Available())

	_, pwdChanged, err := c.getPassword(ctx, cr)
	if err != nil {
		return managed.ExternalObservation{}, err
	}

    // TODO (benagricola): Check privilege changes
	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: !pwdChanged,
	}, nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.Role)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotRole)
	}

	var b strings.Builder
	b.WriteString("CREATE ROLE ")
    b.WriteString(pq.QuoteIdentifier(meta.GetExternalName(cr)))

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

    b.WriteString(" PASSWORD ")
    b.WriteString(pq.QuoteLiteral(pw))

	privs := cr.Spec.ForProvider.Privileges
	for _, p := range privs {
		b.WriteString(" ")
        // SQL INJECTION: JUST USE TO TEST
		b.WriteString(string(p))
	}

	if err := c.db.Exec(ctx, xsql.Query{
		String: b.String(),
	}); err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, errCreateRole)
	}

	return managed.ExternalCreation{
		ConnectionDetails: c.db.GetConnectionDetails(meta.GetExternalName(cr), pw),
	}, nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.Role)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotRole)
	}

	pw, changed, err := c.getPassword(ctx, cr)
	if err != nil {
		return managed.ExternalUpdate{}, err
	}

	// TODO(benagricola): Detect privilege changes and password
	// changes separately and only run query when actually changed.
	// RIGHT NOW, Privileges will only be changed with a corresponding
	// password change.
	if changed {
		var b strings.Builder
		b.WriteString("ALTER ROLE ")
        b.WriteString(pq.QuoteIdentifier(meta.GetExternalName(cr)))


		b.WriteString(" PASSWORD ")
        b.WriteString(pq.QuoteLiteral(pw))

		privs := cr.Spec.ForProvider.Privileges
		for _, p := range privs {
			b.WriteString(" ")
			b.WriteString(pq.QuoteIdentifier(string(p)))
		}

		if err := c.db.Exec(ctx, xsql.Query{
			String:     b.String(),
		}); err != nil {
			return managed.ExternalUpdate{}, errors.Wrap(err, errUpdateRole)
		}

		return managed.ExternalUpdate{
			ConnectionDetails: c.db.GetConnectionDetails(meta.GetExternalName(cr), pw),
		}, nil
	}
	return managed.ExternalUpdate{}, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) error {
	cr, ok := mg.(*v1alpha1.Role)
	if !ok {
		return errors.New(errNotRole)
	}

	if err := c.db.Exec(ctx, xsql.Query{
		String: "DROP ROLE " + pq.QuoteIdentifier(meta.GetExternalName(cr)),
	}); err != nil {
		return errors.Wrap(err, errDropRole)
	}
	return nil
}
