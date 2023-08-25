package hana

import (
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

func TestNewDsn(t *testing.T) {
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
