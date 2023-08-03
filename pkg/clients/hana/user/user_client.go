package user

import (
	"context"
	"database/sql"
	"fmt"
	"github.com/pkg/errors"
	"strings"
	"time"

	"github.com/crossplane-contrib/provider-sql/apis/hana/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/hana"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
)

const (
	errSelectUser  = "cannot select user"
	errCreateUser  = "cannot create user"
	errDropUser    = "cannot drop user"
	errGetPassword = "cannot get user password"
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

// Observe checks the state of the user
func (c Client) Observe(ctx context.Context, parameters *v1alpha1.UserParameters) (*v1alpha1.UserObservation, error) {

	observed := &v1alpha1.UserObservation{
		Username:               "",
		RestrictedUser:         false,
		LastPasswordChangeTime: "",
		CreatedAt:              "",
		Privileges:             nil,
		Roles:                  nil,
		Parameters:             make(map[string]string),
		Usergroup:              "",
	}

	query := "SELECT USER_NAME, " +
		"USERGROUP_NAME, " +
		"CREATE_TIME, " +
		"LAST_PASSWORD_CHANGE_TIME, " +
		"IS_RESTRICTED " +
		"FROM SYS.USERS " +
		"WHERE USER_NAME = ?"
	err := c.db.Scan(ctx, xsql.Query{
		String:     query,
		Parameters: []interface{}{parameters.Username}},
		&observed.Username,
		&observed.Usergroup,
		&observed.CreatedAt,
		&observed.LastPasswordChangeTime,
		&observed.RestrictedUser,
	)
	observed.CreatedAt = formatTime(observed.CreatedAt)
	observed.LastPasswordChangeTime = formatTime(observed.LastPasswordChangeTime)
	if xsql.IsNoRows(err) {
		return observed, nil
	}
	if err != nil {
		return observed, errors.Wrap(err, errSelectUser)
	}

	queryParams := "SELECT USER_NAME, " +
		"PARAMETER, " +
		"VALUE " +
		"FROM SYS.USER_PARAMETERS " +
		"WHERE USER_NAME = ?"
	rows, err2 := c.db.Query(ctx, xsql.Query{String: queryParams, Parameters: []interface{}{parameters.Username}})
	if err2 != nil {
		return observed, errors.Wrap(err2, errSelectUser)
	}
	defer rows.Close() //nolint:errcheck
	if xsql.IsNoRows(err2) {
		return observed, nil
	}
	for rows.Next() {
		var username, key, value string
		rowErr := rows.Scan(&username, &key, &value)
		if rowErr == nil {
			observed.Parameters[key] = value
		}
	}
	if err := rows.Err(); err != nil {
		return observed, errors.Wrap(err, errSelectUser)
	}

	queryPrivileges := "SELECT GRANTEE, GRANTEE_TYPE, PRIVILEGE FROM GRANTED_PRIVILEGES WHERE GRANTEE = ? AND GRANTEE_TYPE = 'USER'"
	privRows, err3 := c.db.Query(ctx, xsql.Query{String: queryPrivileges, Parameters: []interface{}{parameters.Username}})
	if err3 != nil {
		return observed, errors.Wrap(err3, errSelectUser)
	}
	defer privRows.Close() //nolint:errcheck
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
	if err := privRows.Err(); err != nil {
		return observed, errors.Wrap(err, errSelectUser)
	}

	queryRoles := "SELECT GRANTEE, GRANTEE_TYPE, ROLE_SCHEMA_NAME, ROLE_NAME FROM GRANTED_ROLES WHERE GRANTEE = ? AND GRANTEE_TYPE = 'USER'"
	roleRows, err4 := c.db.Query(ctx, xsql.Query{String: queryRoles, Parameters: []interface{}{parameters.Username}})
	if err4 != nil {
		return observed, errors.Wrap(err4, errSelectUser)
	}
	defer roleRows.Close() //nolint:errcheck
	if xsql.IsNoRows(err4) {
		return observed, nil
	}
	for roleRows.Next() {
		var grantee, granteeType, roleName string
		var roleSchemaName sql.NullString
		rowErr := roleRows.Scan(&grantee, &granteeType, &roleSchemaName, &roleName)
		if rowErr == nil {
			if roleSchemaName.Valid {
				roleName = fmt.Sprintf("%s.%s", roleSchemaName.String, roleName)
			}
			observed.Roles = append(observed.Roles, roleName)
		}
	}
	if err := roleRows.Err(); err != nil {
		return observed, errors.Wrap(err, errSelectUser)
	}

	return observed, nil
}

func formatTime(inTime string) string {
	t, timeErr := time.Parse(time.RFC3339, inTime)
	if timeErr == nil {
		return t.Format("2006-01-02 15:04:05")
	}
	return inTime
}

// Create a new user
func (c Client) Create(ctx context.Context, parameters *v1alpha1.UserParameters, args ...any) error {

	query := fmt.Sprintf("CREATE %sUSER %s", ternary(parameters.RestrictedUser, "RESTRICTED ", ""), parameters.Username)

	password := parameters.Authentication.Password
	if password.PasswordSecretRef != nil {
		passwrd := args[0].(string)
		if passwrd == "" {
			return errors.New(errGetPassword)
		}
		query += fmt.Sprintf(" PASSWORD \"%s\" %s", passwrd, ternary(password.ForceFirstPasswordChange, "", "NO FORCE_FIRST_PASSWORD_CHANGE"))
	}

	validParams := []string{"CLIENT", "LOCALE", "TIME ZONE", "EMAIL ADDRESS", "STATEMENT MEMORY LIMIT", "STATEMENT THREAD LIMIT"}

	if len(parameters.Parameters) > 0 {
		query += " SET PARAMETER"
		for key, value := range parameters.Parameters {
			key = strings.ToUpper(key)
			if contains(validParams, key) {
				query += fmt.Sprintf(" %s = '%s',", key, value)
			}
		}
		query = strings.TrimSuffix(query, ",")
	}

	if parameters.Usergroup != "" {
		query += fmt.Sprintf(" SET USERGROUP %s", parameters.Usergroup)
	}

	err := c.db.Exec(ctx, xsql.Query{String: query})

	if err != nil {
		return errors.Wrap(err, errCreateUser)
	}

	if len(parameters.Privileges) > 0 {
		queryPrives := "GRANT"
		for _, privilege := range parameters.Privileges {
			queryPrives += fmt.Sprintf(" %s,", privilege)
		}
		queryPrives = strings.TrimSuffix(queryPrives, ",")
		queryPrives += fmt.Sprintf(" TO %s", parameters.Username)
		err2 := c.db.Exec(ctx, xsql.Query{String: queryPrives})
		if err2 != nil {
			return errors.Wrap(err2, errCreateUser)
		}
	}

	if len(parameters.Roles) > 0 {
		queryRoles := "GRANT"
		for _, role := range parameters.Roles {
			queryRoles += fmt.Sprintf(" %s,", role)
		}
		queryRoles = strings.TrimSuffix(queryRoles, ",")
		queryRoles += fmt.Sprintf(" TO %s", parameters.Username)
		err3 := c.db.Exec(ctx, xsql.Query{String: queryRoles})
		if err3 != nil {
			return errors.Wrap(err3, errCreateUser)
		}
	}

	return nil
}

// UpdatePassword returns an error about not being able to update the password
func (c Client) UpdatePassword(ctx context.Context, username string, password string, forceFirstPasswordChange bool) error {
	query := fmt.Sprintf("ALTER USER %s PASSWORD \"%s\" %s", username, password, ternary(forceFirstPasswordChange, "", "NO FORCE_FIRST_PASSWORD_CHANGE"))
	err := c.db.Exec(ctx, xsql.Query{String: query})

	if err != nil {
		return errors.Wrap(err, "cannot update user password")
	}
	return nil
}

// UpdateRolesOrPrivileges updates the roles or privileges of the user
func (c Client) UpdateRolesOrPrivileges(ctx context.Context, username string, rolesOrPrivilegesToGrant, rolesOrPrivilegesToRevoke []string) error {

	if len(rolesOrPrivilegesToGrant) > 0 {
		query := "GRANT"
		for _, roleOrPrivilege := range rolesOrPrivilegesToGrant {
			query += fmt.Sprintf(" %s,", roleOrPrivilege)
		}
		query = strings.TrimSuffix(query, ",")
		query += fmt.Sprintf(" TO %s", username)
		err := c.db.Exec(ctx, xsql.Query{String: query})
		if err != nil {
			return errors.Wrap(err, "failed to grant privileges/roles")
		}
	}

	if len(rolesOrPrivilegesToRevoke) > 0 {
		query := "REVOKE"
		for _, roleOrPrivilege := range rolesOrPrivilegesToRevoke {
			query += fmt.Sprintf(" %s,", roleOrPrivilege)
		}
		query = strings.TrimSuffix(query, ",")
		query += fmt.Sprintf(" FROM %s", username)
		err := c.db.Exec(ctx, xsql.Query{String: query})
		if err != nil {
			return errors.Wrap(err, "failed to revoke privileges/roles")
		}
	}

	return nil
}

// UpdateParameters updates the parameters of the user
func (c Client) UpdateParameters(ctx context.Context, username string, parametersToSet map[string]string, parametersToClear map[string]string) error {
	query := fmt.Sprintf("ALTER USER %s", username)

	validParams := []string{"CLIENT", "LOCALE", "TIME ZONE", "EMAIL ADDRESS", "STATEMENT MEMORY LIMIT", "STATEMENT THREAD LIMIT"}

	if len(parametersToSet) > 0 {
		query += " SET PARAMETER"
		for key, value := range parametersToSet {
			key = strings.ToUpper(key)
			if contains(validParams, key) {
				query += fmt.Sprintf(" %s = '%s',", key, value)
			}
		}
		query = strings.TrimSuffix(query, ",")
	}

	if len(parametersToClear) > 0 {
		query += " CLEAR PARAMETER"
		for key := range parametersToClear {
			key = strings.ToUpper(key)
			if contains(validParams, key) {
				query += fmt.Sprintf(" %s,", key)
			}
		}
		query = strings.TrimSuffix(query, ",")
	}

	err := c.db.Exec(ctx, xsql.Query{String: query})

	if err != nil {
		return errors.Wrap(err, "cannot update user parameters")
	}
	return nil
}

// UpdateUsergroup updates the usergroup of the user
func (c Client) UpdateUsergroup(ctx context.Context, username string, usergroup string) error {
	query := fmt.Sprintf("ALTER USER %s", username)

	if usergroup != "" {
		query += fmt.Sprintf(" SET USERGROUP %s", usergroup)
	} else {
		query += " UNSET USERGROUP"
	}

	err := c.db.Exec(ctx, xsql.Query{String: query})

	if err != nil {
		return errors.Wrap(err, "cannot update user usergroup")
	}
	return nil
}

// Delete deletes the user
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

func contains(arr []string, str string) bool {
	for _, a := range arr {
		if a == str {
			return true
		}
	}
	return false
}
