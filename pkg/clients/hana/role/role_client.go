package role

import (
	"context"
	"database/sql"
	"fmt"
	"strings"

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

func (c Client) Observe(ctx context.Context, parameters *v1alpha1.RoleParameters) (*v1alpha1.RoleObservation, error) {

	observed := &v1alpha1.RoleObservation{
		RoleName:   "",
		Schema:     "",
		Privileges: nil,
		LdapGroups: nil,
	}

	var schema sql.NullString
	query := "SELECT ROLE_SCHEMA_NAME, ROLE_NAME FROM SYS.ROLES WHERE ROLE_NAME = ?"
	err1 := c.db.Scan(ctx, xsql.Query{String: query, Parameters: []interface{}{parameters.RoleName}}, &schema, &observed.RoleName)
	if xsql.IsNoRows(err1) {
		return observed, nil
	}
	observed.Schema = schema.String
	if err1 != nil {
		return observed, errors.Wrap(err1, errSelectRole)
	}

	queryLdapGroups := "SELECT ROLE_NAME, LDAP_GROUP_NAME FROM SYS.ROLE_LDAP_GROUPS WHERE ROLE_NAME = ?"
	ldapRows, err2 := c.db.Query(ctx, xsql.Query{String: queryLdapGroups, Parameters: []interface{}{parameters.RoleName}})
	if xsql.IsNoRows(err2) {
		return observed, nil
	}
	for ldapRows.Next() {
		var role, ldapGroup string
		rowErr := ldapRows.Scan(&role, &ldapGroup)
		if rowErr == nil {
			observed.LdapGroups = append(observed.LdapGroups, ldapGroup)
		}
	}
	if err2 != nil {
		return observed, errors.Wrap(err2, errSelectRole)
	}

	queryPrivileges := "SELECT GRANTEE, GRANTEE_TYPE, PRIVILEGE FROM GRANTED_PRIVILEGES WHERE GRANTEE = ? AND GRANTEE_TYPE = 'ROLE'"
	privRows, err3 := c.db.Query(ctx, xsql.Query{String: queryPrivileges, Parameters: []interface{}{parameters.RoleName}})
	if xsql.IsNoRows(err3) {
		return observed, nil
	}
	for privRows.Next() {
		var grantee, granteeType, privilege string
		rowErr := privRows.Scan(&grantee, &granteeType, &privilege)
		if rowErr == nil {
			observed.Privileges = append(observed.Privileges, privilege)
		}
	}
	if err3 != nil {
		return observed, errors.Wrap(err3, errSelectRole)
	}

	return observed, nil
}

func (c Client) Create(ctx context.Context, parameters *v1alpha1.RoleParameters, args ...any) error {

	query := fmt.Sprintf("CREATE ROLE %s", getRoleName(parameters.Schema, parameters.RoleName))

	if len(parameters.LdapGroups) > 0 {
		query += " LDAP GROUP"
		for _, ldapGroup := range parameters.LdapGroups {
			query += fmt.Sprintf(" '%s',", ldapGroup)
		}
		query = strings.TrimSuffix(query, ",")
	}

	if parameters.NoGrantToCreator {
		query += " NO GRANT TO CREATOR"
	}

	err1 := c.db.Exec(ctx, xsql.Query{String: query})

	if err1 != nil {
		return errors.Wrap(err1, errCreateRole)
	}

	if len(parameters.Privileges) > 0 {
		queryPrives := "GRANT"
		for _, privilege := range parameters.Privileges {
			queryPrives += fmt.Sprintf(" %s,", privilege)
		}
		queryPrives = strings.TrimSuffix(queryPrives, ",")
		queryPrives += fmt.Sprintf(" TO %s", getRoleName(parameters.Schema, parameters.RoleName))
		err2 := c.db.Exec(ctx, xsql.Query{String: queryPrives})
		if err2 != nil {
			return errors.Wrap(err2, errCreateRole)
		}
	}

	return nil
}

func (c Client) UpdateLdapGroups(ctx context.Context, parameters *v1alpha1.RoleParameters, groupsToAdd, groupsToRemove []string) error {

	if len(groupsToAdd) > 0 {
		query := fmt.Sprintf("ALTER ROLE %s ADD LDAP GROUP", getRoleName(parameters.Schema, parameters.RoleName))
		for _, ldapGroup := range groupsToAdd {
			query += fmt.Sprintf(" '%s',", ldapGroup)
		}
		query = strings.TrimSuffix(query, ",")
		err := c.db.Exec(ctx, xsql.Query{String: query})
		if err != nil {
			return errors.Wrap(err, "failed to add ldap groups")
		}
	}

	if len(groupsToRemove) > 0 {
		query := fmt.Sprintf("ALTER ROLE %s DROP LDAP GROUP", getRoleName(parameters.Schema, parameters.RoleName))
		for _, ldapGroup := range groupsToRemove {
			query += fmt.Sprintf(" '%s',", ldapGroup)
		}
		query = strings.TrimSuffix(query, ",")
		err := c.db.Exec(ctx, xsql.Query{String: query})
		if err != nil {
			return errors.Wrap(err, "failed to remove ldap groups")
		}
	}

	return nil
}

func (c Client) UpdatePrivileges(ctx context.Context, parameters *v1alpha1.RoleParameters, privilegesToGrant, privilegesToRevoke []string) error {

	if len(privilegesToGrant) > 0 {
		query := "GRANT"
		for _, privilege := range privilegesToGrant {
			query += fmt.Sprintf(" %s,", privilege)
		}
		query = strings.TrimSuffix(query, ",")
		query += fmt.Sprintf(" TO %s", getRoleName(parameters.Schema, parameters.RoleName))
		err := c.db.Exec(ctx, xsql.Query{String: query})
		if err != nil {
			return errors.Wrap(err, "failed to grant privileges")
		}
	}

	if len(privilegesToRevoke) > 0 {
		query := "REVOKE"
		for _, privilege := range privilegesToRevoke {
			query += fmt.Sprintf(" %s,", privilege)
		}
		query = strings.TrimSuffix(query, ",")
		query += fmt.Sprintf(" FROM %s", getRoleName(parameters.Schema, parameters.RoleName))
		err := c.db.Exec(ctx, xsql.Query{String: query})
		if err != nil {
			return errors.Wrap(err, "failed to revoke privileges")
		}
	}

	return nil
}

func (c Client) Delete(ctx context.Context, parameters *v1alpha1.RoleParameters) error {

	query := fmt.Sprintf("DROP ROLE %s", getRoleName(parameters.Schema, parameters.RoleName))

	err := c.db.Exec(ctx, xsql.Query{String: query})

	if err != nil {
		return errors.Wrap(err, errDropRole)
	}

	return nil
}

func getRoleName(schemaName, roleName string) string {
	if schemaName != "" {
		return fmt.Sprintf("%s.%s", schemaName, roleName)
	} else {
		return roleName
	}
}
