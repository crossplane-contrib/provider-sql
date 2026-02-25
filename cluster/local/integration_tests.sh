#!/usr/bin/env bash
set -e

# setting up colors
BLU='\033[0;34m'
YLW='\033[0;33m'
GRN='\033[0;32m'
RED='\033[0;31m'
NOC='\033[0m' # No Color
echo_info() {
    printf "\n${BLU}%s${NOC}" "$1"
}
echo_step() {
    printf "\n${BLU}>>>>>>> %s${NOC}\n" "$1"
}
echo_sub_step() {
    printf "\n${BLU}>>> %s${NOC}\n" "$1"
}

echo_step_completed() {
    printf "${GRN} [✔]${NOC}"
}

echo_success() {
    printf "\n${GRN}%s${NOC}\n" "$1"
}
echo_warn() {
    printf "\n${YLW}%s${NOC}" "$1"
}
echo_error() {
    printf "\n${RED}%s${NOC}" "$1"
    exit 1
}

# ------------------------------
projectdir="$( cd "$( dirname "${BASH_SOURCE[0]}")"/../.. && pwd )"
scriptdir="$(dirname "$0")"

# get the build environment variables from the special build.vars target in the main makefile
eval $(make --no-print-directory -C ${projectdir} build.vars)

# ------------------------------

SAFEHOSTARCH="${SAFEHOSTARCH:-amd64}"
CONTROLLER_IMAGE="${BUILD_REGISTRY}/${PROJECT_NAME}-${SAFEHOSTARCH}"

K8S_CLUSTER="${K8S_CLUSTER:-${BUILD_REGISTRY}-inttests}"

PACKAGE_NAME="provider-sql"
MARIADB_ROOT_PW=$(openssl rand -base64 32)
MARIADB_TEST_PW=$(openssl rand -base64 32)
MSSQL_SA_PW="$(openssl rand -base64 16)Aa1!"  # MSSQL requires complex password

# cleanup on exit
if [ "$skipcleanup" != true ]; then
  function cleanup {
    echo_step "Cleaning up..."
    export KUBECONFIG=
    cleanup_cluster
  }

  trap cleanup EXIT
fi

# Global variable to control API type
API_TYPE="cluster"

SCRIPT_DIR="$(dirname "$(realpath "$0")")"
# shellcheck source="$SCRIPT_DIR/postgresdb_functions.sh"
source "$SCRIPT_DIR/postgresdb_functions.sh"
if [ $? -ne 0 ]; then
  echo "postgresdb_functions.sh failed. Exiting."
  exit 1
fi

# shellcheck source="$SCRIPT_DIR/mssqldb_functions.sh"
source "$SCRIPT_DIR/mssqldb_functions.sh"
if [ $? -ne 0 ]; then
  echo "mssqldb_functions.sh failed. Exiting."
  exit 1
fi

integration_tests_end() {
  echo_step "--- CLEAN-UP ---"
  cleanup_provider
  echo_success " All integration tests succeeded!"
}

setup_cluster() {
  local node_image="kindest/node:${KIND_NODE_IMAGE_TAG}"
  echo_step "creating k8s cluster using kind ${KIND_VERSION} and node image ${node_image}"

  "${KIND}" create cluster --name="${K8S_CLUSTER}" --wait=5m --image="${node_image}"
}

cleanup_cluster() {
  "${KIND}" delete cluster --name="${K8S_CLUSTER}"
}

setup_crossplane() {
  local channel="${CROSSPLANE_HELM_CHANNEL:-stable}"
  echo_step "installing crossplane from ${channel} channel"

  "${HELM}" repo add crossplane-channel "https://charts.crossplane.io/${channel}/" --force-update

  local chart_version="${CROSSPLANE_HELM_CHART_VERSION:-}"
  if [ -z "${chart_version}" ]; then
    chart_version="$("${HELM}" search repo crossplane-channel/crossplane | awk 'FNR == 2 {print $2}')"
  fi
  echo_info "using crossplane version ${chart_version}"
  echo

  "${HELM}" install crossplane --namespace crossplane-system --create-namespace \
    crossplane-channel/crossplane \
    --version "${chart_version}" --wait
}

setup_provider() {
  echo_step "deploying provider via local.xpkg.deploy"
  make -C "${projectdir}" local.xpkg.deploy.provider.${PACKAGE_NAME} KIND_CLUSTER_NAME="${K8S_CLUSTER}"

  echo_step "waiting for provider to be installed"
  "${KUBECTL}" wait "provider.pkg.crossplane.io/${PACKAGE_NAME}" --for=condition=healthy --timeout=60s
}

