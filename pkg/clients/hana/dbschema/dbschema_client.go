package dbschema

import (
	"context"
	"fmt"
	"strings"

	"github.com/pkg/errors"

	"github.com/crossplane-contrib/provider-sql/apis/hana/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/hana"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
)

const (
	errSelectSchema = "cannot select schema"
	errCreateSchema = "cannot create schema"
	errDropSchema   = "cannot drop schema"
)

type Client struct {
	db xsql.DB
}

func New(creds map[string][]byte) Client {
	return Client{
		db: hana.New(creds),
	}
}

func (c Client) Observe(ctx context.Context, parameters *v1alpha1.DbSchemaParameters) (*v1alpha1.DbSchemaObservation, error) {

	observed := &v1alpha1.DbSchemaObservation{
		Name:  "",
		Owner: "",
	}

	schemaName := strings.ToUpper(parameters.Name)

	query := "SELECT SCHEMA_NAME, SCHEMA_OWNER FROM SYS.SCHEMAS WHERE SCHEMA_NAME = ?"

	err := c.db.Scan(ctx, xsql.Query{String: query, Parameters: []interface{}{schemaName}}, &observed.Name, &observed.Owner)
	if xsql.IsNoRows(err) {
		return observed, nil
	}
	if err != nil {
		return observed, errors.Wrap(err, errSelectSchema)
	}

	return observed, nil
}

func (c Client) Create(ctx context.Context, parameters *v1alpha1.DbSchemaParameters, args ...any) error {

	query := fmt.Sprintf("CREATE SCHEMA %s", parameters.Name)

	if parameters.Owner != "" {
		query += fmt.Sprintf(" OWNED BY %s", parameters.Owner)
	}

	err := c.db.Exec(ctx, xsql.Query{String: query})

	if err != nil {
		return errors.Wrap(err, errCreateSchema)
	}

	return nil
}

func (c Client) Update(ctx context.Context, parameters *v1alpha1.DbSchemaParameters, args ...any) error {

	// TODO

	return nil
}

func (c Client) Delete(ctx context.Context, parameters *v1alpha1.DbSchemaParameters) error {

	query := fmt.Sprintf("DROP SCHEMA %s", parameters.Name)

	err := c.db.Exec(ctx, xsql.Query{String: query})

	if err != nil {
		return errors.Wrap(err, errDropSchema)
	}

	return nil
}
