package mssql

import (
	"bufio"
	"context"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"net/url"

	mssqldb "github.com/microsoft/go-mssqldb"
)

type httpProxyDialer struct {
	proxy *url.URL
}

func (d *httpProxyDialer) DialContext(ctx context.Context, network, addr string) (net.Conn, error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", d.proxy.Host)
	if err != nil {
		return nil, err
	}
	defer func()
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
	if err := req.Write(conn); err != nil {
		return nil, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		return nil, err
	}
	resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		conn.Close() //nolint:errcheck
		return nil, fmt.Errorf("proxy CONNECT to %s: %s", addr, resp.Status)
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
		return sql.Open(driverName, dsn)
	}
	connector, err := mssqldb.NewConnector(dsn)
	if err != nil {
		return nil, err
	}
	connector.Dialer = &httpProxyDialer{proxy: proxyURL}
	return sql.OpenDB(connector), nil
}
