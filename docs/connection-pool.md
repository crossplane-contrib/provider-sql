# Database connection pooling

provider-sql reuses a bounded database connection pool for the queries it issues
against each database server. Pools are shared process-wide and keyed by driver
and DSN, so every managed resource that targets the same server (via the same
credentials) shares one pool. This caps the number of connections the provider
holds against any one server and avoids opening a fresh connection for every
query.

The pool is tunable per `ProviderConfig` (and `ClusterProviderConfig`) for all
three databases — MySQL, PostgreSQL, and MSSQL.

## Configuration

Add a `connectionPool` block to the ProviderConfig spec. Every field is
optional; any field you omit (or omitting `connectionPool` entirely) uses the
default.

```yaml
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
  connectionPool:
    maxOpenConnections: 10
    maxIdleConnections: 5
    maxConnLifetime: 1h
    maxConnIdleTime: 10m
```

| Field | Default | Meaning |
|-------|---------|---------|
| `maxOpenConnections` | `10` | Maximum open connections to the server. `0` means unlimited. |
| `maxIdleConnections` | `5` | Maximum idle connections retained in the pool. |
| `maxConnLifetime` | `1h` | Maximum time a connection may be reused before being closed. Go duration string (e.g. `30m`, `1h`). |
| `maxConnIdleTime` | `10m` | Maximum time a connection may sit idle before being closed. Go duration string. |

These map directly to the Go `database/sql` pool knobs
(`SetMaxOpenConns`, `SetMaxIdleConns`, `SetConnMaxLifetime`,
`SetConnMaxIdleTime`).

## Behavior notes

- **Shared per (driver, DSN).** Resources using the same credentials against the
  same server share a single pool, so `maxOpenConnections` bounds the provider's
  total concurrent connections to that server — not per resource.
- **First writer wins.** Pool settings are applied when the pool is first
  created for a DSN. In normal use a DSN maps to one ProviderConfig, so its
  settings are stable.
- **Credential rotation.** A password change produces a new DSN and therefore a
  new pool; the old pool is closed and evicted after it goes unused
  (30 minutes), and its idle connections are reaped sooner by `maxConnIdleTime`.
- **Sizing.** Each controller reconciles up to 5 resources concurrently. If you
  run many resources against one server, keep `maxOpenConnections` high enough
  that reconciles are not serialized waiting for a free connection, but within
  the server's own connection limit.
