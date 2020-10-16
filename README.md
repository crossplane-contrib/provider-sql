# provider-sql

A [Crossplane] provider for SQL. Note that provider-sql orchestrates SQL servers
by creating databases, users, etc. It does not create SQL servers. provider-sql
can be used in conjunction with other providers (e.g. provider-azure) to define
a composite resource that creates both a SQL server and a new database.

[Crossplane]: https://crossplane.io
