# provider-sql

A [Crossplane] provider for SQL. Note that provider-sql orchestrates SQL servers
by creating databases, users, etc. It does not create SQL servers. provider-sql
can be used in conjunction with other providers (e.g. provider-azure) to define
a composite resource that creates both an SQL server and a new database.

To reduce load on the managed databases and increase responsiveness with many
managed resources, this provider reconciles it's managed resources every 10 minutes.

## PostgreSQL

### Database

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

### Extension

To create a PostgreSQL 'hstore' extension on database 'example':

```yaml
---
apiVersion: postgresql.sql.crossplane.io/v1alpha1
kind: Extension
metadata:
  name: example
spec:
  forProvider:
    extension: hstore
    databaseRef:
      name: example
```

### Role

To create a PostgreSQL role named 'example', that allows logins:

```yaml
---
apiVersion: postgresql.sql.crossplane.io/v1alpha1
kind: Role
metadata:
  name: example
spec:
  forProvider:
    privileges:
      login: true
  writeConnectionSecretToRef:
    name: example-role-secret
    namespace: crossplane-system
```

If no password is provided in `.spec.forProvider.passwordSecretRef`, a random one will be generated.

### Grant

To create a PostgreSQL role membership grant between 'parent-role' and 'child-role':

```yaml
---
apiVersion: postgresql.sql.crossplane.io/v1alpha1
kind: Grant
metadata:
  name: example-grant-role-membership
spec:
  forProvider:
    roleRef:
      name: example-role
    memberOfRef:
      name: parent-role
```

To create a PostgreSQL permissions grant on role 'example' and database 'example':

```yaml
---
apiVersion: postgresql.sql.crossplane.io/v1alpha1
kind: Grant
metadata:
  name: example-grant-connect-to-role-on-database
spec:
  forProvider:
    privileges:
      - CONNECT
    roleRef:
      name: example
    databaseRef:
      name: example
```

## MySQL

### Database

To create a MySQL database named 'example':

```yaml
---
apiVersion: mysql.sql.crossplane.io/v1alpha1
kind: ProviderConfig
metadata:
  name: default
spec:
  credentials:
    source: MySQLConnectionSecret
    connectionSecretRef:
      namespace: default
      name: db-conn
---
apiVersion: mysql.sql.crossplane.io/v1alpha1
kind: Database
metadata:
  name: example
spec: {}
```

### User

To create a MySQL user named 'example':

```yaml
apiVersion: mysql.sql.crossplane.io/v1alpha1
kind: User
metadata:
  name: example
spec:
  writeConnectionSecretToRef:
    name: example-user-secret
    namespace: crossplane-system
```

If no password is provided in `.spec.forProvider.passwordSecretRef`, a random one will be generated.

### Grant

To create a MySQL grant:

```yaml
apiVersion: mysql.sql.crossplane.io/v1alpha1
kind: Grant
metadata:
  name: example
spec:
  forProvider:
    privileges:
      - ALL
    userRef:
      name: example
    databaseRef:
      name: example
```

[Crossplane]: https://crossplane.io
