// SPDX-FileCopyrightText: 2026 The Crossplane Authors <https://crossplane.io>
//
// SPDX-License-Identifier: Apache-2.0

package mysql

import (
	"database/sql"
	"os"
	"sort"
	"strconv"
	"testing"
	"time"

	"github.com/google/go-cmp/cmp"
)

// TestRoleMembershipIntegration runs against an explicitly supplied disposable
// server. It creates and removes its own users, roles, and database.
func TestRoleMembershipIntegration(t *testing.T) {
	dsn := os.Getenv("MYSQL_TEST_DSN")
	if dsn == "" {
		t.Skip("set MYSQL_TEST_DSN to run against a disposable server")
	}
	admin, err := sql.Open("mysql", dsn)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { _ = admin.Close() })
	ctx := t.Context()

	suffix := strconv.FormatInt(time.Now().UnixNano(), 36)
	user := "mysql_user_" + suffix
	role := "mysql_role_" + suffix
	other := "mysql_other_" + suffix
	database := "mysql_db_" + suffix
	grantee := roleAccount{name: user, host: "%"}
	roleSQL := QuoteIdentifier(role)
	otherSQL := QuoteIdentifier(other)
	exec := func(query string) {
		t.Helper()
		if _, err := admin.ExecContext(ctx, query); err != nil {
			t.Fatal(err)
		}
	}
	t.Cleanup(func() {
		// Cleanup also runs after a failed assertion; individual accounts may have
		// already been removed by the test.
		_, _ = admin.Exec("DROP USER IF EXISTS " + grantee.sql())
		_, _ = admin.Exec("DROP ROLE IF EXISTS " + roleSQL)
		_, _ = admin.Exec("DROP ROLE IF EXISTS " + otherSQL)
		_, _ = admin.Exec("DROP DATABASE IF EXISTS " + QuoteIdentifier(database))
	})
	exec("CREATE USER " + grantee.sql())
	exec("CREATE ROLE " + roleSQL)
	exec("CREATE ROLE " + otherSQL)
	exec("CREATE DATABASE " + QuoteIdentifier(database))
	exec("GRANT SELECT ON " + QuoteIdentifier(database) + ".* TO " + grantee.sql())
	exec("GRANT " + otherSQL + " TO " + grantee.sql() + " WITH ADMIN OPTION")

	db := mySQLDB{dsn: dsn}
	grants := func() []string {
		t.Helper()
		rows, err := admin.QueryContext(ctx, "SHOW GRANTS FOR "+grantee.sql())
		if err != nil {
			t.Fatal(err)
		}
		defer rows.Close()
		var got []string
		for rows.Next() {
			var grant string
			if err := rows.Scan(&grant); err != nil {
				t.Fatal(err)
			}
			got = append(got, grant)
		}
		if err := rows.Err(); err != nil {
			t.Fatal(err)
		}
		sort.Strings(got)
		return got
	}
	assertMembership := func(role string, want bool) {
		t.Helper()
		got, err := RoleMembershipExists(ctx, db, user, role)
		if err != nil {
			t.Fatal(err)
		}
		if got != want {
			t.Fatalf("membership in %q = %v, want %v", role, got, want)
		}
	}

	before := grants()
	assertMembership(role, false)
	for range 2 {
		if err := GrantRoleMembership(ctx, db, user, role); err != nil {
			t.Fatal(err)
		}
		assertMembership(role, true)
	}
	for range 2 {
		if err := RevokeRoleMembership(ctx, db, user, role); err != nil {
			t.Fatal(err)
		}
		assertMembership(role, false)
	}
	if diff := cmp.Diff(before, grants()); diff != "" {
		t.Fatalf("unrelated grants changed (-want, +got):\n%s", diff)
	}

	// Dropping either account removes the membership. Observation and deletion
	// should not require the now-missing account to be recreated.
	if err := GrantRoleMembership(ctx, db, user, role); err != nil {
		t.Fatal(err)
	}
	exec("DROP ROLE " + roleSQL)
	assertMembership(role, false)
	if err := RevokeRoleMembership(ctx, db, user, role); err != nil {
		t.Fatal(err)
	}
	exec("DROP USER " + grantee.sql())
	assertMembership(other, false)
	if err := RevokeRoleMembership(ctx, db, user, other); err != nil {
		t.Fatal(err)
	}
}
