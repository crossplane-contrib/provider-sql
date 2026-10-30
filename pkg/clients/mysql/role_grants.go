// SPDX-FileCopyrightText: 2026 The Crossplane Authors <https://crossplane.io>
//
// SPDX-License-Identifier: Apache-2.0

package mysql

import (
	"context"
	"fmt"

	mysqldriver "github.com/go-sql-driver/mysql"
	"github.com/pkg/errors"

	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
)

const (
	// MariaDB roles have no host, so only the implicit default role host matches.
	selectRolesMapping = "SELECT EXISTS(SELECT 1 FROM mysql.roles_mapping " +
		"WHERE `Role` = ? AND `User` = ? AND `Host` = ? AND ? = '%')"
	selectRoleEdges = "SELECT EXISTS(SELECT 1 FROM mysql.role_edges " +
		"WHERE FROM_USER = ? AND FROM_HOST = ? AND TO_USER = ? AND TO_HOST = ?)"
)

type roleAccount struct {
	name string
	host string
}

func (a roleAccount) sql() string {
	return QuoteIdentifier(a.name) + "@" + QuoteIdentifier(a.host)
}

func (a roleAccount) roleSQL() string {
	if a.host == "%" {
		return QuoteIdentifier(a.name)
	}
	return a.sql()
}

func membershipAccounts(user, memberOf string) (roleAccount, roleAccount, error) {
	if user == "" || memberOf == "" {
		return roleAccount{}, roleAccount{}, errors.New("role membership requires a user and a role")
	}
	username, userhost := SplitUserHost(user)
	rolename, rolehost := SplitUserHost(memberOf)
	return roleAccount{name: username, host: userhost}, roleAccount{name: rolename, host: rolehost}, nil
}

// RoleMembershipExists checks one direct membership in mysql.roles_mapping or
// mysql.role_edges. Missing users or roles have no matching membership.
// Administrative options are not managed here.
func RoleMembershipExists(ctx context.Context, db xsql.DB, user, memberOf string) (bool, error) {
	grantee, role, err := membershipAccounts(user, memberOf)
	if err != nil {
		return false, err
	}
	exists := false
	mappingErr := db.Scan(ctx, xsql.Query{
		String:     selectRolesMapping,
		Parameters: []interface{}{role.name, grantee.name, grantee.host, role.host},
	}, &exists)
	if mappingErr == nil {
		return exists, nil
	}

	// A missing catalog can also return SELECT denied, so try the other catalog
	// on any query error.
	if err := db.Scan(ctx, xsql.Query{
		String:     selectRoleEdges,
		Parameters: []interface{}{role.name, role.host, grantee.name, grantee.host},
	}, &exists); err != nil {
		return false, fmt.Errorf("cannot read role membership: mysql.roles_mapping: %w; mysql.role_edges: %w", mappingErr, err)
	}
	return exists, nil
}

// GrantRoleMembership grants only the requested membership. It neither creates
// the role nor changes its activation or administrative options.
func GrantRoleMembership(ctx context.Context, db xsql.DB, user, memberOf string) error {
	grantee, role, err := membershipAccounts(user, memberOf)
	if err != nil {
		return err
	}
	return db.Exec(ctx, xsql.Query{String: "GRANT " + role.roleSQL() + " TO " + grantee.sql()})
}

// RevokeRoleMembership removes only the requested membership, not the user or
// role. Dropping either account also removes its memberships.
func RevokeRoleMembership(ctx context.Context, db xsql.DB, user, memberOf string) error {
	grantee, role, err := membershipAccounts(user, memberOf)
	if err != nil {
		return err
	}
	err = db.Exec(ctx, xsql.Query{String: "REVOKE " + role.roleSQL() + " FROM " + grantee.sql()})
	var myErr *mysqldriver.MySQLError
	if errors.As(err, &myErr) {
		switch myErr.Number {
		case 1141, 3523: // MySQL: no such grant or unknown user/role.
			return nil
		case 1959, 1962: // MariaDB: invalid role or no such membership/recipient.
			return nil
		}
	}
	return err
}
