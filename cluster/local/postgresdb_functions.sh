#!/usr/bin/env bash
set -e

setup_postgresdb_no_tls() {
  echo_step "Installing PostgresDB into default namespace"
  postgres_root_pw=$(LC_ALL=C tr -cd "A-Za-z0-9" </dev/urandom | head -c 32)

  "${KUBECTL}" create secret generic postgresdb-creds \
      --from-literal username="postgres" \
      --from-literal password="${postgres_root_pw}" \
      --from-literal endpoint="postgresdb-postgresql.default.svc.cluster.local" \
      --from-literal port="5432"

  scriptdir=$(dirname "$0")
  "${KUBECTL}" apply -f "${scriptdir}/postgres.server.yaml"

  echo_step "Waiting for PostgreSQL to be ready"
  "${KUBECTL}" wait --for=condition=ready pod -l app=postgresdb-postgresql --timeout=120s

  "${KUBECTL}" port-forward --namespace default svc/postgresdb-postgresql 5432:5432 &
  PORT_FORWARD_PID=$!

  while ! PGPASSWORD="${postgres_root_pw}" psql -h localhost -p 5432 -U postgres -wtAc "SELECT 1;"; do
      echo "Waiting for PostgresDB to be ready..."
      sleep 2
  done
}

setup_provider_config_postgres_no_tls() {
  echo_step "creating ProviderConfig for PostgresDb with no TLS ${API_TYPE}"
  "${KUBECTL}" apply -f "${scriptdir}/postgres.providerconfig.${API_TYPE}.yaml"
}

setup_postgresdb_tests(){
  # install provider resources
  echo_step "creating PostgresDB Database resource"
  # create DB
  "${KUBECTL}" apply -f ${projectdir}/examples/${API_TYPE}/postgresql/database.yaml

  echo_step "creating PostgresDB Role resource"
  # create grant
  "${KUBECTL}" apply -f ${projectdir}/examples/${API_TYPE}/postgresql/role.yaml

  echo_step "creating PostgresDB Grant resource"
  # create grant
  "${KUBECTL}" apply -f ${projectdir}/examples/${API_TYPE}/postgresql/grant.yaml

  echo_step "creating PostgresDB Schema resources"
  # create grant
  "${KUBECTL}" apply -f ${projectdir}/examples/${API_TYPE}/postgresql/schema.yaml

  echo_step "check if Role is ready"
  "${KUBECTL}" wait --timeout 2m --for condition=Ready -f ${projectdir}/examples/${API_TYPE}/postgresql/role.yaml > /dev/null
  echo_step_completed

  echo_step "check if database is ready"
  "${KUBECTL}" wait --timeout 2m --for condition=Ready -f ${projectdir}/examples/${API_TYPE}/postgresql/database.yaml > /dev/null
  echo_step_completed

  echo_step "check if grant is ready"
  "${KUBECTL}" wait --timeout 2m --for condition=Ready -f ${projectdir}/examples/${API_TYPE}/postgresql/grant.yaml > /dev/null
  echo_step_completed

  echo_step "check if schema is ready"
  "${KUBECTL}" wait --timeout 2m --for condition=Ready -f ${projectdir}/examples/${API_TYPE}/postgresql/schema.yaml > /dev/null
  echo_step_completed
}

check_all_roles_privileges() {
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
}

check_role_privileges() {
    local role=$1
    local expected_privileges=$2
    local target_db=$4

    echo_info "Checking privileges for role: $role (expected: $expected_privileges)"
    echo ""
    result=$(PGPASSWORD="$3" psql -h localhost -p 5432 -U postgres -d postgres -wtAc" SELECT CASE WHEN has_database_privilege('$role', '$target_db', 'CONNECT') THEN 'CONNECT' ELSE NULL END, CASE WHEN has_database_privilege('$role', '$target_db', 'CREATE') THEN 'CREATE' ELSE NULL END, CASE WHEN has_database_privilege('$role', '$target_db', 'TEMP') THEN 'TEMP' ELSE NULL END " | tr '\n' ',' | sed 's/,$//')

    if [ "$result" = "$expected_privileges" ]; then
        echo_info "Privileges for $role are as expected: $result"
        echo ""
    else
        echo_error "ERROR: Privileges for $role do not match expected. Found: $result, Expected: $expected_privileges"
        echo ""
    fi
}

check_schema_privileges(){
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
}

setup_observe_only_database(){
  echo_step "create pre-existing database for observe only"

  local datname
  datname="$(PGPASSWORD="${postgres_root_pw}" psql -h localhost -p 5432 -U postgres -wtAc "CREATE DATABASE \"db-observe\";")"

  echo_step_completed
}

