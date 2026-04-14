package mysql

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"net/url"

	gomysql "github.com/go-sql-driver/mysql"
)

func tunnel(ctx context.Context, proxy *url.URL, addr string) (_ net.Conn, err error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", proxy.Host)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			closeErr := conn.Close()
			if closeErr != nil {
				err = fmt.Errorf("error closing connection (%w) after: %w", closeErr, err)
			}
		}
	}()
	req := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Host: addr},
		Host:   addr,
		Header: http.Header{},
	}
	if err = req.Write(conn); err != nil {
		return nil, err
	}
	var resp *http.Response
	resp, err = http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		return nil, err
	}
	resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("proxy CONNECT to %s: %s", addr, resp.Status)
		return nil, err
	}
	return conn, nil
}

// openDB opens a *sql.DB for the given DSN. When HTTP_PROXY or HTTPS_PROXY is
// set and applicable to the target endpoint, connections are tunnelled through
// the proxy via HTTP CONNECT.
func openDB(endpoint, port, dsn string) (*sql.DB, error) {
	req, _ := http.NewRequest(http.MethodConnect, "https://"+endpoint+":"+port, nil)
	proxyURL, err := http.ProxyFromEnvironment(req)
	if err != nil || proxyURL == nil {
		return sql.Open("mysql", dsn)
	}
	cfg, err := gomysql.ParseDSN(dsn)
	if err != nil {
		return nil, err
	}
	cfg.DialFunc = func(ctx context.Context, network, addr string) (net.Conn, error) {
		return tunnel(ctx, proxyURL, addr)
	}
	connector, err := gomysql.NewConnector(cfg)
	if err != nil {
		return nil, err
	}
	return sql.OpenDB(connector), nil
}
