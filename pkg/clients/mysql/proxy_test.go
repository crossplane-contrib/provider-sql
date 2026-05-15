package mysql

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"testing"
)

func TestTunnelSuccess(t *testing.T) {
	proxy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close() //nolint:errcheck

	go func() {
		conn, err := proxy.Accept()
		if err != nil {
			return
		}
		defer conn.Close() //nolint:errcheck
		buf := make([]byte, 4096)
		_, _ = conn.Read(buf)
		_, _ = fmt.Fprint(conn, "HTTP/1.1 200 Connection established\r\n\r\n")
	}()

	proxyURL := &url.URL{Host: proxy.Addr().String()}
	conn, err := tunnel(context.Background(), proxyURL, "db.example.com:3306")
	if err != nil {
		t.Fatalf("tunnel() unexpected error: %v", err)
	}
	conn.Close() //nolint:errcheck
}

func TestTunnelProxyRejected(t *testing.T) {
	proxy, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer proxy.Close() //nolint:errcheck

	go func() {
		conn, err := proxy.Accept()
		if err != nil {
			return
		}
		defer conn.Close() //nolint:errcheck
		buf := make([]byte, 4096)
		_, _ = conn.Read(buf)
		_, _ = fmt.Fprint(conn, "HTTP/1.1 407 Proxy Authentication Required\r\n\r\n")
	}()

	proxyURL := &url.URL{Host: proxy.Addr().String()}
	_, err = tunnel(context.Background(), proxyURL, "db.example.com:3306")
	if err == nil {
		t.Fatal("tunnel() expected error on non-200 response, got nil")
	}
}

func TestOpenDBNoProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("NO_PROXY", "*")

	db, err := openDB("localhost", "3306", "u:p@tcp(localhost:3306)/db")
	if err != nil {
		t.Fatalf("openDB() unexpected error: %v", err)
	}
	db.Close() //nolint:errcheck
}
