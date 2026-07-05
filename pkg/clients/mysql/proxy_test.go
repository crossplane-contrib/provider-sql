package mysql

import (
	"context"
	"fmt"
	"net"
	"strings"
	"testing"
	"time"
)

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

// TestOpenDBProxyDialUsesTunnel verifies the DialFunc is wired through the
// HTTP CONNECT tunnel when HTTPS_PROXY is set.
func TestOpenDBProxyDialUsesTunnel(t *testing.T) {
	ln, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatal(err)
	}
	defer ln.Close() //nolint:errcheck

	gotReq := make(chan string, 1)
	go func() {
		conn, err := ln.Accept()
		if err != nil {
			return
		}
		defer conn.Close() //nolint:errcheck
		buf := make([]byte, 4096)
		n, _ := conn.Read(buf)
		gotReq <- string(buf[:n])
		_, _ = fmt.Fprint(conn, "HTTP/1.1 502 Bad Gateway\r\n\r\n")
	}()

	t.Setenv("HTTPS_PROXY", "http://"+ln.Addr().String())
	t.Setenv("HTTP_PROXY", "")
	t.Setenv("NO_PROXY", "")

	db, err := openDB("db.example.com", "3306", "u:p@tcp(db.example.com:3306)/db?timeout=3s")
	if err != nil {
		t.Fatalf("openDB() unexpected error: %v", err)
	}
	defer db.Close() //nolint:errcheck

	ctx, cancel := context.WithTimeout(context.Background(), 3*time.Second)
	defer cancel()
	_ = db.PingContext(ctx)

	select {
	case req := <-gotReq:
		if !strings.Contains(req, "CONNECT db.example.com:3306") {
			t.Fatalf("expected CONNECT to db.example.com:3306, got: %q", req)
		}
	case <-time.After(3 * time.Second):
		t.Fatal("timed out waiting for proxy CONNECT")
	}
}
