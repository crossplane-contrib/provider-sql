package xsql

import (
	"context"
	"fmt"
	"io"
	"net"
	"net/url"
	"strings"
	"testing"
)

const (
	envHTTPProxy  = "HTTP_PROXY"
	envHTTPSProxy = "HTTPS_PROXY"
	envNoProxy    = "NO_PROXY"
)

// proxyAccept reads the CONNECT request from a single client, writes the
// supplied response (which may include trailing post-CRLFCRLF bytes), and
// returns the request bytes via the supplied channel before closing.
func proxyAccept(t *testing.T, ln net.Listener, response string, gotReq chan<- string) {
	t.Helper()
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close() //nolint:errcheck
		buf := make([]byte, 4096)
		n, _ := conn.Read(buf)
		gotReq <- string(buf[:n])
		_, _ = fmt.Fprint(conn, response)
		// Hold the connection open briefly so the client can read any
		// post-response bytes from the same socket.
		_, _ = io.ReadAll(conn)
	}()
}

func TestProxyForAddr(t *testing.T) {
	cases := map[string]struct {
		env       map[string]string
		wantProxy string
	}{
		"no env": {
			env:       map[string]string{envHTTPProxy: "", envHTTPSProxy: "", envNoProxy: ""},
			wantProxy: "",
		},
		"HTTPS_PROXY honored": {
			env:       map[string]string{envHTTPProxy: "", envHTTPSProxy: "http://proxy.example.com:3128", envNoProxy: ""},
			wantProxy: "http://proxy.example.com:3128",
		},
		"HTTP_PROXY honored when HTTPS unset": {
			env:       map[string]string{envHTTPProxy: "http://proxy.example.com:8080", envHTTPSProxy: "", envNoProxy: ""},
			wantProxy: "http://proxy.example.com:8080",
		},
		"NO_PROXY wildcard bypasses": {
			env:       map[string]string{envHTTPProxy: "http://p:1", envHTTPSProxy: "http://p:2", envNoProxy: "*"},
			wantProxy: "",
		},
		"NO_PROXY host bypasses": {
			env:       map[string]string{envHTTPProxy: "http://p:1", envHTTPSProxy: "http://p:2", envNoProxy: "db.example.com"},
			wantProxy: "",
		},
	}
	for name, tc := range cases {
		t.Run(name, func(t *testing.T) {
			for k, v := range tc.env {
				t.Setenv(k, v)
			}
			got, err := ProxyForAddr("db.example.com", "5432")
			if err != nil {
				t.Fatalf("ProxyForAddr() unexpected error: %v", err)
			}
			if tc.wantProxy == "" {
				if got != nil {
					t.Fatalf("ProxyForAddr() = %q, want nil", got)
				}
				return
			}
			if got == nil || got.String() != tc.wantProxy {
				t.Fatalf("ProxyForAddr() = %v, want %s", got, tc.wantProxy)
			}
		})
	}
}

func TestTunnelContextSuccess(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close() //nolint:errcheck

	gotReq := make(chan string, 1)
	proxyAccept(t, ln, "HTTP/1.1 200 Connection established\r\n\r\n", gotReq)

	conn, err := TunnelContext(context.Background(), &url.URL{Host: ln.Addr().String()}, "db.example.com:5432")
	if err != nil {
		t.Fatalf("TunnelContext() unexpected error: %v", err)
	}
	defer conn.Close() //nolint:errcheck

	req := <-gotReq
	if !strings.HasPrefix(req, "CONNECT db.example.com:5432 HTTP/1.1\r\n") {
		t.Fatalf("proxy received unexpected request: %q", req)
	}
}

func TestTunnelContextRejected(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close() //nolint:errcheck

	gotReq := make(chan string, 1)
	proxyAccept(t, ln, "HTTP/1.1 403 Forbidden\r\n\r\n", gotReq)

	_, err = TunnelContext(context.Background(), &url.URL{Host: ln.Addr().String()}, "db.example.com:5432")
	if err == nil {
		t.Fatal("TunnelContext() expected error on non-200 response, got nil")
	}
	<-gotReq
}

// TestTunnelContextPreservesPostResponseBytes guards against bufio.Reader
// over-reading past CRLFCRLF and swallowing bytes that the database server (or
// proxy) wrote into the same buffer fill. Without the bufferedConn wrapper the
// banner would never reach the caller.
func TestTunnelContextPreservesPostResponseBytes(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close() //nolint:errcheck

	const banner = "MYSQL_SERVER_HANDSHAKE_BYTES"
	gotReq := make(chan string, 1)
	proxyAccept(t, ln, "HTTP/1.1 200 Connection established\r\n\r\n"+banner, gotReq)

	conn, err := TunnelContext(context.Background(), &url.URL{Host: ln.Addr().String()}, "db.example.com:3306")
	if err != nil {
		t.Fatalf("TunnelContext() unexpected error: %v", err)
	}
	defer conn.Close() //nolint:errcheck

	<-gotReq
	buf := make([]byte, len(banner))
	if _, err := io.ReadFull(conn, buf); err != nil {
		t.Fatalf("ReadFull() unexpected error: %v", err)
	}
	if string(buf) != banner {
		t.Fatalf("post-response bytes = %q, want %q", buf, banner)
	}
}