cleanup_provider() {
  echo_step "uninstalling provider"

  "${KUBECTL}" delete provider.pkg.crossplane.io "${PACKAGE_NAME}"
  "${KUBECTL}" delete deploymentruntimeconfig.pkg.crossplane.io runtimeconfig-${PACKAGE_NAME}

  echo_step "waiting for provider pods to be deleted"
  timeout=60
  current=0
  step=3
  while [[ $(kubectl get providerrevision.pkg.crossplane.io -o name | wc -l | tr -d '[:space:]') != "0" ]]; do
    echo "waiting another $step seconds"
    current=$((current + step))
    if [[ $current -ge $timeout ]]; then
      echo_error "timeout of ${timeout}s has been reached"
    fi
    sleep $step;
  done
}

setup_tls_certs() {
  echo_step "generating CA key and certificate"
  openssl genrsa -out ca-key.pem 2048
  openssl req -new -x509 -key ca-key.pem -out ca-cert.pem -days 365 -subj "/CN=CA"

  echo_step "generating server key and certificate"
  openssl genrsa -out server-key.pem 2048
  openssl req -new -key server-key.pem -out server-req.pem -subj "/CN=mariadb.default.svc.cluster.local"
  openssl x509 -req -in server-req.pem -CA ca-cert.pem -CAkey ca-key.pem -CAcreateserial -out server-cert.pem -days 365

  echo_step "generating client key and certificate"
  openssl genrsa -out client-key.pem 2048
  openssl req -new -key client-key.pem -out client-req.pem -subj "/CN=client"
  openssl x509 -req -in client-req.pem -CA ca-cert.pem -CAkey ca-key.pem -CAcreateserial -out client-cert.pem -days 365

  echo_step "creating secret for the TLS certificates and keys"
  "${KUBECTL}" create secret generic mariadb-server-tls \
      --from-file=ca-cert.pem \
      --from-file=server-cert.pem \
      --from-file=server-key.pem

  echo_step "creating secret for the client TLS certificates and keys"
  "${KUBECTL}" create secret generic mariadb-client-tls \
      --from-file=ca-cert.pem \
      --from-file=client-cert.pem \
      --from-file=client-key.pem
}

cleanup_tls_certs() {
  echo_step "cleaning up TLS certificate files and secrets"
  for file in *.pem *.srl; do
      rm -f "$file"
  done
  "${KUBECTL}" delete secret mariadb-server-tls
  "${KUBECTL}" delete secret mariadb-client-tls
}

setup_provider_config_no_tls() {
  echo_step "creating ProviderConfig with no TLS ${API_TYPE}"
  "${KUBECTL}" apply -f "${scriptdir}/mariadb.providerconfig.notls.${API_TYPE}.yaml"
}

setup_provider_config_tls() {
  echo_step "creating ProviderConfig with TLS ${API_TYPE}"
  "${KUBECTL}" apply -f "${scriptdir}/mariadb.providerconfig.tls.${API_TYPE}.yaml"
}

cleanup_provider_config() {
  echo_step "cleaning up ProviderConfig"
  "${KUBECTL}" delete providerconfig.mysql.sql.${APIGROUP_SUFFIX}crossplane.io default
}

setup_mariadb_no_tls() {
  echo_step "installing MariaDB with no TLS"
  "${KUBECTL}" create secret generic mariadb-creds \
      --from-literal username="root" \
      --from-literal password="${MARIADB_ROOT_PW}" \
      --from-literal endpoint="mariadb.default.svc.cluster.local" \
      --from-literal port="3306"

  "${KUBECTL}" apply -f ${scriptdir}/mariadb.server.yaml

  echo_step "Waiting for MariaDB to be ready"
  "${KUBECTL}" rollout status statefulset/mariadb --timeout=120s
}

setup_mariadb_tls() {
  echo_step "installing MariaDB with TLS"
  "${KUBECTL}" create secret generic mariadb-creds \
      --from-literal username="test" \
      --from-literal password="${MARIADB_TEST_PW}" \
      --from-literal endpoint="mariadb.default.svc.cluster.local" \
      --from-literal port="3306" \
      --from-file=ca-cert.pem \
      --from-file=client-cert.pem \
      --from-file=client-key.pem

  # Create init script ConfigMap
  "${KUBECTL}" create configmap mariadb-init-script --from-literal=init.sql="
    CREATE USER 'test'@'%' IDENTIFIED BY '${MARIADB_TEST_PW}' REQUIRE X509;
    GRANT ALL PRIVILEGES ON *.* TO 'test'@'%' WITH GRANT OPTION;
    FLUSH PRIVILEGES;
  "

  # Deploy MariaDB using official mariadb image with TLS
  "${KUBECTL}" apply -f "${scriptdir}/mariadb.tls.server.yaml"

  echo_step "Waiting for MariaDB to be ready"
  "${KUBECTL}" rollout status statefulset/mariadb --timeout=120s
}

