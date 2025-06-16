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

## Install

Install the provider by using the following command after changing the image tag to the [latest release](https://marketplace.upbound.io/providers/crossplane-contrib/provider-sql/):

```bash
cat << EOF | kubectl apply -f -
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: provider-sql
spec:
  package: xpkg.upbound.io/crossplane-contrib/provider-sql:v0.9.0
EOF
```

Alternatively, you can use Crossplane CLI:
```bash
up ctp provider install xpkg.upbound.io/crossplane-contrib/provider-sql:v0.9.0
```

Check the example:

- [Provider](./examples/provider.yaml)
- [deploymentRuntimeConfig](./examples/deploymentRuntimeConfig.yaml)


## Usage

1. Create a connection secret:

   To create provider-sql managed resources, you will first need a K8s secret
   with the connection details to an existing SQL server.

   This secret could either be [created automatically] by provisioning an SQL server
   with a Crossplane provider (e.g. a [CloudSQLInstance] with provider-gcp) or you can
   create for an existing server as follows:

   ```
   kubectl create secret generic db-conn \
     --from-literal=username=admin \
     --from-literal=password='t0ps3cr3t' \
     --from-literal=endpoint=my.sql-server.com \
     --from-literal=port=3306
   ```

2. Create managed resources for your SQL server flavor:

   - **MySQL**: `Database`, `Grant`, `User` (See [the examples](examples/mysql))
   - **PostgreSQL**: `Database`, `Grant`, `Extension`, `Role` (See [the examples](examples/postgresql))
   - **MSSQL**: `Database`, `Grant`, `User` (See [the examples](examples/mssql))

[crossplane]: https://crossplane.io
[cloudsqlinstance]: https://doc.crds.dev/github.com/crossplane/provider-gcp/database.gcp.crossplane.io/CloudSQLInstance/v1beta1@v0.18.0
[created automatically]: https://crossplane.io/docs/v1.5/concepts/managed-resources.html#connection-details

## Contributing

1. Fork the project and clone locally.
2. Create a branch with the changes.
3. Install go version 1.18.
4. Run `make` to initialize the "build". Make submodules used for CI/CD.
5. Run `make reviewable` to run code generation, linters, and tests.
6. Commit, push, and PR.
