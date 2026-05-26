package mysql

import (
	"context"
	"database/sql"
	"net"

	gomysql "github.com/go-sql-driver/mysql"

	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
)

// openDB opens a *sql.DB for the given DSN. When HTTP_PROXY or HTTPS_PROXY is
// set and applicable to the target endpoint, connections are tunnelled through
// the proxy via HTTP CONNECT.
func openDB(endpoint, port, dsn string) (*sql.DB, error) {
	proxyURL, err := xsql.ProxyForAddr(endpoint, port)
	if err != nil {
		return nil, err
	}
	if proxyURL == nil {
		return sql.Open("mysql", dsn)
	}
	cfg, err := gomysql.ParseDSN(dsn)
	if err != nil {
		return nil, err
	}
	cfg.DialFunc = func(ctx context.Context, _, addr string) (net.Conn, error) {
		return xsql.TunnelContext(ctx, proxyURL, addr)
	}
	connector, err := gomysql.NewConnector(cfg)
	if err != nil {
		return nil, err
	}
	return sql.OpenDB(connector), nil
}
