# AWS IAM database authentication

provider-sql can authenticate to Amazon Aurora PostgreSQL and Aurora MySQL with [IAM database authentication](https://docs.aws.amazon.com/AmazonRDS/latest/UserGuide/UsingWithRDS.IAMDBAuth.html). On each connect it generates a short-lived token from the AWS SDK default credential chain and uses that token as the database password. On EKS, [Pod Identity](https://docs.aws.amazon.com/eks/latest/userguide/pod-identities.html) supplies those credentials. The token is not cached, so a provider process that runs longer than the 15-minute token lifetime keeps reconciling. A single reconcile that itself lasts longer than 15 minutes and then opens another connection can fail. Normal SQL reconciles are much shorter.

```yaml
spec:
  sslMode: verify-full
  tlsConfig:
    caCert:
      secretRef:
        namespace: crossplane-system
        name: amazon-rds-ca
        key: global-bundle.pem
  credentials:
    source: AWSIAMAuth
    region: us-east-1
    connectionSecretRef:
      name: aurora-conn
```

`connectionSecretRef` still points at a Secret. Pod Identity does not know which cluster or database user to connect as. The Secret holds `endpoint`, `port`, and `username`. It does not need a `password`. `username` is the database role the token is signed for.

Pod Identity does not set `AWS_REGION`. Set `spec.credentials.region`, a `region` key in the connection secret, or `AWS_REGION` on the provider Deployment. The region is resolved in that order. An empty region is an error, because the SDK will sign a token with a blank region.

## Pod Identity

Attach an `rds-db:connect` policy to the Pod Identity role for the database user in the connection secret. Associate that role with the provider service account. `rds-db:connect` is an AWS IAM permission. The provider does not create it.

Enable IAM database authentication on the Aurora cluster. Aurora PostgreSQL 16 uses the same token as other Aurora PostgreSQL versions. The endpoint in the secret must be the name on the server certificate, because `sslMode: verify-full` checks the hostname. Use the cluster endpoint.

## Database user

This feature does not create the database login. A `Role` already creates a login, and a `Grant` with `memberOf: rds_iam` runs `GRANT rds_iam TO <role>`. On Aurora that grant disables password login for the role. Do not grant `rds_iam` to the master user.

The first grant has to run through a password `ProviderConfig` connected as the master user. After an IAM login exists that can create roles and grant `rds_iam`, later users can be created through the IAM `ProviderConfig`.

## TLS

`sslMode` is passed through. `disable`, `require`, `verify-ca`, and `verify-full` are all accepted. The CRD default is `verify-full`. Aurora rejects an unencrypted IAM login, so `disable` fails at the server.

`verify-full` checks the certificate chain and the hostname. `verify-ca` checks the chain only. `require` encrypts without using `tlsConfig`.

`tlsConfig.caCert` is the CA, read from a Secret the same way MySQL `tls: custom` reads a certificate. There is no client certificate. The secret key is one PEM, the [Amazon RDS global bundle](https://truststore.pki.rds.amazonaws.com/global/global-bundle.pem). That file already concatenates the RDS root CAs and the regional intermediate CAs. Every `CERTIFICATE` block is loaded into one trust pool. The provider image does not include this bundle.

On a namespaced `ProviderConfig`, omit `secretRef.namespace` to use the ProviderConfig namespace. A `ClusterProviderConfig` and a cluster-scoped `ProviderConfig` set the namespace.

Examples:

- `examples/cluster/postgresql/config_iam.yaml`
- `examples/namespaced/postgresql/config_iam.yaml`
- `examples/namespaced/postgresql/clusterconfig_iam.yaml`

## MySQL

The same token is used for Aurora MySQL. `tls` is left as set. `allowCleartextPasswords=true` is added only for `AWSIAMAuth`, because the AWS plugin sends the token with the MySQL cleartext client plugin. TLS still carries it. A password login does not set that flag.

```yaml
spec:
  tls: custom
  tlsConfig:
    caCert:
      secretRef:
        namespace: crossplane-system
        name: amazon-rds-ca
        key: global-bundle.pem
  credentials:
    source: AWSIAMAuth
    region: us-east-1
    connectionSecretRef:
      name: aurora-conn
```

`tls: "false"` is plaintext (`--ssl-mode=DISABLED`). Quote it. An unquoted `false` is a boolean and the string field rejects it. Omitting `tls` still means `preferred`. Aurora rejects a plaintext IAM login.

`tls: custom` verifies the server with `tlsConfig.caCert`. The secret key is the same RDS global bundle. `clientCert` and `clientKey` are optional and must be set together; IAM database auth has no client certificate. The driver sets the hostname check from the endpoint. `tls: true` still uses the pod trust store, which does not contain the RDS CA.

A `User` can set `authenticationPlugin.name: AWSAuthenticationPlugin` and `authString: RDS`. The master login that creates that user still uses a password `ProviderConfig`.

Examples:

- `examples/cluster/mysql/config_iam.yaml`
- `examples/namespaced/mysql/config_iam.yaml`
- `examples/namespaced/mysql/clusterconfig_iam.yaml`
- `examples/cluster/mysql/user_iam_auth.yaml`
- `examples/namespaced/mysql/user_iam_auth.yaml`
