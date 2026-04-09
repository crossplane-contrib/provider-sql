package postgresql

import (
	"bufio"
	"database/sql"
	"fmt"
	"net"
	"net/http"
	"net/url"
	"time"

	"github.com/lib/pq"
)

// httpProxyDialer tunnels connections through an HTTP CONNECT proxy.
type httpProxyDialer struct {
	proxy *url.URL
}

func (d *httpProxyDialer) Dial(network, addr string) (net.Conn, error) {
	return tunnel(&net.Dialer{}, d.proxy, addr)
}

func (d *httpProxyDialer) DialTimeout(network, addr string, timeout time.Duration) (net.Conn, error) {
	return tunnel(&net.Dialer{Timeout: timeout}, d.proxy, addr)
}

func tunnel(nd *net.Dialer, proxy *url.URL, addr string) (net.Conn, error) {
	conn, err := nd.Dial("tcp", proxy.Host)
	if err != nil {
		return nil, err
	}
	req := &http.Request{
		Method: http.MethodConnect,
		URL:    &url.URL{Host: addr},
		Host:   addr,
		Header: http.Header{},
	}
	if err := req.Write(conn); err != nil {
		conn.Close()
		return nil, err
	}
	resp, err := http.ReadResponse(bufio.NewReader(conn), req)
	if err != nil {
		conn.Close()
		return nil, err
	}
	resp.Body.Close()
	if resp.StatusCode != http.StatusOK {
		conn.Close()
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
		return sql.Open("postgres", dsn)
	}
	connector, err := pq.NewConnector(dsn)
	if err != nil {
		return nil, err
	}
	connector.Dialer(&httpProxyDialer{proxy: proxyURL})
	return sql.OpenDB(connector), nil
}
