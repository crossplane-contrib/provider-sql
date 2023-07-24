package user

import (
	"context"
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

type Client struct {
	db xsql.DB
}

func New(creds map[string][]byte) Client {
	return Client{
		db: hana.New(creds),
	}
}

func (c Client) Observe(ctx context.Context, parameters *v1alpha1.UserParameters) (*v1alpha1.UserObservation, error) {

	observed := &v1alpha1.UserObservation{
		Username:               "",
		RestrictedUser:         false,
		LastPasswordChangeTime: "",
		CreatedAt:              "",
		Parameters:             make(map[string]string),
		Usergroup:              "",
	}

	userName := strings.ToUpper(parameters.Username)

	query := "SELECT USER_NAME, " +
		"USERGROUP_NAME, " +
		"CREATE_TIME, " +
		"LAST_PASSWORD_CHANGE_TIME, " +
		"IS_RESTRICTED " +
		"FROM SYS.USERS " +
		"WHERE USER_NAME = ?"

	err := c.db.Scan(ctx, xsql.Query{
		String:     query,
		Parameters: []interface{}{userName}},
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

	rows, err := c.db.Query(ctx, xsql.Query{String: queryParams, Parameters: []interface{}{parameters.Username}})

	for rows.Next() {
		var username, key, value string
		rowErr := rows.Scan(&username, &key, &value)
		if rowErr == nil {
			observed.Parameters[key] = value
		}
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

func (c Client) Create(ctx context.Context, parameters *v1alpha1.UserParameters, args ...any) error {

	query := fmt.Sprintf("CREATE %s USER %s", ternary(parameters.RestrictedUser, "RESTRICTED", ""), parameters.Username)

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

	return nil
}

func (c Client) UpdatePassword(ctx context.Context, username string, password string, forceFirstPasswordChange bool) error {
	query := fmt.Sprintf("ALTER USER %s PASSWORD \"%s\" %s", username, password, ternary(forceFirstPasswordChange, "", "NO FORCE_FIRST_PASSWORD_CHANGE"))
	err := c.db.Exec(ctx, xsql.Query{String: query})

	if err != nil {
		return errors.Wrap(err, "cannot update user password")
	}
	return nil
}

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
		for key, _ := range parametersToClear {
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

func (c Client) UpdateUsergroup(ctx context.Context, username string, usergroup string) error {
	query := fmt.Sprintf("ALTER USER %s", username)

	if usergroup != "" {
		query += fmt.Sprintf(" SET USERGROUP %s", usergroup)
	} else {
		query += fmt.Sprintf(" UNSET USERGROUP")
	}

	err := c.db.Exec(ctx, xsql.Query{String: query})

	if err != nil {
		return errors.Wrap(err, "cannot update user usergroup")
	}
	return nil
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

func contains(arr []string, str string) bool {
	for _, a := range arr {
		if a == str {
			return true
		}
	}
	return false
}
