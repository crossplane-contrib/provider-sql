# Crossplane Provider for SQL

A [Crossplane] provider for SQL.
A [Crossplane] provider for RDBMS schema management/manipulation. Note that
`provider-sql` orchestrates relational database servers by creating databases,
users, etc. It does not create server instances themselves. `provider-sql` can
be used in conjunction with other providers (e.g. provider-azure) to define a
composite resource that creates both an RDBMS server and a new database schema.

To reduce load on the managed databases and increase responsiveness with many
managed resources, this provider reconciles its managed resources every 10 minutes.

It currently supports **MySQL**, **PostgreSQL** and **MSSQL**.

## Usage

1. Create a connection secret:

To create resources in this repository, you will first need a k8s secret
with the connection details to an existing SQL server.

This could either be [created automatically] by provisioning an SQL server with
a Crossplane provider (e.g. a [CloudSQLInstance] with provider-gcp) or you can
create for an existing server as follows:

```
kubectl create secret generic db-conn \
  --from-literal=username=admin \
  --from-literal=password=t0ps3cr3t \
  --from-literal=endpoint=my.sql-server.com \
  --from-literal=port=3306
```

2. Create managed resource for your SQL server flavor:

- [**MySQL**]: `Database`, `Grant`, `User`
- [**PostgreSQL**]: `Database`, `Grant`, `Extension`, `Role`
- [**MSSQL**]: `Database`, `Grant`, `User`

[Crossplane]: https://crossplane.io
[CloudSQLInstance]: https://doc.crds.dev/github.com/crossplane/provider-gcp/database.gcp.crossplane.io/CloudSQLInstance/v1beta1@v0.18.0
[created automatically]: https://crossplane.io/docs/v1.5/concepts/managed-resources.html#connection-details
[**MySQL**]: examples/mysql
[**PostgreSQL**]: examples/postgresql
[**MSSQL**]: examples/mssql
