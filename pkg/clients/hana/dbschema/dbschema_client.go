package dbschema

import (
	"context"
	"fmt"

	"github.com/crossplane-contrib/provider-sql/apis/hana/v1alpha1"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/hana"
	"github.com/crossplane-contrib/provider-sql/pkg/clients/xsql"
)

// Client struct holds the connection to the db
type Client struct {
	db xsql.DB
}

// New creates a new db client
func New(creds map[string][]byte) Client {
	return Client{
		db: hana.New(creds),
	}
}

// Read checks the state of the schema
func (c Client) Read(ctx context.Context, parameters *v1alpha1.DbSchemaParameters) (*v1alpha1.DbSchemaObservation, error) {

	observed := &v1alpha1.DbSchemaObservation{
		SchemaName: "",
		Owner:      "",
	}

	query := "SELECT SCHEMA_NAME, SCHEMA_OWNER FROM SYS.SCHEMAS WHERE SCHEMA_NAME = ?"

	err := c.db.Scan(ctx, xsql.Query{String: query, Parameters: []interface{}{parameters.SchemaName}}, &observed.SchemaName, &observed.Owner)
	if xsql.IsNoRows(err) {
		return observed, nil
	}
	if err != nil {
		return observed, err
	}

	return observed, nil
}

// Create a new schema
func (c Client) Create(ctx context.Context, parameters *v1alpha1.DbSchemaParameters, args ...any) error {

	query := fmt.Sprintf("CREATE SCHEMA %s", parameters.SchemaName)

	if parameters.Owner != "" {
		query += fmt.Sprintf(" OWNED BY %s", parameters.Owner)
	}

	err := c.db.Exec(ctx, xsql.Query{String: query})

	if err != nil {
		return err
	}

	return nil
}

// Delete an existing schema
func (c Client) Delete(ctx context.Context, parameters *v1alpha1.DbSchemaParameters) error {

	query := fmt.Sprintf("DROP SCHEMA %s", parameters.SchemaName)

	err := c.db.Exec(ctx, xsql.Query{String: query})

	if err != nil {
		return err
	}

	return nil
}
