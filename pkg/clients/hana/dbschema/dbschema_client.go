package dbschema

import (
	"context"
	"fmt"
	"github.com/crossplane-contrib/provider-sql/apis/hana/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/hana"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
	"github.com/crossplane/crossplane-runtime/pkg/reconciler/managed"
	"github.com/pkg/errors"
	"strings"
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

func (c Client) Observe(ctx context.Context, parameters *v1alpha1.DbSchemaParameters) (managed.ExternalObservation, error) {

	observed := &v1alpha1.DbSchemaObservation{
		Name:  "",
		Owner: "",
	}

	schemaName := strings.ToUpper(parameters.Name)

	query := "SELECT SCHEMA_NAME, SCHEMA_OWNER FROM SYS.SCHEMAS WHERE SCHEMA_NAME = ?"

	err := c.db.Scan(ctx, xsql.Query{String: query, Parameters: []interface{}{schemaName}}, &observed.Name, &observed.Owner)
	if xsql.IsNoRows(err) {
		return managed.ExternalObservation{ResourceExists: false}, nil
	}
	if err != nil {
		return managed.ExternalObservation{}, errors.Wrap(err, errSelectSchema)
	}

	return managed.ExternalObservation{
		ResourceExists:    true,
		ResourceUpToDate:  true,
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c Client) Create(ctx context.Context, parameters *v1alpha1.DbSchemaParameters) (managed.ExternalCreation, error) {

	query := fmt.Sprintf("CREATE SCHEMA %s", parameters.Name)

	if parameters.Owner != "" {
		query += fmt.Sprintf(" OWNED BY %s", parameters.Owner)
	}

	err := c.db.Exec(ctx, xsql.Query{String: query})

	if err != nil {
		return managed.ExternalCreation{}, errors.Wrap(err, errCreateSchema)
	}

	return managed.ExternalCreation{
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c Client) Update(ctx context.Context, parameters *v1alpha1.DbSchemaParameters) (managed.ExternalUpdate, error) {

	//TODO

	return managed.ExternalUpdate{
		ConnectionDetails: managed.ConnectionDetails{},
	}, nil
}

func (c Client) Delete(ctx context.Context, parameters *v1alpha1.DbSchemaParameters) error {

	query := fmt.Sprintf("DROP SCHEMA %s", parameters.Name)

	err := c.db.Exec(ctx, xsql.Query{String: query})

	if err != nil {
		return errors.Wrap(err, errDropSchema)
	}

	return nil
}
