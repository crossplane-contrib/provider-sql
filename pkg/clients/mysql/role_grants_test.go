// SPDX-FileCopyrightText: 2026 The Crossplane Authors <https://crossplane.io>
//
// SPDX-License-Identifier: Apache-2.0

package mysql

import (
	"context"
	"database/sql"
	"errors"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	mysqldriver "github.com/go-sql-driver/mysql"

	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
)

const (
	readerGrant  = "GRANT `reader` TO `app`@`%`"
	readerRevoke = "REVOKE `reader` FROM `app`@`%`"
)

// roleTestDB adapts sqlmock without implementing unrelated xsql operations.
type roleTestDB struct {
	xsql.DB
	db *sql.DB
}

func (d roleTestDB) Scan(ctx context.Context, q xsql.Query, dest ...interface{}) error {
	return d.db.QueryRowContext(ctx, q.String, q.Parameters...).Scan(dest...)
}

func (d roleTestDB) Exec(ctx context.Context, q xsql.Query) error {
	_, err := d.db.ExecContext(ctx, q.String, q.Parameters...)
	return err
}

func newRoleTestDB(t *testing.T) (roleTestDB, sqlmock.Sqlmock) {
	t.Helper()
	db, mock, err := sqlmock.New(sqlmock.QueryMatcherOption(sqlmock.QueryMatcherEqual))
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		if err := mock.ExpectationsWereMet(); err != nil {
			t.Error(err)
		}
		_ = db.Close()
	})
	return roleTestDB{db: db}, mock
}

func TestRoleMembershipExists(t *testing.T) {
	missingMapping := &mysqldriver.MySQLError{Number: 1146, Message: "mysql.roles_mapping does not exist"}
	denied := &mysqldriver.MySQLError{Number: 1142, Message: "SELECT denied"}
	mappingErr := errors.New("roles_mapping unavailable")
	edgesErr := errors.New("role_edges unavailable")

	cases := map[string]struct {
		user       string
		role       string
		exists     bool
		mappingErr error
		edgesErr   error
		wantErr    bool
	}{
		"MariaDBPresent":            {user: "app", role: "reader", exists: true},
		"MariaDBAbsent":             {user: "app", role: "reader"},
		"MariaDBExplicitUserHost":   {user: "app@10.%", role: "reader", exists: true},
		"MariaDBNonDefaultRoleHost": {user: "app", role: "reader@localhost"},
		"UppercaseRoleName":         {user: "app", role: "AWS_SELECT_S3_ACCESS", exists: true},
		"MySQLPresent":              {user: "app", role: "reader", exists: true, mappingErr: missingMapping},
		"MySQLAbsent":               {user: "app", role: "reader", mappingErr: missingMapping},
		"MySQLExplicitHosts":        {user: "app@10.%", role: "reader@localhost", exists: true, mappingErr: missingMapping},
		"PermissionDeniedFallback":  {user: "app", role: "reader", exists: true, mappingErr: denied},
		"OtherQueryErrorFallback":   {user: "app", role: "reader", exists: true, mappingErr: mappingErr},
		"BothQueriesUnavailable":    {user: "app", role: "reader", mappingErr: mappingErr, edgesErr: edgesErr, wantErr: true},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			db, mock := newRoleTestDB(t)
			username, userhost := SplitUserHost(tc.user)
			rolename, rolehost := SplitUserHost(tc.role)
			mapping := mock.ExpectQuery(selectRolesMapping).
				WithArgs(rolename, username, userhost, rolehost)
			if tc.mappingErr == nil {
				mapping.WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(tc.exists))
			} else {
				mapping.WillReturnError(tc.mappingErr)
				edges := mock.ExpectQuery(selectRoleEdges).
					WithArgs(rolename, rolehost, username, userhost)
				if tc.edgesErr == nil {
					edges.WillReturnRows(sqlmock.NewRows([]string{"exists"}).AddRow(tc.exists))
				} else {
					edges.WillReturnError(tc.edgesErr)
				}
			}

			exists, err := RoleMembershipExists(t.Context(), db, tc.user, tc.role)
			if tc.wantErr {
				for _, cause := range []error{tc.mappingErr, tc.edgesErr} {
					if !errors.Is(err, cause) {
						t.Errorf("RoleMembershipExists() error = %v, want cause %v", err, cause)
					}
				}
			} else if err != nil {
				t.Fatal(err)
			}
			if exists != tc.exists {
				t.Errorf("RoleMembershipExists() = %v, want %v", exists, tc.exists)
			}
		})
	}
}

