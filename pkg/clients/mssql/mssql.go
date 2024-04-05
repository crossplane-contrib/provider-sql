/*
Copyright 2021 The Crossplane Authors.

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

package mssql

import (
	"context"
	"database/sql"
	"fmt"
	"net/url"
	"strings"

	"github.com/pkg/errors"

	xpv1 "github.com/crossplane/crossplane-runtime/apis/common/v1"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"

	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
)

const (
	schema        = "sqlserver"
	stdDriverName = "sqlserver"
	azDriverName  = "azuresql"

	errNotSupported = "%s not supported by MSSQL client"
	fedauth         = "fedauth"
)

type mssqlDB struct {
	dsn      string
	endpoint string
	port     string
	driver   string
}

// New returns a new mssql database client.
func New(creds map[string][]byte, database string) xsql.DB {
	endpoint := string(creds[xpv1.ResourceCredentialsSecretEndpointKey])
	port := string(creds[xpv1.ResourceCredentialsSecretPortKey])
	driver := stdDriverName
	host := endpoint
	if port != "" {
		host = fmt.Sprintf("%s:%s", endpoint, port)
	}

	query := url.Values{}
	if database != "" {
		query.Add("database", database)
	}
	var u *url.URL
	if val, ok := creds[fedauth]; ok {
		authType := string(val)
		query.Add(fedauth, authType)
		if authType == "ActiveDirectoryServicePrincipal" || authType == "ActiveDirectoryApplication" || authType == "ActiveDirectoryPassword" {
			query.Add("password", string(creds[xpv1.ResourceCredentialsSecretPasswordKey]))
		}
		if val, ok := creds[xpv1.ResourceCredentialsSecretUserKey]; ok {
			query.Add("user id", string(val))
		}
		u = &url.URL{
			Scheme:   schema,
			Host:     host,
			RawQuery: query.Encode(),
		}
		driver = azDriverName
	} else {

		u = &url.URL{
			Scheme:   schema,
			User:     url.UserPassword(string(creds[xpv1.ResourceCredentialsSecretUserKey]), string(creds[xpv1.ResourceCredentialsSecretPasswordKey])),
			Host:     host,
			RawQuery: query.Encode(),
		}
	}
	return mssqlDB{
		dsn:      u.String(),
		endpoint: endpoint,
		port:     port,
		driver:   driver,
	}
}

// ExecTx is unsupported in mssql.
func (c mssqlDB) ExecTx(_ context.Context, _ []xsql.Query) error {
	return errors.Errorf(errNotSupported, "transactions")
}

// Exec the supplied query.
func (c mssqlDB) Exec(ctx context.Context, q xsql.Query) error {
	d, err := sql.Open(c.driver, c.dsn)
	if err != nil {
		return err
	}
	defer d.Close() //nolint:errcheck

	_, err = d.ExecContext(ctx, q.String, q.Parameters...)
	return err
}

// Query the supplied query.
func (c mssqlDB) Query(ctx context.Context, q xsql.Query) (*sql.Rows, error) {
	d, err := sql.Open(c.driver, c.dsn)
	if err != nil {
		return nil, err
	}
	defer d.Close() //nolint:errcheck

	return d.QueryContext(ctx, q.String, q.Parameters...)
}

// Scan the results of the supplied query into the supplied destination.
func (c mssqlDB) Scan(ctx context.Context, q xsql.Query, dest ...interface{}) error {
	db, err := sql.Open(c.driver, c.dsn)
	if err != nil {
		return err
	}
	defer db.Close() //nolint:errcheck

	return db.QueryRowContext(ctx, q.String, q.Parameters...).Scan(dest...)
}

// GetConnectionDetails returns the connection details for a user of this DB
func (c mssqlDB) GetConnectionDetails(username, password string) managed.ConnectionDetails {
	return managed.ConnectionDetails{
		xpv1.ResourceCredentialsSecretUserKey:     []byte(username),
		xpv1.ResourceCredentialsSecretPasswordKey: []byte(password),
		xpv1.ResourceCredentialsSecretEndpointKey: []byte(c.endpoint),
		xpv1.ResourceCredentialsSecretPortKey:     []byte(c.port),
	}
}

// QuoteIdentifier for mssql queries
func QuoteIdentifier(id string) string {
	return "[" + id + "]"
}

// QuoteValue for mssql queries
func QuoteValue(id string) string {
	return "'" + strings.ReplaceAll(id, "'", "''") + "'"
}
