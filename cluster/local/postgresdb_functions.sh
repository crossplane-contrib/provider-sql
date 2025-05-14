#!/usr/bin/env bash
set -e

setup_postgresdb_no_tls() {
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

  "${KUBECTL}" port-forward --namespace default svc/postgresdb-postgresql 5432:5432 | grep -v "Handling connection for" &
  PORT_FORWARD_PID=$!
}

setup_provider_config_postgres_no_tls() {
  echo_step "creating ProviderConfig for PostgresDb with no TLS"
  local yaml="$( cat <<EOF
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
  echo "${yaml}" | "${KUBECTL}" apply -f -
}

create_grantable_objects() {
  TARGET_DB='db1'
  TARGE_SCHEMA='public'
  request="
  CREATE TABLE \"$TARGE_SCHEMA\".test_table(col1 INT NULL);
  CREATE SEQUENCE \"$TARGE_SCHEMA\".test_sequence START WITH 1000 INCREMENT BY 1;
  CREATE PROCEDURE \"$TARGE_SCHEMA\".test_procedure(arg TEXT) LANGUAGE plpgsql AS \$\$ BEGIN END; \$\$;
  CREATE TABLE \"$TARGE_SCHEMA\".test_table_column(test_column INT NULL);
  CREATE FOREIGN DATA WRAPPER test_foreign_data_wrapper;
  CREATE SERVER test_foreign_server FOREIGN DATA WRAPPER test_foreign_data_wrapper;
  "
  create_objects=$(PGPASSWORD="${postgres_root_pw}" psql -h localhost -p 5432 -U postgres -d "$TARGET_DB" -wtAc "$request")
  if [ $? -eq 0 ]; then
    echo_info "PostgresDB objects created in schema public"
  else
    echo_error "ERROR: could not create grantable objects: $create_objects"
  fi
}

delete_grantable_objects() {
  TARGET_DB='db1'
  TARGE_SCHEMA='public'
  request="
  DROP SERVER test_foreign_server;
  DROP FOREIGN DATA WRAPPER test_foreign_data_wrapper;
  DROP TABLE \"$TARGE_SCHEMA\".test_table_column;
  DROP PROCEDURE \"$TARGE_SCHEMA\".test_procedure(TEXT);
  DROP SEQUENCE \"$TARGE_SCHEMA\".test_sequence;
  DROP TABLE \"$TARGE_SCHEMA\".test_table;
  "
  drop_objects=$(PGPASSWORD="${postgres_root_pw}" psql -h localhost -p 5432 -U postgres -d "$TARGET_DB" -wtAc "$request")
  if [ $? -eq 0 ]; then
    echo_info "PostgresDB objects dropped from schema public"
  else
    echo_error "ERROR: could not delete grantable objects: $drop_objects"
  fi
}

setup_postgresdb_tests(){
# install provider resources
echo_step "creating PostgresDB Database resource"
# create DB
"${KUBECTL}" apply -f ${projectdir}/examples/postgresql/database.yaml

echo_step "creating PostgresDB Role resource"
# create grant
"${KUBECTL}" apply -f ${projectdir}/examples/postgresql/role.yaml

echo_step "creating PostgresDB Schema resources"
# create grant
"${KUBECTL}" apply -f ${projectdir}/examples/postgresql/schema.yaml

echo_step "check if Role is ready"
"${KUBECTL}" wait --timeout 2m --for condition=Ready -f ${projectdir}/examples/postgresql/role.yaml
echo_step_completed

echo_step "check if database is ready"
"${KUBECTL}" wait --timeout 2m --for condition=Ready -f ${projectdir}/examples/postgresql/database.yaml
echo_step_completed

echo_step "check if schema is ready"
"${KUBECTL}" wait --timeout 2m --for condition=Ready -f ${projectdir}/examples/postgresql/schema.yaml
echo_step_completed

echo_step "create grantable objects"
create_grantable_objects
echo_step_completed

echo_step "creating PostgresDB Grant resource"
# create grant
"${KUBECTL}" apply -f ${projectdir}/examples/postgresql/grant.yaml

echo_step "check if grant is ready"
"${KUBECTL}" wait --timeout 2m --for condition=Ready -f ${projectdir}/examples/postgresql/grant.yaml
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

    echo -n "Privileges for role: $role (expected: $expected_privileges)"

    result=$(PGPASSWORD="$3" psql -h localhost -p 5432 -U postgres -d postgres -wtAc" SELECT CASE WHEN has_database_privilege('$role', '$target_db', 'CONNECT') THEN 'CONNECT' ELSE NULL END, CASE WHEN has_database_privilege('$role', '$target_db', 'CREATE') THEN 'CREATE' ELSE NULL END, CASE WHEN has_database_privilege('$role', '$target_db', 'TEMP') THEN 'TEMP' ELSE NULL END " | tr '\n' ',' | sed 's/,$//')

    if [ "$result" = "$expected_privileges" ]; then
        echo " condition met"
    else
        echo ""
        echo_error "ERROR: Privileges for $role do not match expected. Found: $result, Expected: $expected_privileges"
        echo ""
    fi
}

