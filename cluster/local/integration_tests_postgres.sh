#!/usr/bin/env bash
set -e

SCRIPT_DIR="$(dirname "$(realpath "$0")")"
source "$SCRIPT_DIR/common_functions.sh"

# install postgres and master secret
echo_step "Installing PostgresDB Helm chart into default namespace"
postgres_root_pw=$(LC_ALL=C tr -cd "A-Za-z0-9" </dev/urandom | head -c 32)

"${HELM}" repo update
"${HELM}" install postgresdb bitnami/postgresql \
    --version 11.9.1 \
    --set global.postgresql.auth.postgresPassword="${postgres_root_pw}" \
    --wait

"${KUBECTL}" create secret generic postgresdb-creds \
    --from-literal username="postgres" \
    --from-literal password="${postgres_root_pw}" \
    --from-literal endpoint="postgresdb-postgresql.default.svc.cluster.local" \
    --from-literal port="5432"

"${KUBECTL}" port-forward --namespace default svc/postgresdb-postgresql 5432:5432 &
PORT_FORWARD_PID=$!

# install provider config
echo_step "Installing ProviderConfig"
PROVIDER_CONFIG_POSTGRES_YAML="$( cat <<EOF
apiVersion: postgresql.sql.crossplane.io/v1alpha1
kind: ProviderConfig
metadata:
  name: default
spec:
  sslMode: disable
  credentials:
    source: PostgreSQLConnectionSecret
    connectionSecretRef:
      namespace: default
      name: postgresdb-creds
EOF
)"
echo "${PROVIDER_CONFIG_POSTGRES_YAML}" | "${KUBECTL}" apply -f -

# install provider resources
echo_step "creating PostgresDB Database resource"
# create DB
"${KUBECTL}" apply -f ${projectdir}/examples/postgresql/database.yaml

echo_step "creating PostgresDB Role resource"
# create grant
"${KUBECTL}" apply -f ${projectdir}/examples/postgresql/role.yaml

echo_step "creating PostgresDB Grant resource"
# create grant
"${KUBECTL}" apply -f ${projectdir}/examples/postgresql/grant.yaml

echo_step "creating PostgresDB Schema resources"
# create grant
"${KUBECTL}" apply -f ${projectdir}/examples/postgresql/schema.yaml

echo_step "check if Role is ready"
"${KUBECTL}" wait --timeout 2m --for condition=Ready -f ${projectdir}/examples/postgresql/role.yaml
echo_step_completed

echo_step "check if database is ready"
"${KUBECTL}" wait --timeout 2m --for condition=Ready -f ${projectdir}/examples/postgresql/database.yaml
echo_step_completed

echo_step "check if grant is ready"
"${KUBECTL}" wait --timeout 2m --for condition=Ready -f ${projectdir}/examples/postgresql/grant.yaml
echo_step_completed

echo_step "check if schema is ready"
"${KUBECTL}" wait --timeout 2m --for condition=Ready -f ${projectdir}/examples/postgresql/schema.yaml
echo_step_completed

# check if granting mechanism is working properly

echo_step "check if grant mechanism is working"

TARGET_DB='db1'
OWNER_ROLE='ownerrole'
USER_ROLE='example-role'

# Define roles and their expected privileges
roles="$OWNER_ROLE $USER_ROLE"
privileges="CONNECT|CREATE|TEMP ||"

# Iterate over roles and expected privileges
role_index=1
for role in $roles; do
    expected_privileges=$(echo "$privileges" | cut -d ' ' -f $role_index)
    check_role_privileges "$role" "$expected_privileges" "${postgres_root_pw}" "$TARGET_DB"
    role_index=$((role_index + 1))
done

echo_step_completed


# check if schema privileges are set properly
echo_step "check if schema privileges are set properly"

TARGET_DB='db1'

nspacl=$(PGPASSWORD="${postgres_root_pw}" psql -h localhost -p 5432 -U postgres -d "$TARGET_DB" -wtAc "SELECT nspacl FROM pg_namespace WHERE nspname = 'public';")
nspacl=$(echo "$nspacl" | xargs)

if [[ "$nspacl" == "{ownerrole=UC/ownerrole}" ]]; then
    echo "Privileges on schema public are as expected: $nspacl"
    echo_info "OK"
else
    echo "Privileges on schema public are NOT as expected: $nspacl"
    echo_error "Not OK"
fi

echo_step_completed

# uninstall
echo_step "uninstalling ${PROJECT_NAME}"
"${KUBECTL}" delete -f ${projectdir}/examples/postgresql/grant.yaml
"${KUBECTL}" delete -f ${projectdir}/examples/postgresql/database.yaml
"${KUBECTL}" delete -f ${projectdir}/examples/postgresql/role.yaml
"${KUBECTL}" delete -f ${projectdir}/examples/postgresql/schema.yaml
echo "${PROVIDER_CONFIG_POSTGRES_YAML}" | "${KUBECTL}" delete -f -

# ----------- cleaning postgres related resources

echo_step "kill port-forwarding"
kill $PORT_FORWARD_PID

echo_step "uninstalling secret and provider config for postgres"
"${KUBECTL}" delete secret postgresdb-creds

echo_step "Uninstalling PostgresDB Helm chart from default namespace"
"${HELM}" uninstall postgresdb

# ----------- success
echo_success "PostgresDB Integration tests succeeded!"