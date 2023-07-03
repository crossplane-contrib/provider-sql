package usergroup

import (
	"context"
	"fmt"
	"github.com/crossplane-contrib/provider-sql/apis/hana/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/hana"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/pkg/errors"
	"strings"
)

const (
	errSelectUsergroup = "cannot select usergroup"
	errCreateUsergroup = "cannot create usergroup"
	errDropUsergroup   = "cannot drop usergroup"
)

type Client struct {
	db xsql.DB
}

func New(creds map[string][]byte) Client {
	return Client{
		db: hana.New(creds),
	}
}

func (c Client) Observe(ctx context.Context, parameters *v1alpha1.UsergroupParameters) (managed.ExternalObservation, error) {

	usergroupName := strings.ToUpper(parameters.UsergroupName)

	query := "SELECT USERGROUP_NAME, IS_USER_ADMIN_ENABLED FROM SYS.USERGROUPS WHERE USERGROUP_NAME = ?"

	err := c.db.Scan(ctx, xsql.Query{String: query, Parameters: []interface{}{usergroupName}}, &parameters.UsergroupName, &parameters.DisableUserAdmin)
	if xsql.IsNoRows(err) {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errSelectUsergroup)
	}

	queryParams := "SELECT USERGROUP_NAME, PARAMETER_NAME, PARAMETER_VALUE FROM SYS.USERGROUP_PARAMETERS WHERE USERGROUP_NAME = ?"

	rows, err := c.db.Query(ctx, xsql.Query{String: queryParams, Parameters: []interface{}{usergroupName}})

	for rows.Next() {
		var name, parameter, value string
		rowErr := rows.Scan(&name, &parameter, &value)
		if rowErr == nil {
			parameters.Parameters[parameter] = value
		}
	}

	return managed.ExternalObservation{
		ResourceExists:    true,
		ResourceUpToDate:  true,
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c Client) Create(ctx context.Context, parameters *v1alpha1.UsergroupParameters) (managed.ExternalCreation, error) {

	query := fmt.Sprintf("CREATE USERGROUP %s", parameters.UsergroupName)

	if parameters.DisableUserAdmin {
		query += " DISABLE USER ADMIN"
	}

	if parameters.NoGrantToCreator {
		query += " NO GRANT TO CREATOR"
	}

	if len(parameters.Parameters) > 0 {
		query += " SET PARAMETER"
		for key, value := range parameters.Parameters {
			query += fmt.Sprintf(" '%s' = '%s',", key, value)
		}
		query = strings.TrimSuffix(query, ",")
	}

	if parameters.EnableParameterSet != "" {
		query += fmt.Sprintf(" ENABLE PARAMETER SET '%s'", parameters.EnableParameterSet)
	}

	err := c.db.Exec(ctx, xsql.Query{String: query})

	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, errCreateUsergroup)
	}

	return managed.ExternalCreation{
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c Client) Update(ctx context.Context, parameters *v1alpha1.UsergroupParameters) (managed.ExternalUpdate, error) {

	//TODO

	return managed.ExternalUpdate{
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c Client) Delete(ctx context.Context, parameters *v1alpha1.UsergroupParameters) error {

	query := fmt.Sprintf("DROP USERGROUP %s", parameters.UsergroupName)

	err := c.db.Exec(ctx, xsql.Query{String: query})

	if err != nil {
		return errors.Wrap(err, errDropUsergroup)
	}

	return nil
}
