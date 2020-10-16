# provider-sql

A [Crossplane] provider for SQL. Note that provider-sql orchestrates SQL servers
by creating databases, users, etc. It does not create SQL servers. provider-sql
can be used in conjunction with other providers (e.g. provider-azure) to define
a composite resource that creates both an SQL server and a new database.

## PostgreSQL

To create a PostgreSQL database named 'example':

```yaml
---
apiVersion: postgresql.sql.crossplane.io/v1alpha1
kind: ProviderConfig
metadata:
  name: default
spec:
  credentials:
    source: PostgreSQLConnectionSecret
    connectionSecretRef:
      namespace: default
      name: db-conn
---
apiVersion: postgresql.sql.crossplane.io/v1alpha1
kind: Database
metadata:
  name: example
spec:
  forProvider: {}
```

[Crossplane]: https://crossplane.io
