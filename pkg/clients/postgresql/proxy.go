package postgresql

import (
	"context"
	"database/sql"
	"net"
	"net/url"
	"time"

	"github.com/lib/pq"

	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
)

// proxyDialer adapts xsql.TunnelContext to the pq.Dialer interface, which
// predates context and only exposes Dial and DialTimeout.
type proxyDialer struct {
	proxy *url.URL
}

func (d *proxyDialer) Dial(_, addr string) (net.Conn, error) {
	return xsql.TunnelContext(context.Background(), d.proxy, addr)
}

func (d *proxyDialer) DialTimeout(_, addr string, timeout time.Duration) (net.Conn, error) {
	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()
	return xsql.TunnelContext(ctx, d.proxy, addr)
}

// openDB opens a *sql.DB for the given DSN. When HTTP_PROXY or HTTPS_PROXY is
// set and applicable to the target endpoint, connections are tunnelled through
// the proxy via HTTP CONNECT.
func openDB(endpoint, port, dsn string) (*sql.DB, error) {
	proxyURL, err := xsql.ProxyForAddr(endpoint, port)
	if err != nil {
		return nil, err
	}
	if proxyURL == nil {
		return sql.Open("postgres", dsn)
	}
	connector, err := pq.NewConnector(dsn)
	if err != nil {
		return nil, err
	}
	// pq.Connector.Dialer is a setter method, not a field.
	connector.Dialer(&proxyDialer{proxy: proxyURL})
	return sql.OpenDB(connector), nil
}
