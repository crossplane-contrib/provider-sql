# PostgreSQL Role passwords

`provider-sql` sets a password on every PostgreSQL `Role` it manages and publishes the current value to the connection secret named by `spec.writeConnectionSecretToRef`. This document describes where that password comes from and how to rotate it.

## Providing the password

There are two ways to provide the password, selected by whether `spec.forProvider.passwordSecretRef` is set.

### Bring your own password

Set `passwordSecretRef` to the secret key that holds the password. The provider applies that value when it creates the role and re-applies it whenever the referenced secret changes, so the password is always whatever you put in that secret.

For a namespaced `Role` (`postgresql.sql.m.crossplane.io`), `passwordSecretRef` takes only `name` and `key` and always resolves in the `Role`'s own namespace; drop the `namespace` field from the example below.

```yaml
apiVersion: postgresql.sql.crossplane.io/v1alpha1
kind: Role
metadata:
  name: example-role
spec:
  forProvider:
    privileges:
      login: true
    passwordSecretRef:
      name: example-role-password
      namespace: default
      key: password
  writeConnectionSecretToRef:
    name: example-role-secret
    namespace: default
```

### Auto-generated password

When `passwordSecretRef` is not set, the provider generates a password while creating the role and writes it to the connection secret. The generated password is a cryptographically random 27 characters drawn from `a-z`, `A-Z` and `0-9` (no symbols). Length and character set are not configurable.

Once the role has a generated password the provider leaves it alone, with two exceptions:

- **Recovering a lost password.** If `status.atProvider.lastPasswordChange` has never been recorded and the connection secret is missing or has no password - for example the database role was restored from a snapshot but its connection secret was not, or the secret was deleted - the provider generates a fresh password and applies it to the role, so the database and the connection secret agree again. This needs no configuration.
- **Rotation on demand.** See below.

```yaml
apiVersion: postgresql.sql.crossplane.io/v1alpha1
kind: Role
metadata:
  name: example-role
spec:
  forProvider:
    privileges:
      login: true
  writeConnectionSecretToRef:
    name: example-role-secret
    namespace: default
```

Runnable manifests covering both modes: [cluster-scoped](../examples/cluster/postgresql/role.yaml), [namespaced](../examples/namespaced/postgresql/role.yaml).

## Rotating the password with `passwordRotationTrigger`

### With a user-supplied password

`passwordRotationTrigger` has no effect. To rotate, change the value in the secret referenced by `passwordSecretRef`; the provider notices the change and re-applies it to the role.

### With an auto-generated password

Set `spec.forProvider.passwordRotationTrigger` to a timestamp later than `status.atProvider.lastPasswordChange`. On the next reconcile the provider generates a new password, applies it to the role in the database, writes it to the connection secret, and updates `lastPasswordChange` to the current time. To rotate again later, move the trigger to a newer timestamp; a trigger equal to or older than `lastPasswordChange` does nothing.

```yaml
apiVersion: postgresql.sql.crossplane.io/v1alpha1
kind: Role
metadata:
  name: example-role
spec:
  forProvider:
    privileges:
      login: true
    # Move past status.atProvider.lastPasswordChange to rotate.
    passwordRotationTrigger: "2026-01-01T00:00:00Z"
  writeConnectionSecretToRef:
    name: example-role-secret
    namespace: default
```

> **Caveat:** `passwordRotationTrigger` has no effect until `status.atProvider.lastPasswordChange` is set, which does not happen on creation. A role the provider created itself - connection secret populated, `lastPasswordChange` still unset - ignores the trigger. `lastPasswordChange` is first recorded when the provider changes the password, which for such a role means the recovery case above. To force that first change, clear the `password` key in the connection secret; the provider regenerates it and records `lastPasswordChange`, after which the trigger works normally.
