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
	"database/sql/driver"
)

// PasswordProvider returns credentials for a new physical database connection.
// Implementations must support concurrent calls and respect cancellation.
type PasswordProvider func(context.Context) (string, error)

// PasswordConnector obtains a fresh password on every physical connection,
// including connections database/sql opens when retrying a bad connection.
type PasswordConnector struct {
	Password     PasswordProvider
	NewConnector func(string) (driver.Connector, error)
	SQLDriver    driver.Driver
}

func (c *PasswordConnector) Connect(ctx context.Context) (driver.Conn, error) {
	password, err := c.Password(ctx)
	if err != nil {
		return nil, err
	}
	connector, err := c.NewConnector(password)
	if err != nil {
		return nil, err
	}
	return connector.Connect(ctx)
}

func (c *PasswordConnector) Driver() driver.Driver { return c.SQLDriver }
