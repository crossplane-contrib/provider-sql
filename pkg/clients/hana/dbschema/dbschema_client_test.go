package dbschema

import (
	"context"
	"database/sql"
	"github.com/crossplane-contrib/provider-sql/apis/hana/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/crossplane/crossplane-runtime/pkg/test"
	"github.com/google/go-cmp/cmp"
	"github.com/pkg/errors"
	"testing"
)

type mockDB struct {
	MockExec                 func(ctx context.Context, q xsql.Query) error
	MockExecTx               func(ctx context.Context, ql []xsql.Query) error
	MockScan                 func(ctx context.Context, q xsql.Query, dest ...interface{}) error
	MockQuery                func(ctx context.Context, q xsql.Query) (*sql.Rows, error)
	MockGetConnectionDetails func(username, password string) managed.ConnectionDetails
}

func (m mockDB) Exec(ctx context.Context, q xsql.Query) error {
	return m.MockExec(ctx, q)
}
func (m mockDB) ExecTx(ctx context.Context, ql []xsql.Query) error {
	return m.MockExecTx(ctx, ql)
}
func (m mockDB) Scan(ctx context.Context, q xsql.Query, dest ...interface{}) error {
	return m.MockScan(ctx, q, dest...)
}
func (m mockDB) Query(ctx context.Context, q xsql.Query) (*sql.Rows, error) {
	return m.MockQuery(ctx, q)
}
func (m mockDB) GetConnectionDetails(username, password string) managed.ConnectionDetails {
	return m.MockGetConnectionDetails(username, password)
}

func TestRead(t *testing.T) {
	errBoom := errors.New("boom")

	type fields struct {
		db mockDB
	}

	type args struct {
		ctx        context.Context
		parameters *v1alpha1.DbSchemaParameters
	}

	type want struct {
		observed *v1alpha1.DbSchemaObservation
		err      error
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   want
	}{
		"ErrRead": {
			reason: "Any errors encountered while reading the schema should be returned",
			fields: fields{
				db: mockDB{
					MockScan: func(ctx context.Context, q xsql.Query, dest ...interface{}) error {
						return errBoom
					},
				},
			},
			args: args{
				parameters: &v1alpha1.DbSchemaParameters{
					SchemaName: "DEMO_SCHEMA",
				},
			},
			want: want{
				observed: &v1alpha1.DbSchemaObservation{
					SchemaName: "",
					Owner:      "",
				},
				err: errBoom,
			},
		},
		"Success": {
			reason: "No error should be returned when we successfully read a schema",
			fields: fields{
				db: mockDB{
					MockScan: func(ctx context.Context, q xsql.Query, dest ...interface{}) error {
						return nil
					},
				},
			},
			args: args{
				parameters: &v1alpha1.DbSchemaParameters{
					SchemaName: "DEMO_SCHEMA",
				},
			},
			want: want{
				observed: &v1alpha1.DbSchemaObservation{
					SchemaName: "",
					Owner:      "",
				},
				err: nil,
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := Client{db: tc.fields.db}
			got, err := c.Read(tc.args.ctx, tc.args.parameters)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Read(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
			if diff := cmp.Diff(tc.want.observed, got); diff != "" {
				t.Errorf("\n%s\ne.Read(...): -want, +got:\n%s\n", tc.reason, diff)
			}
		})
	}
}

func TestCreate(t *testing.T) {
	errBoom := errors.New("boom")

	type fields struct {
		db mockDB
	}

	type args struct {
		ctx        context.Context
		parameters *v1alpha1.DbSchemaParameters
	}

	type want struct {
		err error
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   want
	}{
		"ErrCreate": {
			reason: "Any errors encountered while creating the schema should be returned",
			fields: fields{
				db: mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error {
						return errBoom
					},
				},
			},
			args: args{
				parameters: &v1alpha1.DbSchemaParameters{
					SchemaName: "DEMO_SCHEMA",
				},
			},
			want: want{
				err: errBoom,
			},
		},
		"Success": {
			reason: "No error should be returned when we successfully create a schema",
			fields: fields{
				db: mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error {
						return nil
					},
				},
			},
			args: args{
				parameters: &v1alpha1.DbSchemaParameters{
					SchemaName: "DEMO_SCHEMA",
				},
			},
			want: want{
				err: nil,
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := Client{db: tc.fields.db}
			err := c.Create(tc.args.ctx, tc.args.parameters)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Read(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
		})
	}
}

func TestDelete(t *testing.T) {
	errBoom := errors.New("boom")

	type fields struct {
		db mockDB
	}

	type args struct {
		ctx        context.Context
		parameters *v1alpha1.DbSchemaParameters
	}

	type want struct {
		err error
	}

	cases := map[string]struct {
		reason string
		fields fields
		args   args
		want   want
	}{
		"ErrDelete": {
			reason: "Any errors encountered while deleting the schema should be returned",
			fields: fields{
				db: mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error {
						return errBoom
					},
				},
			},
			args: args{
				parameters: &v1alpha1.DbSchemaParameters{
					SchemaName: "DEMO_SCHEMA",
				},
			},
			want: want{
				err: errBoom,
			},
		},
		"Success": {
			reason: "No error should be returned when we successfully delete a schema",
			fields: fields{
				db: mockDB{
					MockExec: func(ctx context.Context, q xsql.Query) error {
						return nil
					},
				},
			},
			args: args{
				parameters: &v1alpha1.DbSchemaParameters{
					SchemaName: "DEMO_SCHEMA",
				},
			},
			want: want{
				err: nil,
			},
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			c := Client{db: tc.fields.db}
			err := c.Delete(tc.args.ctx, tc.args.parameters)
			if diff := cmp.Diff(tc.want.err, err, test.EquateErrors()); diff != "" {
				t.Errorf("\n%s\ne.Read(...): -want error, +got error:\n%s\n", tc.reason, diff)
			}
		})
	}
}
