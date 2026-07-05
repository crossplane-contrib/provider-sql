package xsql

import (
	"bufio"
	"context"
	"fmt"
	"net"
	"net/http"
	"net/url"

	"golang.org/x/net/http/httpproxy"
)

// ProxyForAddr returns the HTTP CONNECT proxy to use for connections to
// endpoint:port, or nil if none applies. HTTPS_PROXY is checked first, then
// HTTP_PROXY; NO_PROXY is honored either way.
//
// http.ProxyFromEnvironment is not used because it reads the env once via
// sync.Once and ignores later changes, which breaks tests and any process
// whose environment is mutated at runtime.
func ProxyForAddr(endpoint, port string) (*url.URL, error) {
	addr := net.JoinHostPort(endpoint, port)
	proxyFn := httpproxy.FromEnvironment().ProxyFunc()
	for _, scheme := range []string{"https", "http"} {
		proxy, err := proxyFn(&url.URL{Scheme: scheme, Host: addr})
		if err != nil {
			return nil, err
		}
		if proxy != nil {
			return proxy, nil
		}
	}
	return nil, nil
}

// TunnelContext opens a TCP connection through the supplied HTTP CONNECT
// proxy to the target addr. The returned net.Conn replays any bytes the
// underlying bufio reader buffered past the CONNECT response (for example a
// MySQL server greeting that arrived in the same packet as the proxy's
// "200 Connection established") ahead of subsequent reads on the socket.
func TunnelContext(ctx context.Context, proxy *url.URL, addr string) (_ net.Conn, err error) {
	conn, err := (&net.Dialer{}).DialContext(ctx, "tcp", proxy.Host)
	if err != nil {
		return nil, err
	}
	defer func() {
		if err != nil {
			if cerr := conn.Close(); cerr != nil {
				err = fmt.Errorf("error closing connection (%w) after: %w", cerr, err)
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
	br := bufio.NewReader(conn)
	var resp *http.Response
	resp, err = http.ReadResponse(br, req)
	if err != nil {
		return nil, err
	}
	resp.Body.Close() //nolint:errcheck
	if resp.StatusCode != http.StatusOK {
		err = fmt.Errorf("proxy CONNECT to %s: %s", addr, resp.Status)
		return nil, err
	}
	return &bufferedConn{Conn: conn, r: br}, nil
}

// bufferedConn serves bytes that bufio.Reader pre-buffered while parsing the
// CONNECT response, then falls through to the underlying socket.
type bufferedConn struct {
	net.Conn
	r *bufio.Reader
}

func (b *bufferedConn) Read(p []byte) (int, error) {
	return b.r.Read(p)
}