func TestGrantRoleMembership(t *testing.T) {
	for name, tc := range map[string]struct {
		user string
		role string
		sql  string
	}{
		"DefaultHosts":        {user: "app", role: "reader", sql: readerGrant},
		"ExplicitDefaultHost": {user: "app", role: "reader@%", sql: readerGrant},
		"ExplicitHosts":       {user: "app@10.%", role: "reader@localhost", sql: "GRANT `reader`@`localhost` TO `app`@`10.%`"},
		"Escaping":            {user: "a`p'p\\", role: "r`e'a\\", sql: "GRANT `r``e'a\\` TO `a``p'p\\`@`%`"},
	} {
		t.Run(name, func(t *testing.T) {
			db, mock := newRoleTestDB(t)
			mock.ExpectExec(tc.sql).WillReturnResult(sqlmock.NewResult(0, 0))
			if err := GrantRoleMembership(t.Context(), db, tc.user, tc.role); err != nil {
				t.Fatal(err)
			}
		})
	}
	t.Run("ExecError", func(t *testing.T) {
		db, mock := newRoleTestDB(t)
		want := errors.New("grant denied")
		mock.ExpectExec(readerGrant).WillReturnError(want)
		if err := GrantRoleMembership(t.Context(), db, "app", "reader"); !errors.Is(err, want) {
			t.Fatalf("error = %v, want %v", err, want)
		}
	})
}

func TestRevokeRoleMembership(t *testing.T) {
	for name, tc := range map[string]struct {
		execErr error
		wantErr bool
	}{
		"SuccessOrAlreadyAbsent": {},
		"NoGrant":                {execErr: &mysqldriver.MySQLError{Number: 1141}},
		"UnknownUserOrRole":      {execErr: &mysqldriver.MySQLError{Number: 3523}},
		"MariaDBUnknownRole":     {execErr: &mysqldriver.MySQLError{Number: 1959}},
		"MariaDBNoMembership":    {execErr: &mysqldriver.MySQLError{Number: 1962}},
		"MariaDBPermissionError": {execErr: &mysqldriver.MySQLError{Number: 1698}, wantErr: true},
		"Unavailable":            {execErr: errors.New("unavailable"), wantErr: true},
	} {
		t.Run(name, func(t *testing.T) {
			db, mock := newRoleTestDB(t)
			exec := mock.ExpectExec(readerRevoke)
			if tc.execErr != nil {
				exec.WillReturnError(tc.execErr)
			} else {
				exec.WillReturnResult(sqlmock.NewResult(0, 0))
			}
			err := RevokeRoleMembership(t.Context(), db, "app", "reader")
			if tc.wantErr && !errors.Is(err, tc.execErr) {
				t.Fatalf("error = %v, want %v", err, tc.execErr)
			}
			if !tc.wantErr && err != nil {
				t.Fatal(err)
			}
		})
	}
}

func TestMissingMembershipUser(t *testing.T) {
	db, _ := newRoleTestDB(t)
	const want = "role membership requires a user and a role"
	if _, err := RoleMembershipExists(t.Context(), db, "", "reader"); err == nil || err.Error() != want {
		t.Errorf("RoleMembershipExists() error = %v, want %q", err, want)
	}
	if err := GrantRoleMembership(t.Context(), db, "", "reader"); err == nil || err.Error() != want {
		t.Errorf("GrantRoleMembership() error = %v, want %q", err, want)
	}
	if err := RevokeRoleMembership(t.Context(), db, "", "reader"); err == nil || err.Error() != want {
		t.Errorf("RevokeRoleMembership() error = %v, want %q", err, want)
	}
}
