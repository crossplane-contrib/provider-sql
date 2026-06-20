# AWS IAM database authentication

provider-sql can authenticate to Amazon Aurora / RDS (MySQL and PostgreSQL)
using [IAM database authentication](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/UsingWithRDS.IAMDBAuth.html)
instead of a static password. The provider generates a short-lived IAM token at
connection time and uses it as the database password. Combined with EKS Pod
Identity or IRSA, this removes static database credentials entirely.

Select it with the `AWSIAMAuth` credentials source on a `ProviderConfig`:

```yaml
spec:
  credentials:
    source: AWSIAMAuth
    connectionSecretRef:
      name: aurora-conn   # endpoint, port, username - no password
    # region: us-east-1   # optional (see "Region resolution")
```

See the runnable examples: `examples/{cluster,namespaced}/{mysql,postgresql}/config_iam.yaml`.

## Prerequisites

1. **Enable IAM authentication** on the Aurora cluster / RDS instance.

2. **Create an IAM-enabled database user for the provider to connect as.** This
   is a one-time bootstrap that needs a normal (password) connection, performed
   out of band - the provider does not do it. Use a **dedicated provisioning
   role, not the master user** (granting `rds_iam` to the Postgres master
   permanently disables its password auth, and the MySQL plugin is fixed at
   `CREATE USER` time):

   - PostgreSQL: `CREATE USER crossplane_admin; GRANT rds_iam TO crossplane_admin;`
     plus the privileges it needs to manage your databases/roles/grants.
   - MySQL: `CREATE USER 'crossplane_admin'@'%' IDENTIFIED WITH AWSAuthenticationPlugin AS 'RDS';`
     plus the required `GRANT`s.

   (Creating these users is also possible declaratively through provider-sql's
   own `Role`/`Grant` resources from a password-based `ProviderConfig`.)

3. **Allow the controller's IAM identity to connect** by attaching an
   `rds-db:connect` policy for that database user, and give the provider pod that
   identity via [EKS Pod Identity](https://docs.aws.amazon.com/eks/latest/userguide/pod-identities.html)
   or [IRSA](https://docs.aws.amazon.com/eks/latest/userguide/iam-roles-for-service-accounts.html).
   Credentials are discovered from the environment by the AWS SDK.

4. **Provide a connection secret** containing only `endpoint`, `port` and
   `username` (no `password`).

## TLS and the Amazon RDS certificate authority

IAM authentication requires TLS - RDS rejects unencrypted connections. To
*verify* the server certificate, the client needs the Amazon RDS CA, which is
**not** in the default system trust store. provider-sql does **not** ship the RDS
CA bundle; making it available to the provider pod is an operator concern. Two
common approaches:

- **[trust-manager](https://cert-manager.io/docs/trust/trust-manager/)** - add the
  [Amazon RDS CA bundle](https://truststore.pki.rds.amazonaws.com/global/global-bundle.pem)
  to the provider pod's trust store. Then set `tls: "true"` (MySQL) or
  `sslMode: verify-full` (PostgreSQL) and the certificate verifies normally.
- **Mounted secret** - mount the RDS CA and reference it through the provider's
  existing custom TLS mechanism (`tls: custom` + `tlsConfig` for MySQL;
  `sslMode: verify-full` with the CA for PostgreSQL).

If you do neither, IAM auth still works but the connection is **encrypted
without certificate verification**:

| Engine | Verified (RDS CA available) | Default fallback |
|--------|-----------------------------|------------------|
| MySQL | `tls: "true"` or `tls: custom` | `skip-verify` (encrypted, unverified) |
| PostgreSQL | `sslMode: verify-full` | `sslMode: require` (encrypted, unverified) |

The provider always enforces at least an encrypted connection for IAM auth.

## Region resolution

The region used to sign the token is resolved in order:

1. `spec.credentials.region` on the `ProviderConfig`
2. a `region` key in the connection secret
3. the controller's AWS environment (`AWS_REGION` / instance metadata)

If none is set the token cannot be signed correctly, so configure one of them.

## Notes

- The provider opens a fresh connection each reconcile and generates a new token
  each time, so the 15-minute token lifetime is never an issue.
- Setting `AWSIAMAuth` forces TLS on even if a weaker `tls`/`sslMode` was set.