cleanup_mariadb() {
  echo_step "uninstalling MariaDB"
  "${KUBECTL}" delete statefulset mariadb -n default
  "${KUBECTL}" delete service mariadb -n default
  "${KUBECTL}" delete configmap mariadb-init-script -n default --ignore-not-found=true
  "${KUBECTL}" delete secret mariadb-creds
}

test_create_database() {
  echo_step "test creating MySQL Database resource"
  "${KUBECTL}" apply -f ${projectdir}/examples/${API_TYPE}/mysql/database.yaml

  echo_info "check if is ready"
  "${KUBECTL}" wait --timeout 2m --for condition=Ready -f ${projectdir}/examples/${API_TYPE}/mysql/database.yaml
  echo_step_completed
}

test_database_charset() {
  echo_step "test database has correct charset and collation"

  local charset collation
  charset=$("${KUBECTL}" exec mariadb-0 -- bash -c \
    'mariadb -uroot -p${MARIADB_ROOT_PASSWORD} -N -e "SELECT default_character_set_name FROM information_schema.schemata WHERE schema_name = '"'"'example-db'"'"'"')
  collation=$("${KUBECTL}" exec mariadb-0 -- bash -c \
    'mariadb -uroot -p${MARIADB_ROOT_PASSWORD} -N -e "SELECT default_collation_name FROM information_schema.schemata WHERE schema_name = '"'"'example-db'"'"'"')

  charset=$(echo "${charset}" | tr -d '[:space:]')
  collation=$(echo "${collation}" | tr -d '[:space:]')

  echo_info "charset=${charset}, collation=${collation}"

  if [ "${charset}" != "utf8mb4" ]; then
    echo_error "expected charset utf8mb4 but got ${charset}"
  fi
  if [ "${collation}" != "utf8mb4_bin" ]; then
    echo_error "expected collation utf8mb4_bin but got ${collation}"
  fi
  echo_step_completed
}

test_update_database_charset() {
  echo_step "test updating MySQL Database charset and collation"

  # Patch the database to use a different collation
  "${KUBECTL}" patch database.mysql.sql.${APIGROUP_SUFFIX}crossplane.io example-db --type merge \
    -p '{"spec":{"forProvider":{"defaultCollation":"utf8mb4_general_ci"}}}'

  # Wait for the controller to reconcile the change
  sleep 15

  echo_info "check if collation was updated in MariaDB"
  local collation
  collation=$("${KUBECTL}" exec mariadb-0 -- bash -c \
    'mariadb -uroot -p${MARIADB_ROOT_PASSWORD} -N -e "SELECT default_collation_name FROM information_schema.schemata WHERE schema_name = '"'"'example-db'"'"'"')
  collation=$(echo "${collation}" | tr -d '[:space:]')

  echo_info "collation=${collation}"

  if [ "${collation}" != "utf8mb4_general_ci" ]; then
    echo_error "expected collation utf8mb4_general_ci after update but got ${collation}"
  fi
  echo_step_completed

  # Restore original collation for subsequent tests
  "${KUBECTL}" patch database.mysql.sql.${APIGROUP_SUFFIX}crossplane.io example-db --type merge \
    -p '{"spec":{"forProvider":{"defaultCollation":"utf8mb4_bin"}}}'
  sleep 10
}

test_remove_database_charset() {
  echo_step "test removing charset/collation from spec leaves database unchanged"

  # Remove charset and collation from the spec (set forProvider to only have empty fields)
  "${KUBECTL}" patch database.mysql.sql.${APIGROUP_SUFFIX}crossplane.io example-db --type json \
    -p '[{"op":"remove","path":"/spec/forProvider/defaultCharacterSet"},{"op":"remove","path":"/spec/forProvider/defaultCollation"}]'

  # Wait for the controller to reconcile -- late init should re-populate the fields
  sleep 15

  echo_info "check database resource is still Ready"
  "${KUBECTL}" wait --timeout 30s --for condition=Ready database.mysql.sql.${APIGROUP_SUFFIX}crossplane.io/example-db
  echo_step_completed

  echo_info "check charset/collation unchanged in MariaDB"
  local charset collation
  charset=$("${KUBECTL}" exec mariadb-0 -- bash -c \
    'mariadb -uroot -p${MARIADB_ROOT_PASSWORD} -N -e "SELECT default_character_set_name FROM information_schema.schemata WHERE schema_name = '"'"'example-db'"'"'"')
  collation=$("${KUBECTL}" exec mariadb-0 -- bash -c \
    'mariadb -uroot -p${MARIADB_ROOT_PASSWORD} -N -e "SELECT default_collation_name FROM information_schema.schemata WHERE schema_name = '"'"'example-db'"'"'"')

  charset=$(echo "${charset}" | tr -d '[:space:]')
  collation=$(echo "${collation}" | tr -d '[:space:]')

  echo_info "charset=${charset}, collation=${collation}"

  if [ "${charset}" != "utf8mb4" ]; then
    echo_error "expected charset utf8mb4 after field removal but got ${charset}"
  fi
  if [ "${collation}" != "utf8mb4_bin" ]; then
    echo_error "expected collation utf8mb4_bin after field removal but got ${collation}"
  fi
  echo_step_completed
}

