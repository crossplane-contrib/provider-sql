package user

import (
	"context"
	"fmt"
	"strings"

	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/pkg/errors"

	"github.com/crossplane-contrib/provider-sql/apis/hana/v1alpha1"
	apisv1alpha1 "github.com/crossplane-contrib/provider-sql/apis/hana/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/hana"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
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

	remoteIdentity := parameters.Authentication.RemoteIdentity
	password := parameters.Authentication.Password
	externalIdentity := parameters.Authentication.ExternalIdentity
	if remoteIdentity.RemoteUserName != "" && remoteIdentity.DatabaseName != "" {
		query += fmt.Sprintf(" '%s' AT DATABASE '%s'", remoteIdentity.RemoteUserName, remoteIdentity.DatabaseName)
	} else if password.Password != "" {
		query += fmt.Sprintf(" PASSWORD \"%s\" %s", password.Password, ternary(password.ForceFirstPasswordChange, "", "NO FORCE_FIRST_PASSWORD_CHANGE"))
	} else if externalIdentity != "" {
		query += fmt.Sprintf(" IDENTIFIED EXTERNALLY AS '%s'", externalIdentity)
	} else {
		x509Provider := parameters.Authentication.WithIdentity.X509Provider
		if x509Provider.IssuerDistinguishedName != "" && x509Provider.SubjectDistinguishedName != "" {
			query += fmt.Sprintf(" '%s' FOR X509 '%s'", x509Provider.IssuerDistinguishedName, x509Provider.SubjectDistinguishedName)
		}
		kerberosProvider := parameters.Authentication.WithIdentity.KerberosProvider
		if kerberosProvider != "" {
			query += fmt.Sprintf(" '%s' FOR KERBEROS", kerberosProvider)
		}
		logonTicket := parameters.Authentication.WithIdentity.LogonTicket
		if logonTicket {
			query += " FOR SAP LOGON TICKET"
		}
		assertionTicket := parameters.Authentication.WithIdentity.AssertionTicket
		if assertionTicket {
			query += " FOR SAP ASSERTION TICKET"
		}
		jwtProvider := parameters.Authentication.WithIdentity.JwtProvider
		if jwtProvider.JwtProviderName != "" && jwtProvider.MappedUserName != "" {
			query += fmt.Sprintf(" '%s' FOR JWT PROVIDER '%s'", jwtProvider.MappedUserName, jwtProvider.JwtProviderName)
		}
		ldapProvider := parameters.Authentication.WithIdentity.LdapProvider
		if ldapProvider {
			query += " FOR LDAP PROVIDER"
		}
	}

	validParams := []string{"CLIENT", "LOCALE", "TIME ZONE", "EMAIL ADDRESS", "STATEMENT MEMORY LIMIT", "STATEMENT THREAD LIMIT"}

	if len(parameters.Parameters) > 0 {
		query += " SET PARAMETER"
		for key, value := range parameters.Parameters {
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
		return managed.ExternalCreation{}, errors.Wrap(err, errCreateUser)
	}

	return managed.ExternalCreation{
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c Client) Update(ctx context.Context, parameters *v1alpha1.UserParameters) (managed.ExternalUpdate, error) {

	// TODO

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

func contains(arr []string, str string) bool {
	for _, a := range arr {
		if a == str {
			return true
		}
	}
	return false
}
