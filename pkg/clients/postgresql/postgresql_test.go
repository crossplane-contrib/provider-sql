package postgresql

import (
	"context"
	"testing"

	"github.com/DATA-DOG/go-sqlmock"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
	"github.com/google/go-cmp/cmp"
	"github.com/google/go-cmp/cmp/cmpopts"
	"github.com/pkg/errors"
)

func TestDSNURLEscaping(t *testing.T) {
	endpoint := "endpoint"
	port := "5432"
	db := "postgres"
	user := "username"
	rawPass := "password^"
	encPass := "password%5E"
	sslmode := "require"
	dsn := DSN(user, rawPass, endpoint, port, db, sslmode)
	if dsn != "postgres://"+user+":"+encPass+"@"+endpoint+":"+port+"/"+db+"?sslmode="+sslmode {
		t.Errorf("DSN string did not match expected output with userinfo URL encoded")
	}
}

func TestExecTx(t *testing.T) {
	errBoom := errors.New("boom")
	usage := xsql.Query{String: "GRANT USAGE ON SCHEMA s TO r"}
	selct := xsql.Query{String: "GRANT SELECT ON ALL TABLES IN SCHEMA s TO r"}
	ok := sqlmock.NewResult(0, 0)

	cases := map[string]struct {
		ql     []xsql.Query
		expect func(sqlmock.Sqlmock)
		want   error
	}{
		"Commits": {
			ql: []xsql.Query{usage, selct},
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectBegin()
				m.ExpectExec("GRANT USAGE").WillReturnResult(ok)
				m.ExpectExec("GRANT SELECT").WillReturnResult(ok)
				m.ExpectCommit()
			},
		},
		"ReturnsBeginError": {
			ql: []xsql.Query{usage},
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectBegin().WillReturnError(errBoom)
			},
			want: errBoom,
		},
		"RollsBackOnExecError": {
			ql: []xsql.Query{usage},
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectBegin()
				m.ExpectExec("GRANT USAGE").WillReturnError(errBoom)
				m.ExpectRollback()
			},
			want: errBoom,
		},
		// A later statement failing must not commit the earlier ones.
		"RollsBackOnLaterExecError": {
			ql: []xsql.Query{usage, selct},
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectBegin()
				m.ExpectExec("GRANT USAGE").WillReturnResult(ok)
				m.ExpectExec("GRANT SELECT").WillReturnError(errBoom)
				m.ExpectRollback()
			},
			want: errBoom,
		},
		// COMMIT can fail after every statement succeeded, e.g. a deferred
		// constraint trigger. The error must reach the caller.
		"ReturnsCommitError": {
			ql: []xsql.Query{usage},
			expect: func(m sqlmock.Sqlmock) {
				m.ExpectBegin()
				m.ExpectExec("GRANT USAGE").WillReturnResult(ok)
				m.ExpectCommit().WillReturnError(errBoom)
			},
			want: errBoom,
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			db, mock, err := sqlmock.New()
			if err != nil {
				t.Fatal(err)
			}
			defer db.Close()
			tc.expect(mock)

			got := execTx(context.Background(), db, tc.ql)
			if diff := cmp.Diff(tc.want, got, cmpopts.EquateErrors()); diff != "" {
				t.Errorf("execTx(): -want, +got:\n%s", diff)
			}
			if err := mock.ExpectationsWereMet(); err != nil {
				t.Error(err)
			}
		})
	}
}
