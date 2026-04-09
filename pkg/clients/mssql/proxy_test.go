package mssql

import (
	"context"
	"fmt"
	"net"
	"net/url"
	"testing"
)

func TestHttpProxyDialerSuccess(t *testing.T) {
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

	d := &httpProxyDialer{proxy: &url.URL{Host: proxy.Addr().String()}}
	conn, err := d.DialContext(context.Background(), "tcp", "db.example.com:1433")
	if err != nil {
		t.Fatalf("DialContext() unexpected error: %v", err)
	}
	conn.Close() //nolint:errcheck
}

func TestHttpProxyDialerRejected(t *testing.T) {
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
		_, _ = fmt.Fprint(conn, "HTTP/1.1 403 Forbidden\r\n\r\n")
	}()

	d := &httpProxyDialer{proxy: &url.URL{Host: proxy.Addr().String()}}
	_, err = d.DialContext(context.Background(), "tcp", "db.example.com:1433")
	if err == nil {
		t.Fatal("DialContext() expected error on non-200 response, got nil")
	}
}

func TestOpenDBNoProxy(t *testing.T) {
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("HTTPS_PROXY", "")
	t.Setenv("NO_PROXY", "*")

	db, err := openDB("localhost", "1433", "sqlserver://u:p@localhost:1433?database=db")
	if err != nil {
		t.Fatalf("openDB() unexpected error: %v", err)
	}
	db.Close() //nolint:errcheck
}
