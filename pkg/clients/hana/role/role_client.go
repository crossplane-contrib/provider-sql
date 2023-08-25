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

// Observe checks the state of the role
func (c Client) Read(ctx context.Context, parameters *v1alpha1.RoleParameters) (*v1alpha1.RoleObservation, error) {

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
		return observed, err1
	}

	var err2 error
	observed.LdapGroups, err2 = observeLdapGroups(ctx, c.db, parameters.RoleName)
	if err2 != nil {
		return observed, err2
	}

	var err3 error
	observed.Privileges, err3 = observePrivileges(ctx, c.db, parameters.RoleName)
	if err3 != nil {
		return observed, err3
	}

	return observed, nil
}

func observeLdapGroups(ctx context.Context, db xsql.DB, roleName string) (ldapGroups []string, errr error) {
	queryLdapGroups := "SELECT ROLE_NAME, LDAP_GROUP_NAME FROM SYS.ROLE_LDAP_GROUPS WHERE ROLE_NAME = ?"
	ldapRows, err := db.Query(ctx, xsql.Query{String: queryLdapGroups, Parameters: []interface{}{roleName}})
	if err != nil {
		return nil, err
	}
	defer ldapRows.Close() //nolint:errcheck
	if xsql.IsNoRows(err) {
		return nil, nil
	}
	for ldapRows.Next() {
		var role, ldapGroup string
		rowErr := ldapRows.Scan(&role, &ldapGroup)
		if rowErr == nil {
			ldapGroups = append(ldapGroups, ldapGroup)
		}
	}
	if err := ldapRows.Err(); err != nil {
		return nil, err
	}
	return ldapGroups, nil
}

func observePrivileges(ctx context.Context, db xsql.DB, roleName string) (privileges []string, errr error) {
	queryPrivileges := "SELECT GRANTEE, GRANTEE_TYPE, PRIVILEGE FROM GRANTED_PRIVILEGES WHERE GRANTEE = ? AND GRANTEE_TYPE = 'ROLE'"
	privRows, err := db.Query(ctx, xsql.Query{String: queryPrivileges, Parameters: []interface{}{roleName}})
	if err != nil {
		return nil, err
	}
	defer privRows.Close() //nolint:errcheck
	if xsql.IsNoRows(err) {
		return nil, nil
	}
	for privRows.Next() {
		var grantee, granteeType, privilege string
		rowErr := privRows.Scan(&grantee, &granteeType, &privilege)
		if rowErr == nil {
			privileges = append(privileges, privilege)
		}
	}
	if err := privRows.Err(); err != nil {
		return nil, err
	}
	return privileges, nil
}

// Create creates a new role in the db
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
		return err1
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
			return err2
		}
	}

	return nil
}

// UpdateLdapGroups modifies the ldap groups of an existing role in the db
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

// UpdatePrivileges modifies the privileges of an existing role in the db
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

// Delete removes an existing role from the db
func (c Client) Delete(ctx context.Context, parameters *v1alpha1.RoleParameters) error {

	query := fmt.Sprintf("DROP ROLE %s", getRoleName(parameters.Schema, parameters.RoleName))

	err := c.db.Exec(ctx, xsql.Query{String: query})

	if err != nil {
		return err
	}

	return nil
}

func getRoleName(schemaName, roleName string) string {
	if schemaName != "" {
		return fmt.Sprintf("%s.%s", schemaName, roleName)
	}
	return roleName
}
