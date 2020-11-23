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
	"strings"
	"time"

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	ctrl "sigs.k8s.io/controller-runtime"
	"sigs.k8s.io/controller-runtime/pkg/client"

	runtimev1alpha1 "github.com/crossplane/crossplane-runtime/apis/core/v1alpha1"
	"github.com/crossplane/crossplane-runtime/pkg/event"
	"github.com/crossplane/crossplane-runtime/pkg/logging"
	"github.com/crossplane/crossplane-runtime/pkg/meta"
	"github.com/crossplane/crossplane-runtime/pkg/password"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/resource"

	"github.com/crossplane-contrib/provider-sql/apis/mysql/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
)

const (
	errTrackPCUsage = "cannot track ProviderConfig usage"
	errGetPC        = "cannot get ProviderConfig"
	errNoSecretRef  = "ProviderConfig does not reference a credentials Secret"
	errGetSecret    = "cannot get credentials Secret"

	errNotUser                 = "managed resource is not a User custom resource"
	errSelectUser              = "cannot select user"
	errCreateUser              = "cannot create user"
	errDropUser                = "cannot drop user"
	errUpdateUser              = "cannot update user"
	errFlushPriv               = "cannot flush privileges"
	errGetPasswordSecretFailed = "cannot get password secret"
)

// Setup adds a controller that reconciles User managed resources.
func Setup(mgr ctrl.Manager, l logging.Logger) error {
	name := managed.ControllerName(v1alpha1.UserGroupKind)

	t := resource.NewProviderConfigUsageTracker(mgr.GetClient(), &v1alpha1.ProviderConfigUsage{})
	r := managed.NewReconciler(mgr,
		resource.ManagedKind(v1alpha1.UserGroupVersionKind),
		managed.WithExternalConnecter(&connector{kube: mgr.GetClient(), usage: t, newDB: xsql.NewMySQLDB}),
		managed.WithLogger(l.WithValues("controller", name)),
		managed.WithShortWait(10*time.Second),
		managed.WithRecorder(event.NewAPIRecorder(mgr.GetEventRecorderFor(name))))

	return ctrl.NewControllerManagedBy(mgr).
		Named(name).
		For(&v1alpha1.User{}).
		Complete(r)
}

type connector struct {
	kube  client.Client
	usage resource.Tracker
	newDB func(creds map[string][]byte) xsql.DB
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
		db:   c.newDB(s.Data),
		kube: c.kube,
	}, nil
}

type external struct {
	db   xsql.DB
	kube client.Client
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.User)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotUser)
	}

	var name string
	username, host := splitUserHost(meta.GetExternalName(cr))

	query := "SELECT User FROM mysql.user WHERE User = ? AND Host = ?"
	err := c.db.Scan(ctx, xsql.Query{
		String: query,
		Parameters: []interface{}{
			username,
			host,
		},
	}, &name)
	if xsql.IsNoRows(err) {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errSelectUser)
	}

	cr.SetConditions(runtimev1alpha1.Available())

	_, pwdChanged, err := c.getPassword(ctx, cr)
	if err != nil {
		return managed.ExternalObservation{}, err
	}

	return managed.ExternalObservation{
		ResourceExists:   true,
		ResourceUpToDate: !pwdChanged,
	}, nil
}

func (c *external) Create(ctx context.Context, mg resource.Managed) (managed.ExternalCreation, error) {
	cr, ok := mg.(*v1alpha1.User)
	if !ok {
		return managed.ExternalCreation{}, errors.New(errNotUser)
	}

	username := meta.GetExternalName(cr)
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
	if err := c.db.Exec(ctx, xsql.Query{
		String: "CREATE USER " + username + " IDENTIFIED BY " + quoteValue(pw),
	}); err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, errCreateUser)
	}
	if err := c.db.Exec(ctx, xsql.Query{
		String: "FLUSH PRIVILEGES",
	}); err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, errFlushPriv)
	}

	user, _ := splitUserHost(username)

	return managed.ExternalCreation{
		ConnectionDetails: c.db.GetConnectionDetails(user, pw),
	}, nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.User)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotUser)
	}

	username := meta.GetExternalName(cr)
	pw, changed, err := c.getPassword(ctx, cr)
	if err != nil {
		return managed.ExternalUpdate{}, err
	}

	if changed {
		if err := c.db.Exec(ctx, xsql.Query{
			String: "ALTER USER " + username + " IDENTIFIED BY " + quoteValue(pw),
		}); err != nil {
			return managed.ExternalUpdate{}, errors.Wrap(err, errUpdateUser)
		}
		if err := c.db.Exec(ctx, xsql.Query{
			String: "FLUSH PRIVILEGES",
		}); err != nil {
			return managed.ExternalUpdate{}, errors.Wrap(err, errFlushPriv)
		}

		return managed.ExternalUpdate{
			ConnectionDetails: c.db.GetConnectionDetails(username, pw),
		}, nil
	}
	return managed.ExternalUpdate{}, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) error {
	cr, ok := mg.(*v1alpha1.User)
	if !ok {
		return errors.New(errNotUser)
	}

	username := meta.GetExternalName(cr)
	if err := c.db.Exec(ctx, xsql.Query{
		String: "DROP USER IF EXISTS " + username,
	}); err != nil {
		return errors.Wrap(err, errDropUser)
	}
	if err := c.db.Exec(ctx, xsql.Query{
		String: "FLUSH PRIVILEGES",
	}); err != nil {
		return errors.Wrap(err, errFlushPriv)
	}
	return nil
}

func quoteValue(id string) string {
	return "'" + strings.ReplaceAll(id, "'", "''") + "'"
}

func splitUserHost(user string) (username, host string) {
	username = user
	host = "%"
	if strings.Contains(user, "@") {
		parts := strings.SplitN(user, "@", 2)
		username = parts[0]
		host = parts[1]
	}
	return username, host
}
