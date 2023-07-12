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
		Validity: v1alpha1.Validity{
			From:  "",
			Until: "",
		},
		Parameters:             make(map[string]string),
		Usergroup:              "",
		LdapGroupAuthorization: "",
	}

	userName := strings.ToUpper(parameters.Username)

	query := "SELECT USER_NAME, " +
		"USERGROUP_NAME, " +
		"VALID_FROM, VALID_UNTIL, " +
		"LAST_PASSWORD_CHANGE_TIME, " +
		"IS_RESTRICTED, " +
		"AUTHORIZATION_MODE " +
		"FROM SYS.USERS " +
		"WHERE USER_NAME = ?"

	err := c.db.Scan(ctx, xsql.Query{
		String:     query,
		Parameters: []interface{}{userName}},
		&observed.Username,
		&observed.Usergroup,
		&observed.Validity.From,
		&observed.Validity.Until,
		&observed.LastPasswordChangeTime,
		&observed.RestrictedUser,
		&observed.LdapGroupAuthorization,
	)

	observed.Validity.From = formatTime(observed.Validity.From)
	observed.Validity.Until = formatTime(observed.Validity.Until)
	observed.LastPasswordChangeTime = formatTime(observed.LastPasswordChangeTime)

	if xsql.IsNoRows(err) {
		return observed, nil
	}
	if err != nil {
		return observed, errors.Wrap(err, errSelectUser)
	}

	queryParams := "SELECT USER_NAME, PARAMETER, VALUE FROM SYS.USER_PARAMETERS WHERE USER_NAME = ?"
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

	validity := parameters.Validity

	if validity.Until != "" {
		query += " VALID"
		if validity.From == "" {
			query += fmt.Sprintf(" UNTIL '%s'", validity.Until)
		} else {
			query += fmt.Sprintf(" FROM '%s' UNTIL '%s'", validity.From, validity.Until)
		}
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

	if parameters.LdapGroupAuthorization != "" {
		query += fmt.Sprintf(" AUTHORIZATION %s", parameters.LdapGroupAuthorization)
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

func (c Client) Update(ctx context.Context, parameters *v1alpha1.UserParameters, args ...any) error {

	// TODO

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