check_observe_only_database(){
  echo_step "check if observe only database is preserved after deletion"

  # Delete the database kubernetes object, it should not delete the database
  "${KUBECTL}" delete database.postgresql.sql.${APIGROUP_SUFFIX}crossplane.io db-observe

  local datname
  datname="$(PGPASSWORD="${postgres_root_pw}" psql -h localhost -p 5432 -U postgres -wtAc "SELECT datname FROM pg_database WHERE datname = 'db-observe';")"

  if [[ "$datname" == "db-observe" ]]; then
      echo "Database db-observe is still present"
      echo_info "OK"
  else
      echo "Database db-observe was NOT preserved"
      echo_error "Not OK"
  fi

  # Clean up
  PGPASSWORD="${postgres_root_pw}" psql -h localhost -p 5432 -U postgres -wtAc "DROP DATABASE \"db-observe\";"

  echo_step_completed
}

delete_postgresdb_resources(){
  # uninstall
  echo_step "uninstalling ${PROJECT_NAME}"
  "${KUBECTL}" delete -f "${projectdir}/examples/${API_TYPE}/postgresql/grant.yaml"
  "${KUBECTL}" delete --ignore-not-found=true -f "${projectdir}/examples/${API_TYPE}/postgresql/database.yaml"
  "${KUBECTL}" delete -f "${projectdir}/examples/${API_TYPE}/postgresql/role.yaml"
  "${KUBECTL}" delete -f "${projectdir}/examples/${API_TYPE}/postgresql/schema.yaml"
  echo "${PROVIDER_CONFIG_POSTGRES_YAML}" | "${KUBECTL}" delete -f -

  # ----------- cleaning postgres related resources

  echo_step "kill port-forwarding"
  kill $PORT_FORWARD_PID

  echo_step "uninstalling secret and provider config for postgres"
  "${KUBECTL}" delete secret postgresdb-creds

  echo_step "Uninstalling PostgresDB from default namespace"
  "${KUBECTL}" delete statefulset postgresdb-postgresql -n default
  "${KUBECTL}" delete service postgresdb-postgresql -n default
}

setup_extension_test() {
  # Test extensions (only if API_TYPE supports them)
  echo_step "Testing PostgreSQL extensions"

  # Apply extension resources
  echo_sub_step "Creating PostgreSQL extensions"
  "${KUBECTL}" apply -f "${projectdir}/examples/${API_TYPE}/postgresql/extension.yaml"

  # Wait for extensions to be ready
  echo_sub_step "Waiting for extensions to be ready"
  "${KUBECTL}" wait --timeout 2m --for condition=Ready -f "${projectdir}/examples/${API_TYPE}/postgresql/extension.yaml" > /dev/null
  echo_step_completed
}

check_extension_test() {
  echo_step "Verifying PostgreSQL extensions"

  # Check that extensions are ready
  echo_sub_step "Checking extension status"
  hstore_status=$("${KUBECTL}" get extension.postgresql.sql.${APIGROUP_SUFFIX}crossplane.io/hstore-extension-db -n default -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')
  ltree_status=$("${KUBECTL}" get extension.postgresql.sql.${APIGROUP_SUFFIX}crossplane.io/ltree-extension-db -n default -o jsonpath='{.status.conditions[?(@.type=="Ready")].status}')

  if [[ "$hstore_status" == "True" ]] && [[ "$ltree_status" == "True" ]]; then
    echo_info "Extensions are Ready as expected"
  else
    echo_error "ERROR: Extensions are not Ready. hstore: $hstore_status, ltree: $ltree_status"
  fi

  # Verify extensions are installed in the database
  echo_sub_step "Checking extensions in database"
  hstore_installed=$(PGPASSWORD="${postgres_root_pw}" psql -h localhost -p 5432 -U postgres -d example -wtAc "SELECT COUNT(*) FROM pg_extension WHERE extname = 'hstore';")
  ltree_installed=$(PGPASSWORD="${postgres_root_pw}" psql -h localhost -p 5432 -U postgres -d example -wtAc "SELECT COUNT(*) FROM pg_extension WHERE extname = 'ltree';")

  if [[ "$hstore_installed" == "1" ]] && [[ "$ltree_installed" == "1" ]]; then
    echo_info "Extensions are installed in database as expected"
  else
    echo_error "ERROR: Extensions not found in database. hstore: $hstore_installed, ltree: $ltree_installed"
  fi

  echo_step_completed
}

delete_extension_test() {
  echo_step "Cleaning up PostgreSQL extensions"
  "${KUBECTL}" delete --ignore-not-found=true -f "${projectdir}/examples/${API_TYPE}/postgresql/extension.yaml"
  echo_step_completed
}

integration_tests_postgres() {
  setup_postgresdb_no_tls
  setup_provider_config_postgres_no_tls
  setup_observe_only_database
  setup_postgresdb_tests
  check_observe_only_database
  check_all_roles_privileges
  check_schema_privileges
  setup_extension_test
  check_extension_test
  delete_extension_test
  delete_postgresdb_resources
}