test_create_user() {
  echo_step "test creating MySQL User resource"
  local user_pw="asdf1234"
  "${KUBECTL}" create secret generic example-pw --from-literal password="${user_pw}" --save-config
  "${KUBECTL}" apply -f ${projectdir}/examples/${API_TYPE}/mysql/user.yaml

  echo_info "check if is ready"
  "${KUBECTL}" wait --timeout 2m --for condition=Ready -f ${projectdir}/examples/${API_TYPE}/mysql/user.yaml
  echo_step_completed

  echo_info "check if connection secret exists"
  local pw=$("${KUBECTL}" get secret example-connection-secret -ojsonpath='{.data.password}' | base64 --decode)
  [ "${pw}" == "${user_pw}" ]
  echo_step_completed
}

test_update_user_password() {
  echo_step "test updating MySQL User password"
  local user_pw="newpassword"
  "${KUBECTL}" create secret generic example-pw --from-literal password="${user_pw}" --dry-run=client --save-config -oyaml | \
    "${KUBECTL}" apply -f -

  # trigger reconcile
  "${KUBECTL}" annotate -f ${projectdir}/examples/${API_TYPE}/mysql/user.yaml reconcile=now

  sleep 3

  echo_info "check if connection secret has been updated"
  local pw=$("${KUBECTL}" get secret example-connection-secret -ojsonpath='{.data.password}' | base64 --decode)
  [ "${pw}" == "${user_pw}" ]
  echo_step_completed
}

test_create_grant() {
  echo_step "test creating MySQL Grant resource"
  "${KUBECTL}" apply -f ${projectdir}/examples/${API_TYPE}/mysql/grant_database.yaml

  echo_info "check if is ready"
  "${KUBECTL}" wait --timeout 2m --for condition=Ready -f ${projectdir}/examples/${API_TYPE}/mysql/grant_database.yaml
  echo_step_completed
}

test_all() {
  test_create_database
  test_database_charset
  test_update_database_charset
  test_remove_database_charset
  test_create_user
  test_update_user_password
  test_create_grant
}

cleanup_test_resources() {
  echo_step "cleaning up test resources"
  "${KUBECTL}" delete -f ${projectdir}/examples/${API_TYPE}/mysql/grant_database.yaml
  "${KUBECTL}" delete -f ${projectdir}/examples/${API_TYPE}/mysql/database.yaml
  "${KUBECTL}" delete -f ${projectdir}/examples/${API_TYPE}/mysql/user.yaml
  "${KUBECTL}" delete secret example-pw
}

setup_cluster
setup_crossplane
setup_provider

if [ "${QUICK_TEST:-}" == "true" ]; then
  echo_success "Quick test passed: provider is healthy and running."
  exit 0
fi

integration_tests_mariadb() {
  if [[ "${TLS}" == "true" ]]; then
    setup_tls_certs
    setup_mariadb_tls
    setup_provider_config_tls
  else
    setup_mariadb_no_tls
    setup_provider_config_no_tls
  fi

  test_all

  cleanup_test_resources
  cleanup_provider_config
  cleanup_mariadb

  if [[ "${TLS}" == "true" ]]; then
    cleanup_tls_certs
  fi
}

run_test() {
  APIGROUP_SUFFIX=""
  if [ "${API_TYPE}" == "namespaced" ]; then
    APIGROUP_SUFFIX="m."
  fi

  local testmain="$1"

  echo_step "--- TESTING $testmain $API_TYPE WITH TLS=$TLS ---"
  start=$(date +%s)

  $testmain

  duration=$(( $(date +%s) - start ))
  echo_step "--- TESTING $testmain DONE IN ${duration}s ---"
}

TLS=true API_TYPE="cluster" run_test integration_tests_mariadb
TLS=true API_TYPE="namespaced" run_test integration_tests_mariadb
TLS=false API_TYPE="cluster" run_test integration_tests_mariadb

TLS=false API_TYPE="cluster" run_test integration_tests_postgres
TLS=false API_TYPE="namespaced" run_test integration_tests_postgres

# no TLS=false variant - MSSQL uses built-in encryption
TLS=true API_TYPE="cluster" run_test integration_tests_mssql
TLS=true API_TYPE="namespaced" run_test integration_tests_mssql

integration_tests_end
