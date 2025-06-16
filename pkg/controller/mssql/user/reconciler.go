/*
Copyright 2021 The Crossplane Authors.

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

	"github.com/pkg/errors"
	corev1 "k8s.io/api/core/v1"
	"k8s.io/apimachinery/pkg/types"
	"k8s.io/utils/ptr"
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

	"github.com/crossplane-contrib/provider-sql/apis/mssql/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/mssql"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
)

const (
	errTrackPCUsage = "cannot track ProviderConfig usage"
	errGetPC        = "cannot get ProviderConfig"
	errNoSecretRef  = "ProviderConfig does not reference a credentials Secret"
	errGetSecret    = "cannot get credentials Secret"

	errNotUser                = "managed resource is not a User custom resource"
	errSelectUser             = "cannot select user"
	errCreateUser             = "cannot create user %s"
	errCreateLogin            = "cannot create login %s"
	errDropUser               = "error dropping user %s"
	errDropLogin              = "error dropping login %s"
	errCannotGetLogins        = "cannot get current logins"
	errCannotKillLoginSession = "error killing session %d for login %s"

	errUpdateUser              = "cannot update user"
	errGetPasswordSecretFailed = "cannot get password secret"

	maxConcurrency = 5
)

// Setup adds a controller that reconciles User managed resources.
func Setup(mgr ctrl.Manager, o xpcontroller.Options) error {
	name := managed.ControllerName(v1alpha1.UserGroupKind)

	t := resource.NewProviderConfigUsageTracker(mgr.GetClient(), &v1alpha1.ProviderConfigUsage{})
	reconcilerOptions := []managed.ReconcilerOption{
		managed.WithExternalConnecter(&connector{kube: mgr.GetClient(), usage: t, newClient: mssql.New}),
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
	kube      client.Client
	usage     resource.Tracker
	newClient func(creds map[string][]byte, database string) xsql.DB
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

	userDB := c.newClient(s.Data, ptr.Deref(cr.Spec.ForProvider.Database, ""))
	loginDB := userDB
	if cr.Spec.ForProvider.LoginDatabase != nil {
		loginDB = c.newClient(s.Data, ptr.Deref(cr.Spec.ForProvider.LoginDatabase, ""))
	}

	return &external{
		userDB:  userDB,
		loginDB: loginDB,
		kube:    c.kube,
	}, nil
}

type external struct {
	userDB  xsql.DB
	loginDB xsql.DB
	kube    client.Client
}

func (c *external) Observe(ctx context.Context, mg resource.Managed) (managed.ExternalObservation, error) {
	cr, ok := mg.(*v1alpha1.User)
	if !ok {
		return managed.ExternalObservation{}, errors.New(errNotUser)
	}

	userType := v1alpha1.UserTypeLocal
	if cr.Spec.ForProvider.Type != nil {
		userType = *cr.Spec.ForProvider.Type
	}

	var query string

	switch userType {
	case v1alpha1.UserTypeAD:
		query = "SELECT name FROM sys.database_principals WHERE type IN ('E','X') AND name = @p1"
	case v1alpha1.UserTypeLocal:
		query = "SELECT name FROM sys.database_principals WHERE type = 'S' AND name = @p1"
	default:
		return managed.ExternalObservation{}, errors.Errorf("Type '%s' is not valid", *cr.Spec.ForProvider.Type)
	}

	var name string
	err := c.userDB.Scan(ctx, xsql.Query{
		String: query, Parameters: []interface{}{
			meta.GetExternalName(cr),
		},
	}, &name)
	if xsql.IsNoRows(err) {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errSelectUser)
	}

	cr.SetConditions(xpv1.Available())

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

	userType := v1alpha1.UserTypeLocal
	if cr.Spec.ForProvider.Type != nil {
		userType = *cr.Spec.ForProvider.Type
	}

	switch userType {
	case v1alpha1.UserTypeAD:
		return c.createADUser(ctx, cr)
	case v1alpha1.UserTypeLocal:
		return c.createLocalUser(ctx, cr)
	default:
		return managed.ExternalCreation{}, errors.Errorf("Type '%s' is not valid", *cr.Spec.ForProvider.Type)
	}
}

func (c *external) createADUser(ctx context.Context, cr *v1alpha1.User) (managed.ExternalCreation, error) {
	externalProviderUserQuery := fmt.Sprintf("CREATE USER %s FROM EXTERNAL PROVIDER", mssql.QuoteIdentifier(meta.GetExternalName(cr)))
	if err := c.userDB.Exec(ctx, xsql.Query{
		String: externalProviderUserQuery,
	}); err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, errCreateUser)
	}

	return managed.ExternalCreation{
		ConnectionDetails: c.userDB.GetConnectionDetails(meta.GetExternalName(cr), ""),
	}, nil
}

func (c *external) createLocalUser(ctx context.Context, cr *v1alpha1.User) (managed.ExternalCreation, error) {
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

	loginQuery := fmt.Sprintf("CREATE LOGIN %s WITH PASSWORD=%s", mssql.QuoteIdentifier(meta.GetExternalName(cr)), mssql.QuoteValue(pw))
	if err := c.loginDB.Exec(ctx, xsql.Query{
		String: loginQuery,
	}); err != nil {
		return managed.ExternalCreation{}, errors.Wrapf(err, errCreateLogin, meta.GetExternalName(cr))
	}

	userQuery := fmt.Sprintf("CREATE USER %s FOR LOGIN %s", mssql.QuoteIdentifier(meta.GetExternalName(cr)), mssql.QuoteIdentifier(meta.GetExternalName(cr)))
	if err := c.userDB.Exec(ctx, xsql.Query{
		String: userQuery,
	}); err != nil {
		return managed.ExternalCreation{}, errors.Wrapf(err, errCreateUser, meta.GetExternalName(cr))
	}

	return managed.ExternalCreation{
		ConnectionDetails: c.userDB.GetConnectionDetails(meta.GetExternalName(cr), pw),
	}, nil
}

func (c *external) Update(ctx context.Context, mg resource.Managed) (managed.ExternalUpdate, error) {
	cr, ok := mg.(*v1alpha1.User)
	if !ok {
		return managed.ExternalUpdate{}, errors.New(errNotUser)
	}

	if t := cr.Spec.ForProvider.Type; t == nil || *t == v1alpha1.UserTypeLocal {
		pw, changed, err := c.getPassword(ctx, cr)
		if err != nil {
			return managed.ExternalUpdate{}, err
		}

		if changed {
			query := fmt.Sprintf("ALTER LOGIN %s WITH PASSWORD=%s", mssql.QuoteIdentifier(meta.GetExternalName(cr)), mssql.QuoteValue(pw))
			if err := c.loginDB.Exec(ctx, xsql.Query{
				String: query,
			}); err != nil {
				return managed.ExternalUpdate{}, errors.Wrap(err, errUpdateUser)
			}

			return managed.ExternalUpdate{
				ConnectionDetails: c.userDB.GetConnectionDetails(meta.GetExternalName(cr), pw),
			}, nil
		}
	}
	return managed.ExternalUpdate{}, nil
}

func (c *external) Delete(ctx context.Context, mg resource.Managed) error {
	cr, ok := mg.(*v1alpha1.User)
	if !ok {
		return errors.New(errNotUser)
	}

	query := fmt.Sprintf("SELECT session_id FROM sys.dm_exec_sessions WHERE login_name = %s", mssql.QuoteValue(meta.GetExternalName(cr)))
	rows, err := c.userDB.Query(ctx, xsql.Query{String: query})
	if err != nil {
		return errors.Wrap(err, errCannotGetLogins)
	}
	defer rows.Close() //nolint:errcheck

	for rows.Next() {
		var sessionID int
		if err := rows.Scan(&sessionID); err != nil {
			return errors.Wrap(err, errCannotGetLogins)
		}
		if err := c.userDB.Exec(ctx, xsql.Query{String: fmt.Sprintf("KILL %d", sessionID)}); err != nil {
			return errors.Wrapf(err, errCannotKillLoginSession, sessionID, meta.GetExternalName(cr))
		}
	}
	if err := rows.Err(); err != nil {
		return errors.Wrap(err, errCannotGetLogins)
	}

	if err := c.userDB.Exec(ctx, xsql.Query{
		String: fmt.Sprintf("DROP USER IF EXISTS %s", mssql.QuoteIdentifier(meta.GetExternalName(cr))),
	}); err != nil {
		return errors.Wrapf(err, errDropUser, meta.GetExternalName(cr))
	}

	if err := c.loginDB.Exec(ctx, xsql.Query{
		String: fmt.Sprintf("DROP LOGIN %s", mssql.QuoteIdentifier(meta.GetExternalName(cr))),
	}); err != nil {
		return errors.Wrapf(err, errDropLogin, meta.GetExternalName(cr))
	}

	return nil
}
