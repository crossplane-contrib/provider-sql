package usergroup

import (
	"context"
	"fmt"
	"strings"

	"github.com/pkg/errors"

	"github.com/crossplane-contrib/provider-sql/apis/hana/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/hana"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
)

const (
	errSelectUsergroup = "cannot select usergroup"
	errCreateUsergroup = "cannot create usergroup"
	errUpdateUsergroup = "cannot update usergroup"
	errDropUsergroup   = "cannot drop usergroup"
)

// Client struct holds the connection to the db
type Client struct {
	db xsql.DB
}

// New creates a new db client
func New(creds map[string][]byte) Client {
	return Client{
		db: hana.New(creds),
	}
}

// Observe checks the state of the usergroup
func (c Client) Observe(ctx context.Context, parameters *v1alpha1.UsergroupParameters) (*v1alpha1.UsergroupObservation, error) {

	observed := &v1alpha1.UsergroupObservation{
		UsergroupName:    "",
		DisableUserAdmin: false,
		Parameters:       make(map[string]string),
	}

	var disableUserAdminString string

	query := "SELECT USERGROUP_NAME, IS_USER_ADMIN_ENABLED FROM SYS.USERGROUPS WHERE USERGROUP_NAME = ?"

	err := c.db.Scan(ctx, xsql.Query{String: query, Parameters: []interface{}{parameters.UsergroupName}}, &observed.UsergroupName, &disableUserAdminString)
	if xsql.IsNoRows(err) {
		return observed, nil
	}
	if err != nil {
		return observed, errors.Wrap(err, errSelectUsergroup)
	}

	if disableUserAdminString == "FALSE" {
		observed.DisableUserAdmin = true
	}

	queryParams := "SELECT USERGROUP_NAME, PARAMETER_NAME, PARAMETER_VALUE FROM SYS.USERGROUP_PARAMETERS WHERE USERGROUP_NAME = ?"

	rows, err := c.db.Query(ctx, xsql.Query{String: queryParams, Parameters: []interface{}{parameters.UsergroupName}})

	if err != nil {
		return observed, errors.Wrap(err, errSelectUsergroup)
	}
	defer rows.Close() //nolint:errcheck

	for rows.Next() {
		var name, parameter, value string
		rowErr := rows.Scan(&name, &parameter, &value)
		if rowErr == nil {
			observed.Parameters[parameter] = value
		}
	}

	if rows.Err() != nil {
		return observed, errors.Wrap(err, errSelectUsergroup)
	}

	return observed, nil
}

// Create creates a usergroup
func (c Client) Create(ctx context.Context, parameters *v1alpha1.UsergroupParameters, args ...any) error {

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
		return errors.Wrap(err, errCreateUsergroup)
	}

	return nil
}

// Update updates a usergroup
func (c Client) Update(ctx context.Context, parameters *v1alpha1.UsergroupParameters, args ...any) error {

	disableUserAdmin, ok1 := args[0].(bool)
	changedParameters, ok2 := args[1].(map[string]string)

	if !ok1 || !ok2 {
		return errors.New("incorrect argument types for Update")
	}

	if disableUserAdmin != parameters.DisableUserAdmin {
		query := fmt.Sprintf("ALTER USERGROUP %s", parameters.UsergroupName)
		if parameters.DisableUserAdmin {
			query += " DISABLE USER ADMIN"
		} else {
			query += " ENABLE USER ADMIN"
		}
		err := c.db.Exec(ctx, xsql.Query{String: query})
		if err != nil {
			return errors.Wrap(err, errUpdateUsergroup)
		}
	}

	if changedParameters != nil {
		query := fmt.Sprintf("ALTER USERGROUP %s", parameters.UsergroupName)
		query += " SET PARAMETER"
		for key, value := range changedParameters {
			query += fmt.Sprintf(" '%s' = '%s',", key, value)
		}
		query = strings.TrimSuffix(query, ",")
		err := c.db.Exec(ctx, xsql.Query{String: query})
		if err != nil {
			return errors.Wrap(err, errUpdateUsergroup)
		}
	}

	return nil
}

// Delete deletes the usergroup
func (c Client) Delete(ctx context.Context, parameters *v1alpha1.UsergroupParameters) error {

	query := fmt.Sprintf("DROP USERGROUP %s", parameters.UsergroupName)

	err := c.db.Exec(ctx, xsql.Query{String: query})

	if err != nil {
		return errors.Wrap(err, errDropUsergroup)
	}

	return nil
}