check_all_schema_privileges() {
  # check if schema privileges are set properly
  echo_step "check if schema privileges are set properly"

  OWNER_ROLE='ownerrole'
  USER_ROLE='no-grants-role'

  # Define roles and their expected privileges
  roles="$OWNER_ROLE $USER_ROLE"
  dbs="db1 example"
  schemas="public my-schema"
  privileges="USAGE|f,CREATE|f USAGE|t,CREATE|t"

  # Iterate over roles and expected privileges
  role_index=1
  for role in $roles; do
    expected_privileges=$(echo "$privileges" | cut -d ' ' -f $role_index)
    target_db=$(echo "$dbs" | cut -d ' ' -f $role_index)
    target_schema=$(echo "$schemas" | cut -d ' ' -f $role_index)
    check_schema_privileges "$role" "$expected_privileges" "${postgres_root_pw}" "$target_db" "$target_schema"
    role_index=$((role_index + 1))
  done

  echo_step_completed
}

check_privileges(){
  local target_db=$1
  local object=$2
  local role=$3
  local expected=$4
  local request=$5
  echo -n "Privileges on $object for role: $role (expected: $expected)"

  response=$(PGPASSWORD="${postgres_root_pw}" psql -h localhost -p 5432 -U postgres -d "$target_db" -wtAc "$request")
  response=$(echo "$response" | xargs | tr ' ' ',')

  if [[ "$response" == "$expected" ]]; then
    echo " condition met"
  else
    echo ""
    echo_error "Found unexpected privileges: $response"
    echo ""
  fi
}

check_schema_privileges(){
  local role=$1
  local expected_privileges=$2
  local target_db=$4
  local target_schema=$5

  request="select acl.privilege_type, acl.is_grantable from pg_namespace n, aclexplode(n.nspacl) acl INNER JOIN pg_roles s ON acl.grantee = s.oid where n.nspname = '$target_schema' and s.rolname='$role'"

  check_privileges $target_db "schema $target_db.$target_schema" $role $expected_privileges "$request"
}

check_table_privileges(){
  target_db="db1"
  schema="public"
  table="test_table"
  role='no-grants-role'
  expected_privileges='INSERT|NO,SELECT|NO'

  request="select privilege_type, is_grantable from information_schema.role_table_grants where grantee = '$role' and table_schema = '$schema' and table_name='$table' order by privilege_type asc"

  check_privileges $target_db "table $schema.$table" $role $expected_privileges "$request"
}

check_sequence_privileges(){
  target_db="db1"
  schema="public"
  sequence="test_sequence"
  role='no-grants-role'
  expected_privileges='SELECT|f,UPDATE|f,USAGE|f'

  request="select acl.privilege_type, acl.is_grantable from pg_class c inner join pg_namespace n on c.relnamespace = n.oid, aclexplode(c.relacl) as acl inner join pg_roles s on acl.grantee = s.oid where c.relkind = 'S' and n.nspname = '$schema' and s.rolname='$role' and c.relname = '$sequence'"

  check_privileges $target_db "sequence $schema.$sequence" $role $expected_privileges "$request"
}

