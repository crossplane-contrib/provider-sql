# AWS IAM database authentication

provider-sql can authenticate to Amazon Aurora / RDS (MySQL and PostgreSQL)
using [IAM database authentication](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/UsingWithRDS.IAMDBAuth.html)
instead of a static password. The provider generates a short-lived IAM token at
each physical connection attempt and uses it as the database password. Combined with EKS Pod
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
   role, not the master user**. In PostgreSQL, granting `rds_iam` gives IAM
   authentication precedence over password authentication for that role:

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

### IAM identity and ProviderConfig permissions

All IAM ProviderConfigs use the provider pod's AWS identity. A namespace does
not give a ProviderConfig a separate IAM role. Anyone allowed to create an IAM
ProviderConfig and its connection Secret can select any database user that the
pod's `rds-db:connect` policy permits.

Restrict that policy to the required database resource IDs and usernames, for
example `arn:aws:rds-db:eu-west-1:123456789012:dbuser:db-RESOURCEID/crossplane_admin`,
and restrict who can create or modify ProviderConfigs through Kubernetes RBAC.
Use separate provider installations and IAM roles for independent trust boundaries.
See [the RDS IAM policy format](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/UsingWithRDS.IAMDBAuth.IAMPolicy.html).

## TLS and the Amazon RDS certificate authority

Supply the Amazon RDS CA through `tlsConfig.caCert.secretRef` on the
ProviderConfig. Both PostgreSQL and MySQL support a CA bundle without a client
certificate. No provider-pod volume mount, `SSL_CERT_FILE` setting or custom
container image is needed.

Download the public RDS certificate bundle and create a Secret:

```sh
curl --fail --silent --show-error \
  https://truststore.pki.rds.amazonaws.com/global/global-bundle.pem \
  --output rds-global-bundle.pem
kubectl -n crossplane-system create secret generic rds-ca \
  --from-file=ca-bundle.pem=rds-global-bundle.pem \
  --dry-run=client -o yaml | kubectl apply --server-side -f -
```

For a cluster-scoped PostgreSQL ProviderConfig:

```yaml
spec:
  credentials:
    source: AWSIAMAuth
    connectionSecretRef:
      name: aurora-conn
      namespace: crossplane-system
  sslMode: verify-full
  tlsConfig:
    caCert:
      secretRef:
        name: rds-ca
        namespace: crossplane-system
        key: ca-bundle.pem
```

For a namespaced PostgreSQL ProviderConfig, create the CA Secret in that
ProviderConfig's namespace and omit `namespace` from the selector:

```yaml
spec:
  credentials:
    source: AWSIAMAuth
    connectionSecretRef:
      name: aurora-conn
  sslMode: verify-full
  tlsConfig:
    caCert:
      secretRef:
        name: rds-ca
        key: ca-bundle.pem
```

ClusterProviderConfigs use an explicit Secret namespace, as in the first example.
The same CA selector works with password authentication. PostgreSQL CA selectors
require `verify-ca` or `verify-full`; other modes are rejected when a selector is
present. This also applies to IAM: `sslMode: require` with `tlsConfig` is invalid;
use `verify-full` (the default) or `verify-ca` instead. `verify-ca` verifies the
certificate chain; `verify-full` also verifies
the endpoint hostname.

Use `verify-full` for IAM authentication with the actual RDS endpoint hostname.
With `verify-ca`, a broad trust bundle can accept a certificate for a different
server and expose the IAM token. Keep the selected bundle limited to RDS CAs for
RDS connections. See [PostgreSQL's certificate verification guidance](https://www.postgresql.org/docs/current/libpq-ssl.html).

For MySQL, select `tls: custom` and provide the CA Secret:

```yaml
spec:
  credentials:
    source: AWSIAMAuth
    connectionSecretRef:
      name: aurora-conn
      namespace: crossplane-system # omit for a namespaced ProviderConfig
  tls: custom
  tlsConfig:
    caCert:
      secretRef:
        name: rds-ca
        namespace: crossplane-system
        key: ca-bundle.pem
```

MySQL's existing selectors require an explicit namespace. For a namespaced
ProviderConfig, use a Secret in its own namespace; existing MySQL selectors still
allow cross-namespace references and do not enforce tenant isolation. Restrict
ProviderConfig writes with RBAC. If mutual TLS is needed, provide both MySQL
`clientCert` and `clientKey`; an incomplete pair is rejected.

Maintain the Secret as RDS CAs rotate. The provider reloads the selected bundle
on each reconcile, so updates take effect on subsequent connections without a
provider restart. [trust-manager](https://cert-manager.io/docs/trust/trust-manager/)
can distribute the bundle to Secrets; configure the selector to read its output.
The provider does not download or embed the RDS CA.

Without a CA selector, existing driver trust-store behavior is preserved.
PostgreSQL's API defaults an omitted `sslMode` to `verify-full`; IAM preserves
`require`, `verify-ca` and `verify-full`, and upgrades `disable` to `require`.
`require` only encrypts unless lib/pq finds an explicit root certificate file;
loading system roots through `SSL_CERT_FILE` alone does not enable verification
in that mode. MySQL IAM upgrades omitted or `preferred` TLS to verified `true`;
that mode requires the CA in the driver's system pool. Explicit `skip-verify`
remains an operator choice. Kubernetes rejects explicit empty PostgreSQL modes
and MySQL empty or `false` modes.

## Region resolution

The region used to sign the token is resolved in order:

1. `spec.credentials.region` on the `ProviderConfig`
2. a `region` key in the connection secret
3. the AWS SDK's configured region (`AWS_REGION`, `AWS_DEFAULT_REGION`, or the
   selected shared AWS profile)

If none is set the token cannot be signed correctly, so configure one of them.
Region discovery from EC2 instance metadata is not enabled.

## Notes

- IAM tokens are valid for 15 minutes. The provider signs a fresh token immediately
  before each physical database connection, including reconnects within a long
  reconcile. An already authenticated session continues after its token expires;
  it does not need to be interrupted or reauthenticated. The AWS credentials used
  for signing must also be valid; the SDK refreshes renewable credential sources.
  The provider reuses its AWS configuration and credential cache across reconciles;
  restart it after changing environment variables or shared AWS profile settings.
- Setting `AWSIAMAuth` forces TLS on even if a weaker `tls`/`sslMode` was set.
