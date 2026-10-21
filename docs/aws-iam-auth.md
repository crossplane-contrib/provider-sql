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

IAM authentication requires TLS. Encryption and server verification are separate:

| Engine | Server verification enabled | Encrypted without server verification |
|--------|-----------------------------|---------------------------------------|
| MySQL | `tls: "true"` with a trusted RDS CA | `tls: skip-verify` |
| PostgreSQL | `sslMode: verify-full` with a trusted RDS CA | `sslMode: require` (normally) |

For IAM, PostgreSQL preserves `require`, `verify-ca` and `verify-full`, and upgrades
`disable` or an empty mode to `require`. The Kubernetes default remains
`verify-full`, so an omitted field normally requires a trusted RDS CA.
`verify-ca` validates the certificate chain but does not check the hostname;
`verify-full` checks both. lib/pq can also perform CA-only verification in
`require` mode when a root certificate file is configured or discovered; select
`verify-full` explicitly for hostname verification.

For MySQL, IAM upgrades an omitted mode, `false`, or `preferred` to `skip-verify`.
It preserves `true` and registered custom configurations. The existing custom
TLS loader requires **all three** of `caCert`, `clientCert`, and `clientKey`;
it does not support a CA-only `tlsConfig`. IAM tokens do not provide client
certificates. PostgreSQL has no equivalent CA Secret selector in ProviderConfig.
Use the shared pod trust bundle below for both engines.

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
