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

IAM authentication requires TLS. Encryption and server verification are separate:

| Engine | Server verification enabled | Encrypted without server verification |
|--------|-----------------------------|---------------------------------------|
| MySQL | `tls: "true"` with a trusted RDS CA | `tls: skip-verify` |
| PostgreSQL | `sslMode: verify-full` with a trusted RDS CA | `sslMode: require` without an explicit root certificate file |

For IAM, PostgreSQL preserves `require`, `verify-ca` and `verify-full`, and upgrades
`disable` to `require`. An omitted field defaults to `verify-full`, which requires
a trusted RDS CA. Kubernetes rejects an explicit empty mode.
`verify-ca` validates the certificate chain but does not check the hostname;
`verify-full` checks both. Use `verify-full` for IAM server identity verification:
with a broad public trust bundle, `verify-ca` can accept a certificate for a
different server. With the `SSL_CERT_FILE` mount below, `require`
remains encrypted but unverified: loading system roots does not enable
verification in that mode. lib/pq's compatibility behavior only enables CA-only
checking for `require` when a separate `sslrootcert` file is configured or
`~/.postgresql/root.crt` is discovered. `SSL_CERT_FILE` is not that setting.

For MySQL, IAM uses `true` when `tls` is omitted or set to `preferred`. This
verifies the server certificate and hostname using the system pool. An absent
RDS CA therefore causes the default IAM connection to fail. Kubernetes rejects
explicit empty or `false` values.
Explicit `skip-verify` remains an operator opt-in to encrypted but unverified
connections. Registered custom configurations are preserved.

MySQL also supports a per-ProviderConfig CA Secret with `tls: custom` and
`tlsConfig.caCert.secretRef`. No client certificate is needed for IAM. If using
mutual TLS, supply both `clientCert` and `clientKey`; a partial pair is rejected.
PostgreSQL has no equivalent CA Secret selector in ProviderConfig and uses the
pod trust bundle described below.

### Mount a trust bundle into the provider pod

The provider does not ship the RDS CA. Create a bundle containing your normal
public roots **and** the
[Amazon RDS CA certificates](https://truststore.pki.rds.amazonaws.com/global/global-bundle.pem).
Public roots are needed for HTTPS connections to AWS services such as STS.
The following commands run on your workstation, not inside the distroless
provider container. Set `PUBLIC_CA_BUNDLE` to your maintained PEM bundle of public
roots (the shown path is common on Debian/Ubuntu):

```sh
PUBLIC_CA_BUNDLE=/etc/ssl/certs/ca-certificates.crt
curl --fail --silent --show-error \
  https://truststore.pki.rds.amazonaws.com/global/global-bundle.pem \
  --output rds-global-bundle.pem
cat "$PUBLIC_CA_BUNDLE" rds-global-bundle.pem > provider-sql-ca-bundle.pem
kubectl -n crossplane-system create configmap provider-sql-ca \
  --from-file=ca-bundle.pem=provider-sql-ca-bundle.pem \
  --dry-run=client -o yaml | kubectl apply --server-side -f -
kubectl apply -f examples/aws-iam/deployment-runtime-config.yaml
```

Use the namespace where the provider pod runs if it differs from
`crossplane-system`. The certificates are public; a ConfigMap is sufficient.
Server-side apply avoids the client-side last-applied annotation size limit for
large combined bundles.
The runtime configuration mounts this ConfigMap read-only at
`/etc/provider-sql/ca/ca-bundle.pem` and sets `SSL_CERT_FILE` to that path.
[Go uses this variable when loading system roots](https://pkg.go.dev/crypto/x509#SystemCertPool).
No changes to the container image or PostgreSQL ProviderConfig API are needed.

Attach the runtime configuration to your **existing** package Provider (adjust
`provider-sql` to its installed name):

```sh
kubectl patch providers.pkg.crossplane.io provider-sql --type=merge \
  -p '{"spec":{"runtimeConfigRef":{"name":"provider-sql-aws-iam"}}}'
```

Each Provider has one runtimeConfigRef. If you already use a DeploymentRuntimeConfig
for workload identity, scheduling or other settings, merge this example's env,
volume and volumeMount into that configuration instead of replacing its reference.
Keep your existing IRSA or Pod Identity setup. The container name must be
`package-runtime` so Crossplane merges these settings into the provider container.
See [Crossplane runtime configuration](https://docs.crossplane.io/latest/packages/providers/#runtime-configuration).

Then select `sslMode: verify-full` for PostgreSQL or `tls: "true"` for MySQL, as
shown in the cluster and namespaced IAM examples. Use the actual RDS endpoint
hostname, which must match the server certificate. A missing or untrusted CA
must cause a verified connection to fail; the provider does not silently disable
verification.

Maintain the bundle as public roots and RDS CAs rotate. After updating its
ConfigMap, restart the provider Deployment so Go reloads the trust roots:

```sh
PROVIDER_DEPLOYMENT=your-provider-deployment-name
kubectl -n crossplane-system rollout restart deployment/"$PROVIDER_DEPLOYMENT"
kubectl -n crossplane-system rollout status deployment/"$PROVIDER_DEPLOYMENT"
```

[trust-manager](https://cert-manager.io/docs/trust/trust-manager/) can automate
bundle distribution: combine `useDefaultCAs: true` with an RDS CA source and
publish the result to a ConfigMap named `provider-sql-ca`, key `ca-bundle.pem`,
in the provider's namespace. It does **not** mount the bundle or update a running
process's trust store. The same runtime configuration and restart steps still
apply. Keep the RDS source current; trust-manager does not fetch the AWS URL for
you.

### MySQL: use a CA Secret for one ProviderConfig

This alternative keeps database CA trust separate from the pod's public roots.
It does not require `SSL_CERT_FILE` or a runtime mount. Create the CA Secret in
the namespace referenced by `tlsConfig`:

```sh
kubectl -n crossplane-system create secret generic rds-ca \
  --from-file=ca-bundle.pem=rds-global-bundle.pem \
  --dry-run=client -o yaml | kubectl apply --server-side -f -
```

For a namespaced ProviderConfig, create the CA Secret in that config's namespace
and use the same namespace in `tlsConfig.caCert.secretRef` (the complete namespaced
example uses `default`). Existing TLS Secret selectors accept cross-namespace
references; they do not enforce tenant isolation. Restrict ProviderConfig writes
accordingly, especially when using client certificate and key Secrets.

Then configure:

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

Complete examples are in
`examples/{cluster,namespaced}/mysql/config_iam_custom_ca.yaml`.
Keep `insecureSkipVerify` at its default `false`. Update the Secret when the
RDS CA changes; the next reconcile loads the updated bundle. Each ProviderConfig
has its own registered TLS configuration, including when namespaced configs
share a name.

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