check_routine_privileges(){
  target_db="db1"
  schema="public"
  routine="test_procedure"
  role='no-grants-role'
  expected_privileges='EXECUTE|NO'

  request="select privilege_type, is_grantable from information_schema.role_routine_grants where grantee = '$role' and routine_schema = '$schema' and routine_name='$routine' order by privilege_type asc"

  check_privileges $target_db "routine $schema.$routine" $role $expected_privileges "$request"
}

check_column_privileges(){
  target_db="db1"
  schema="public"
  table="test_table_column"
  column="test_column"
  role='no-grants-role'
  expected_privileges='UPDATE|NO'

  request="select privilege_type, is_grantable from information_schema.role_column_grants where grantee = '$role' and table_schema = '$schema' and table_name='$table' and column_name='$column' order by privilege_type asc"

  check_privileges $target_db "column $column on table $schema.$table" $role $expected_privileges "$request"
}

check_foreign_data_wrapper_privileges(){
  target_db="db1"
  foreign_data_wrapper="test_foreign_data_wrapper"
  role='no-grants-role'
  expected_privileges='USAGE|NO'

  request="select privilege_type, is_grantable from information_schema.role_usage_grants where grantee = '$role' and object_type = 'FOREIGN DATA WRAPPER' and object_name='$foreign_data_wrapper' order by privilege_type asc"

  check_privileges $target_db "foreign data wrapper $foreign_data_wrapper" $role $expected_privileges "$request"
}

check_foreign_server_privileges(){
  target_db="db1"
  foreign_server="test_foreign_server"
  role='no-grants-role'
  expected_privileges='USAGE|NO'

  request="select privilege_type, is_grantable from information_schema.role_usage_grants where grantee = '$role' and object_type = 'FOREIGN SERVER' and object_name='$foreign_server' order by privilege_type asc"

  check_privileges $target_db "foreign server $foreign_server" $role $expected_privileges "$request"
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
  kubectl delete database.postgresql.sql.crossplane.io db-observe

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

check_custom_object_privileges(){
  echo_step "check if custom_object_privileges privileges are set properly"

  check_table_privileges
  check_sequence_privileges
  check_routine_privileges
  check_column_privileges
  check_foreign_data_wrapper_privileges
  check_foreign_server_privileges

  echo_step_completed
}

delete_postgresdb_resources(){
  echo_step "deleting grantable resources"
  delete_grantable_objects

  # uninstall
  echo_step "uninstalling ${PROJECT_NAME}"
  "${KUBECTL}" delete -f "${projectdir}/examples/postgresql/grant.yaml"
  "${KUBECTL}" delete --ignore-not-found=true -f "${projectdir}/examples/postgresql/database.yaml"
  "${KUBECTL}" delete -f "${projectdir}/examples/postgresql/role.yaml"
  "${KUBECTL}" delete -f "${projectdir}/examples/postgresql/schema.yaml"
  echo "${PROVIDER_CONFIG_POSTGRES_YAML}" | "${KUBECTL}" delete -f -

  # ----------- cleaning postgres related resources

  echo_step "kill port-forwarding"
  kill $PORT_FORWARD_PID

  echo_step "uninstalling secret and provider config for postgres"
  "${KUBECTL}" delete secret postgresdb-creds

  echo_step "Uninstalling PostgresDB Helm chart from default namespace"
  "${HELM}" uninstall postgresdb
}

integration_tests_postgres() {
  setup_postgresdb_no_tls
  setup_provider_config_postgres_no_tls
  setup_observe_only_database
  setup_postgresdb_tests
  check_observe_only_database
  check_all_roles_privileges
  check_all_schema_privileges
  check_custom_object_privileges
  delete_postgresdb_resources
}