package user

import (
	"context"
	"fmt"
	"github.com/crossplane-contrib/provider-sql/apis/hana/v1alpha1"
	apisv1alpha1 "github.com/crossplane-contrib/provider-sql/apis/hana/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/hana"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/pkg/errors"
	"strings"
)

const (
	errSelectUser = "cannot select user"
	errCreateUser = "cannot create user"
	errDropUser   = "cannot drop user"
)

type Client struct {
	db xsql.DB
}

func New(creds map[string][]byte) Client {
	return Client{
		db: hana.New(creds),
	}
}

func (c Client) Observe(ctx context.Context, parameters *v1alpha1.UserParameters) (managed.ExternalObservation, error) {

	observed := &v1alpha1.UserParameters{
		Username:       "",
		RestrictedUser: false,
		Usergroup:      "",
		Authentication: apisv1alpha1.Authentication{},
	}

	userName := strings.ToUpper(parameters.Username)

	query := "SELECT USER_NAME, USERGROUP_NAME, IS_RESTRICTED FROM SYS.USERS WHERE USER_NAME = ?"
	err := c.db.Scan(ctx, xsql.Query{String: query, Parameters: []interface{}{userName}}, &observed.Username, &observed.Usergroup, &observed.RestrictedUser)
	if xsql.IsNoRows(err) {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errSelectUser)
	}

	return managed.ExternalObservation{
		ResourceExists:    true,
		ResourceUpToDate:  true,
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c Client) Create(ctx context.Context, parameters *v1alpha1.UserParameters) (managed.ExternalCreation, error) {

	query := fmt.Sprintf("CREATE %s USER %s", ternary(parameters.RestrictedUser, "RESTRICTED", ""), parameters.Username)

	if parameters.Authentication.Password.Password != "" {
		query += fmt.Sprintf(" PASSWORD \"%s\" %s", parameters.Authentication.Password.Password, ternary(parameters.Authentication.Password.ForceFirstPasswordChange, "", "NO FORCE_FIRST_PASSWORD_CHANGE"))
	}

	if parameters.Usergroup != "" {
		query += fmt.Sprintf(" SET USERGROUP %s", parameters.Usergroup)
	}

	err := c.db.Exec(ctx, xsql.Query{String: query})

	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, errCreateUser)
	}

	return managed.ExternalCreation{
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c Client) Update(ctx context.Context, parameters *v1alpha1.UserParameters) (managed.ExternalUpdate, error) {

	//TODO

	return managed.ExternalUpdate{
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c Client) Delete(ctx context.Context, parameters *v1alpha1.UserParameters) error {

	query := fmt.Sprintf("DROP USER %s", parameters.Username)

	err := c.db.Exec(ctx, xsql.Query{String: query})

	if err != nil {
		return errors.Wrap(err, errDropUser)
	}

	return nil
}

func ternary(condition bool, trueValue interface{}, falseValue interface{}) interface{} {
	if condition {
		return trueValue
	}
	return falseValue
}
