package hana

import (
	"context"
	"errors"
	"testing"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/google/go-cmp/cmp"

	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
)

var hanaDb = hanaDB{
	dsn:      "hdb://<USER>:<PASSWORD>@<HOST>:<PORT>?TLSServerName=<HOST>",
	endpoint: "<HOST>",
	port:     "<PORT>",
}

func TestNew(t *testing.T) {
	type args struct {
		creds map[string][]byte
	}

	type want struct {
		db xsql.DB
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"validCreds": {
			reason: "valid Credentials",
			args: args{
				creds: map[string][]byte{
					xpv1.ResourceCredentialsSecretEndpointKey: []byte("endpoint.hanacloud.ondemand.com"),
					xpv1.ResourceCredentialsSecretPortKey:     []byte("443"),
					xpv1.ResourceCredentialsSecretUserKey:     []byte("example-user"),
					xpv1.ResourceCredentialsSecretPasswordKey: []byte("Secur3Pass0rd"),
				},
			},
			want: want{
				db: hanaDB{
					dsn:      "hdb://example-user:Secur3Pass0rd@endpoint.hanacloud.ondemand.com:443?TLSServerName=endpoint.hanacloud.ondemand.com",
					endpoint: "endpoint.hanacloud.ondemand.com",
					port:     "443",
				},
			},
		},
		// TODO Add more test cases
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			db := New(tc.args.creds)

			if diff := cmp.Diff(tc.want.db, db, cmp.AllowUnexported(hanaDB{})); diff != "" {
				t.Errorf("\n%s\ne.New(...): -want hanaDB, +got error:\n%s\n", tc.reason, diff)
			}
		})
	}
}

func TestExec(t *testing.T) {
	type args struct {
		query xsql.Query
	}

	type want struct {
		error error
	}

	cases := map[string]struct {
		reason string
		args   args
		want   want
	}{
		"validQuery": {
			reason: "valid query",
			args: args{
				query: xsql.Query{
					String: "SELECT * FROM DUMMY",
				},
			},
			want: want{
				error: nil,
			},
		},
		"invalidQuery": {
			reason: "invalid query",
			args: args{
				query: xsql.Query{
					String: "INVALID SQL QUERY",
				},
			},
			want: want{
				error: errors.New("error"),
			},
		},
	}

	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			err := hanaDb.Exec(context.Background(), tc.args.query)

			if (err != nil && tc.want.error == nil) || (err == nil && tc.want.error != nil) {
				t.Errorf("\n%s\ne.New(...): -want error, +got error:\n%s\n", tc.reason, err)
			}

		})
	}
}
