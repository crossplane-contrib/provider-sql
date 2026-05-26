package mssql

import (
	"context"
	"database/sql"
	"net"
	"net/url"

	mssqldb "github.com/microsoft/go-mssqldb"

	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
)

// proxyDialer satisfies mssqldb.HostDialer so the driver hands the unresolved
// hostname to the dialer instead of calling net.LookupIP locally (which would
// fail for any address only resolvable on the proxy's network).
type proxyDialer struct {
	proxy *url.URL
}

func (d *proxyDialer) DialContext(ctx context.Context, _, addr string) (net.Conn, error) {
	return xsql.TunnelContext(ctx, d.proxy, addr)
}

func (d *proxyDialer) HostName() string { return "" }

// openDB opens a *sql.DB for the given DSN. When HTTP_PROXY or HTTPS_PROXY is
// set and applicable to the target endpoint, connections are tunnelled through
// the proxy via HTTP CONNECT.
func openDB(endpoint, port, dsn string) (*sql.DB, error) {
	proxyURL, err := xsql.ProxyForAddr(endpoint, port)
	if err != nil {
		return nil, err
	}
	if proxyURL == nil {
		return sql.Open(driverName, dsn)
	}
	connector, err := mssqldb.NewConnector(dsn)
	if err != nil {
		return nil, err
	}
	connector.Dialer = &proxyDialer{proxy: proxyURL}
	return sql.OpenDB(connector), nil
}
