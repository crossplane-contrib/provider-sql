package role

import (
	"context"
	"fmt"
	"strings"

	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/pkg/errors"

	"github.com/crossplane-contrib/provider-sql/apis/hana/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/hana"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
)

const (
	errSelectRole = "cannot select role"
	errCreateRole = "cannot create role"
	errDropRole   = "cannot drop role"
)

type Client struct {
	db xsql.DB
}

func New(creds map[string][]byte) Client {
	return Client{
		db: hana.New(creds),
	}
}

func (c Client) Observe(ctx context.Context, parameters *v1alpha1.RoleParameters) (managed.ExternalObservation, error) {

	observed := &v1alpha1.RoleObservation{
		RoleName:   "",
		LdapGroups: nil,
	}

	roleName := strings.ToUpper(parameters.RoleName)

	query := "SELECT ROLE_NAME FROM SYS.ROLES WHERE ROLE_NAME = ?"
	err := c.db.Scan(ctx, xsql.Query{String: query, Parameters: []interface{}{roleName}}, &observed.RoleName)

	if xsql.IsNoRows(err) {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errSelectRole)
	}

	queryLdapGroups := "SELECT ROLE_NAME, LDAP_GROUP_NAME FROM SYS.ROLE_LDAP_GROUPS WHERE ROLE_NAME = ?"

	rows, err := c.db.Query(ctx, xsql.Query{String: queryLdapGroups, Parameters: []interface{}{roleName}})

	for rows.Next() {
		var role, ldapGroup string
		rowErr := rows.Scan(&role, &ldapGroup)
		if rowErr == nil {
			observed.LdapGroups = append(observed.LdapGroups, ldapGroup)
		}
	}

	return managed.ExternalObservation{
		ResourceExists:    true,
		ResourceUpToDate:  true,
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c Client) Create(ctx context.Context, parameters *v1alpha1.RoleParameters) (managed.ExternalCreation, error) {

	query := fmt.Sprintf("CREATE ROLE %s", parameters.RoleName)

	if len(parameters.LdapGroups) > 0 {
		query += " LDAP GROUP"
		for ldapGroup := range parameters.LdapGroups {
			query += fmt.Sprintf(" '%s',", ldapGroup)
		}
		query = strings.TrimSuffix(query, ",")
	}

	if parameters.NoGrantToCreator {
		query += " NO GRANT TO CREATOR"
	}

	err := c.db.Exec(ctx, xsql.Query{String: query})

	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, errCreateRole)
	}

	return managed.ExternalCreation{
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c Client) Update(ctx context.Context, parameters *v1alpha1.RoleParameters) (managed.ExternalUpdate, error) {

	// TODO

	return managed.ExternalUpdate{
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c Client) Delete(ctx context.Context, parameters *v1alpha1.RoleParameters) error {

	query := fmt.Sprintf("DROP ROLE %s", parameters.RoleName)

	err := c.db.Exec(ctx, xsql.Query{String: query})

	if err != nil {
		return errors.Wrap(err, errDropRole)
	}

	return nil
}
