/*
Copyright 2026 The Crossplane Authors.

Licensed under the Apache License, Version 2.0 (the "License");
you may not use this file except in compliance with the License.
You may obtain a copy of the License at

    http://www.apache.org/licenses/LICENSE-2.0

Unless required by applicable law or agreed to in writing, software
distributed under the License is distributed on an "AS IS" BASIS,
WITHOUT WARRANTIES OR CONDITIONS OF ANY KIND, either express or implied.
See the License for the specific language governing permissions and
limitations under the License.
*/

package xsql

import (
	"context"
	"database/sql"
	"database/sql/driver"
	"errors"
	"fmt"
	"io"
	"sync"
	"sync/atomic"
	"testing"
	"time"
)

type tokenTestDriver struct{}

func (tokenTestDriver) Open(string) (driver.Conn, error) {
	return nil, errors.New("unexpected Driver.Open: bypassed password connector")
}

type tokenTestConnector struct {
	connect func(context.Context) (driver.Conn, error)
}

func (c tokenTestConnector) Driver() driver.Driver                            { return tokenTestDriver{} }
func (c tokenTestConnector) Connect(ctx context.Context) (driver.Conn, error) { return c.connect(ctx) }

type tokenTestConn struct{ bad *atomic.Bool }

func (tokenTestConn) Prepare(string) (driver.Stmt, error) {
	return nil, errors.New("unexpected prepare")
}
func (tokenTestConn) Close() error              { return nil }
func (tokenTestConn) Begin() (driver.Tx, error) { return nil, errors.New("unexpected transaction") }
func (c tokenTestConn) QueryContext(context.Context, string, []driver.NamedValue) (driver.Rows, error) {
	if c.bad != nil && c.bad.Swap(false) {
		return nil, driver.ErrBadConn
	}
	return &tokenTestRows{}, nil
}

type tokenTestRows struct{ read bool }

func (*tokenTestRows) Columns() []string { return []string{"value"} }
func (*tokenTestRows) Close() error      { return nil }
func (r *tokenTestRows) Next(values []driver.Value) error {
	if r.read {
		return io.EOF
	}
	values[0] = int64(1)
	r.read = true
	return nil
}

func TestPasswordConnectorRefreshesAfterExpiryAndBadConnection(t *testing.T) {
	now := time.Unix(0, 0)
	var tokens []string
	var retry atomic.Bool
	connector := &PasswordConnector{
		Password:  func(context.Context) (string, error) { return fmt.Sprintf("signed-at-%d", now.Unix()), nil },
		SQLDriver: tokenTestDriver{},
		NewConnector: func(password string) (driver.Connector, error) {
			want := fmt.Sprintf("signed-at-%d", now.Unix())
			if password != want {
				return nil, errors.New("expired token")
			}
			tokens = append(tokens, password)
			return tokenTestConnector{connect: func(context.Context) (driver.Conn, error) { return tokenTestConn{bad: &retry}, nil }}, nil
		},
	}
	db := sql.OpenDB(connector)
	defer db.Close()
	query := func() {
		t.Helper()
		var value int
		if err := db.QueryRowContext(context.Background(), "SELECT 1").Scan(&value); err != nil {
			t.Fatal(err)
		}
	}
	query()
	now = now.Add(20 * time.Minute)
	// Existing authenticated sessions survive token expiry; a broken session
	// makes database/sql retry with a new physical connection and a fresh token.
	retry.Store(true)
	query()
	if len(tokens) != 2 || tokens[0] == tokens[1] {
		t.Fatalf("expected fresh token after reconnect, got %v", tokens)
	}
	db.SetMaxIdleConns(0)
	now = now.Add(20 * time.Minute)
	query()
	if len(tokens) != 3 || tokens[1] == tokens[2] {
		t.Fatal("later physical connection reused an old token")
	}
}

func TestPasswordConnectorErrorsAndCancellation(t *testing.T) {
	boom := errors.New("signing failed")
	c := &PasswordConnector{Password: func(context.Context) (string, error) { return "", boom }, SQLDriver: tokenTestDriver{}, NewConnector: func(string) (driver.Connector, error) {
		t.Fatal("must not connect after signing error")
		return nil, nil
	}}
	if _, err := c.Connect(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("lost signing error: %v", err)
	}
	c.Password = func(ctx context.Context) (string, error) { return "", ctx.Err() }
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := c.Connect(ctx); !errors.Is(err, context.Canceled) {
		t.Fatalf("lost cancellation: %v", err)
	}
	c.Password = func(context.Context) (string, error) { return "token", nil }
	c.NewConnector = func(string) (driver.Connector, error) { return nil, boom }
	if _, err := c.Connect(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("lost connector error: %v", err)
	}
	c.NewConnector = func(string) (driver.Connector, error) {
		return tokenTestConnector{connect: func(context.Context) (driver.Conn, error) { return nil, boom }}, nil
	}
	if _, err := c.Connect(context.Background()); !errors.Is(err, boom) {
		t.Fatalf("lost connection error: %v", err)
	}
}

func TestPasswordConnectorConcurrentConnections(t *testing.T) {
	var issued atomic.Int64
	var seen sync.Map
	c := &PasswordConnector{Password: func(context.Context) (string, error) { return fmt.Sprint(issued.Add(1)), nil }, SQLDriver: tokenTestDriver{}, NewConnector: func(password string) (driver.Connector, error) {
		if _, loaded := seen.LoadOrStore(password, true); loaded {
			return nil, errors.New("reused token")
		}
		return tokenTestConnector{connect: func(context.Context) (driver.Conn, error) { return tokenTestConn{}, nil }}, nil
	}}
	var workers sync.WaitGroup
	for range 20 {
		workers.Go(func() {
			conn, err := c.Connect(context.Background())
			if err != nil {
				t.Error(err)
				return
			}
			_ = conn.Close()
		})
	}
	workers.Wait()
	if issued.Load() != 20 {
		t.Fatalf("want 20 independently generated tokens, got %d", issued.Load())
	}
}
