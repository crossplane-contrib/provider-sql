#!/usr/bin/env bash
set -e

setup_mssql() {
  echo_step "installing MSSQL Server"

  "${KUBECTL}" create secret generic mssql-creds \
      --from-literal username="sa" \
      --from-literal password="${MSSQL_SA_PW}" \
      --from-literal endpoint="mssql.default.svc.cluster.local" \
      --from-literal port="1433"

  echo_step "Verifying secret creation"
  "${KUBECTL}" get secret mssql-creds -o yaml

  "${KUBECTL}" apply -f ${scriptdir}/mssql.server.yaml

  echo_step "Waiting for MSSQL Server to be ready"
  "${KUBECTL}" wait --for=create pod mssql-0
  "${KUBECTL}" wait --for=condition=ready pod -l app=mssql --timeout=300s

  # Wait a bit more for MSSQL to be fully ready for connections
  sleep 30
}

cleanup_mssql() {
  echo_step "cleaning up MSSQL server"
  "${KUBECTL}" delete -f ${scriptdir}/mssql.server.yaml --ignore-not-found=true
  "${KUBECTL}" delete secret mssql-creds --ignore-not-found=true
}

setup_mssql_provider_config() {
  echo_step "setting up MSSQL provider config"
  "${KUBECTL}" apply -f "${scriptdir}/mssql.providerconfig.${API_TYPE}.yaml"
}

cleanup_mssql_provider_config() {
  echo_step "cleaning up MSSQL provider config"
  "${KUBECTL}" delete providerconfig.mssql.sql.${APIGROUP_SUFFIX}crossplane.io default --ignore-not-found=true
}

test_create_mssql_database() {
  echo_step "test creating MSSQL Database resource"
  "${KUBECTL}" apply -f ${projectdir}/examples/${API_TYPE}/mssql/database.yaml

  echo_step "Waiting for MSSQL Database to be ready"
  "${KUBECTL}" wait --timeout 2m --for condition=Ready -f ${projectdir}/examples/${API_TYPE}/mssql/database.yaml

  echo_step_completed
}

test_create_mssql_user() {
  echo_step "test creating MSSQL User resource (traditional)"
  # Create password secret first
  "${KUBECTL}" create secret generic example-pw --from-literal password="Test123!" --dry-run=client -o yaml | "${KUBECTL}" apply -f -

  "${KUBECTL}" apply -f ${projectdir}/examples/${API_TYPE}/mssql/user.yaml

  echo_step "Waiting for MSSQL User to be ready"
  "${KUBECTL}" wait --timeout 2m --for condition=Ready -f ${projectdir}/examples/${API_TYPE}/mssql/user.yaml

  echo_step_completed
}

test_update_mssql_user_password() {
  echo_step "test updating MSSQL User password"

  # Update password secret
  "${KUBECTL}" patch secret example-pw -p '{"data":{"password":"'$(echo -n "NewTest123!" | base64)'"}}'

  # Force reconcile by adding annotation
  "${KUBECTL}" annotate -f ${projectdir}/examples/${API_TYPE}/mssql/user.yaml reconcile=now

  # Wait a bit for password update
  sleep 10

  echo_step_completed
}

test_create_mssql_grant() {
  echo_step "test creating MSSQL Grant resource"
  "${KUBECTL}" apply -f ${projectdir}/examples/${API_TYPE}/mssql/grant.yaml

  echo_step "Waiting for MSSQL Grant to be ready"
  "${KUBECTL}" wait --timeout 2m --for condition=Ready -f ${projectdir}/examples/${API_TYPE}/mssql/grant.yaml

  echo_step_completed
}

test_mssql_all() {
  test_create_mssql_database
  test_create_mssql_user
  test_update_mssql_user_password
  test_create_mssql_grant
}

cleanup_mssql_test_resources() {
  echo_step "cleaning up MSSQL test resources"
  "${KUBECTL}" delete -f ${projectdir}/examples/${API_TYPE}/mssql/grant.yaml --ignore-not-found=true
  "${KUBECTL}" wait --for=delete grant.mssql.sql.${APIGROUP_SUFFIX}crossplane.io/example-grant --timeout=60s

  "${KUBECTL}" delete -f ${projectdir}/examples/${API_TYPE}/mssql/user.yaml --ignore-not-found=true
  "${KUBECTL}" wait --for=delete user.mssql.sql.${APIGROUP_SUFFIX}crossplane.io/example-user --timeout=60s

  "${KUBECTL}" delete -f ${projectdir}/examples/${API_TYPE}/mssql/database.yaml --ignore-not-found=true
  "${KUBECTL}" wait --for=delete database.mssql.sql.${APIGROUP_SUFFIX}crossplane.io/example-db --timeout=60s

  echo_step "deleting example password secret"
  "${KUBECTL}" delete secret example-pw --ignore-not-found=true
}

integration_tests_mssql() {
  setup_mssql
  setup_mssql_provider_config

  test_mssql_all

  cleanup_mssql_test_resources
  cleanup_mssql_provider_config
  cleanup_mssql
}
