# MySQL driver context cancellation does not stop server-side statements

This document records a behavior of `github.com/go-sql-driver/mysql` that has
operational consequences for provider-sql, why it caused a production incident
(MySQL/Percona becoming unresponsive with many `ALTER USER` statements pending),
and how the provider mitigates it.

## TL;DR

- When a Go `context` is cancelled (including on deadline), the MySQL driver
  **closes the TCP socket but does not send `KILL QUERY`**. The statement keeps
  running / waiting on the server.
- This is **intentional, longstanding driver behavior** and is present in every
  release up to and including the current latest, `v1.10.0`. **Upgrading the
  driver does not fix it** — it is not a bug to be patched away.
- Combined with MySQL's default `lock_wait_timeout` of `31536000` seconds
  (1 year), a statement blocked on a metadata / ACL lock stays pending forever
  even after the provider gives up, and every reconcile retry adds another one.
  They accumulate until the server runs out of connections or its ACL subsystem
  is wedged.
- Mitigation: provider-sql now appends `lock_wait_timeout` (and a dial
  `timeout`) to the MySQL DSN so blocked statements **fail fast server-side and
  release**, instead of piling up. See `pkg/clients/mysql/mysql.go`.

## The driver behavior (verified in v1.10.0)

On query/exec the driver arms a watcher goroutine
(`connection.go` `startWatcher`). When `ctx.Done()` fires it calls
`mc.cancel(err)` → `mc.cleanup()`, and `cleanup()` simply does
`close(mc.closech)` and `conn.Close()` on the raw network connection. There is
no code path that opens a side connection and issues `KILL QUERY <id>`.

From Go's point of view the query is cancelled and the call returns
`context.DeadlineExceeded`. From MySQL's point of view the session's statement
continues: for a statement blocked in `Waiting for metadata lock`, the server
thread does **not** poll socket liveness during the lock wait, so it keeps
waiting until it acquires the lock (then notices the dead client) or is killed
out of band.

The driver maintainers' recommended way to actually cancel server-side is the
application's job: check out a dedicated connection, read its
`CONNECTION_ID()`, and issue `KILL QUERY <id>` from a **separate** connection
on cancel. provider-sql does not do this (see "What this does not cover").

References:
- <https://github.com/go-sql-driver/mysql/issues/863> — query keeps running after context cancel
- <https://github.com/go-sql-driver/mysql/issues/925> — context cancellation semantics
- <https://github.com/go-sql-driver/mysql/issues/1631> — DeadlineExceeded closes the network connection
- <https://medium.com/@rocketlaunchr.cloud/canceling-mysql-in-go-827ed8f83b30> — the app-level `KILL QUERY` pattern

## Why it caused an incident here

1. **Every write goes through one path.** All MySQL controllers issue writes via
   `mysql.ExecWrapper → c.db.Exec`, which opens a fresh connection per call
   (`pkg/clients/mysql/mysql.go`). This covers, in the User controller,
   `CREATE USER`, `ALTER USER ... WITH <connection limits/resource options>`,
   `ALTER USER ... IDENTIFIED WITH <plugin>`, `ALTER USER ... IDENTIFIED BY`,
   `DROP USER`; in Grant, `GRANT` / `REVOKE`; in Database,
   `CREATE/ALTER/DROP DATABASE`. Password rotation is only the *most frequent*
   trigger — any of these statements can block.

2. **The reconcile deadline is 60s.** crossplane-runtime applies a default
   `reconcileTimeout` of 1 minute (provider-sql does not override it via
   `managed.WithTimeout`). So the provider goroutine is *not* hung forever — it
   is cancelled at ~60s.

3. **But cancellation only closes the socket (see above).** The abandoned
   `ALTER USER` / `GRANT` keeps waiting on the server because of the 1-year
   default `lock_wait_timeout`.

4. **Retries re-issue the statement.** The 60s cancellation surfaces as an
   error, crossplane requeues with backoff, and the next reconcile opens a new
   connection and issues the statement again — which also blocks.

5. **The ACL global lock amplifies it.** MySQL account-management statements
   (`CREATE/ALTER/DROP USER`, `GRANT`, `REVOKE`) serialize on a single global
   ACL lock. One stuck statement stalls *every* account-management statement
   server-wide, not just ones touching the same object.

6. **A feedback loop keeps it going.** Controllers persist observed state only
   *after* a successful statement. While the statement keeps failing, observed
   state never updates, so the next reconcile re-diffs and re-issues the same
   blocked statement every poll cycle.

Net effect: blocked statements and their connections accumulate until
`max_connections` is exhausted and/or ACL contention wedges account management
— the database appears unresponsive.

A common real-world trigger for the initial block is a backup
(e.g. Percona XtraBackup / `FLUSH TABLES WITH READ LOCK`) or a long-running
transaction holding a lock that account-management DDL must wait behind.

## Mitigation implemented

`DSN()` in `pkg/clients/mysql/mysql.go` now appends:

- `lock_wait_timeout=30` — an unrecognised driver parameter, so go-sql-driver
  issues `SET lock_wait_timeout = 30` on connect. A statement that cannot get
  its metadata / ACL lock within 30s **fails server-side and releases** instead
  of waiting up to a year. The provider then retries on the normal reconcile
  backoff once the lock clears.
- `timeout=10s` — the driver's dial timeout, so a reconcile does not block on
  an unreachable endpoint.

This directly breaks the pile-up chain for the lock-wait case that caused the
incident, and it applies to every MySQL controller (User, Grant, Database, both
cluster- and namespaced-scoped) because they all share this client.

## What this does *not* cover

`lock_wait_timeout` only bounds waits for metadata / ACL locks. A statement
hung for a *non-lock* reason — a dead network path, or a server wedged
mid-execution — is still only socket-closed-without-`KILL` at the 60s reconcile
deadline, and can continue running server-side. The only remedy for that class
is the application-level `KILL QUERY` pattern described above.

## Recommended follow-ups (not in this change)

- **Application-level `KILL QUERY` on cancel** for the non-lock hang case
  (dedicated connection + `CONNECTION_ID()` + out-of-band `KILL`).
- **Bounded shared connection pool.** The client currently calls `sql.Open` per
  query and closes it immediately (no reuse, no `SetMaxOpenConns` /
  `SetConnMaxLifetime`). A shared, capped `*sql.DB` would hard-limit total
  connections and remove per-query connect/auth overhead, but requires adding a
  `Close()` to the `xsql.DB` interface and wiring it through `Disconnect` for
  all three drivers (MySQL, PostgreSQL, MSSQL).
- **Harden the reconcile feedback loop** so a persistently failing statement
  does not re-fire every poll cycle.
- **Make `lock_wait_timeout` configurable** (e.g. via `ProviderConfig`) for
  environments that legitimately need longer or shorter waits.

## Reproducing / verifying

1. Create a MySQL `User` with a `PasswordSecretRef`.
2. In a separate MySQL session, hold a lock the DDL must wait behind
   (e.g. `LOCK TABLES mysql.user WRITE`, or an open transaction).
3. Rotate the password so the provider issues `ALTER USER ... IDENTIFIED BY`.

- **Before the fix:** `SHOW PROCESSLIST` accumulates
  `ALTER USER ... | Waiting for metadata lock` threads and the connection count
  climbs with every reconcile.
- **After the fix:** the `ALTER USER` fails with a lock-wait-timeout error
  within ~30s and is retried on backoff; `SHOW PROCESSLIST` stays clean and the
  connection count stays flat.
