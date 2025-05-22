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

# get the build environment variables from the special build.vars target in the main makefile
eval $(make --no-print-directory -C ${projectdir} build.vars)

# ------------------------------

SAFEHOSTARCH="${SAFEHOSTARCH:-amd64}"
CONTROLLER_IMAGE="${BUILD_REGISTRY}/${PROJECT_NAME}-${SAFEHOSTARCH}"

version_tag="$(cat ${projectdir}/_output/version)"
# tag as latest version to load into kind cluster
K8S_CLUSTER="${K8S_CLUSTER:-${BUILD_REGISTRY}-inttests}"

PACKAGE_NAME="provider-sql"
MARIADB_ROOT_PW=$(openssl rand -base64 32)
MARIADB_TEST_PW=$(openssl rand -base64 32)

# cleanup on exit
if [ "$skipcleanup" != true ]; then
  function cleanup {
    echo_step "Cleaning up..."
    export KUBECONFIG=
    cleanup_cluster
  }

  trap cleanup EXIT
fi

SCRIPT_DIR="$(dirname "$(realpath "$0")")"
# shellcheck source="$SCRIPT_DIR/postgresdb_functions.sh"
source "$SCRIPT_DIR/postgresdb_functions.sh"
if [ $? -ne 0 ]; then
  echo "postgresdb_functions.sh failed. Exiting."
  exit 1
fi

integration_tests_end() {
  echo_step "--- CLEAN-UP ---"
  cleanup_provider
  echo_success " All integration tests succeeded!"
}

setup_cluster() {
  echo_step "setting up local package cache"

  local cache_path="${projectdir}/.work/inttest-package-cache"
  mkdir -p "${cache_path}"
  echo "created cache dir at ${cache_path}"
  "${UP}" alpha xpkg xp-extract --from-xpkg "${OUTPUT_DIR}"/xpkg/linux_"${SAFEHOSTARCH}"/"${PACKAGE_NAME}"-"${VERSION}".xpkg -o "${cache_path}/${PACKAGE_NAME}.gz"
  chmod 644 "${cache_path}/${PACKAGE_NAME}.gz"

  local node_image="kindest/node:${KIND_NODE_IMAGE_TAG}"
  echo_step "creating k8s cluster using kind ${KIND_VERSION} and node image ${node_image}"

  local config="$( cat <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  extraMounts:
  - hostPath: "${cache_path}/"
    containerPath: /cache
EOF
  )"
  echo "${config}" | "${KIND}" create cluster --name="${K8S_CLUSTER}" --wait=5m --image="${node_image}" --config=-

  echo_step "tag controller image and load it into kind cluster"

  docker tag "${CONTROLLER_IMAGE}" "xpkg.crossplane.io/${PACKAGE_NAME}"
  "${KIND}" load docker-image "xpkg.crossplane.io/${PACKAGE_NAME}" --name="${K8S_CLUSTER}"

  echo_step "create crossplane-system namespace"

  "${KUBECTL}" create ns crossplane-system

  echo_step "create persistent volume for mounting package-cache"

  local pv_yaml="$( cat <<EOF
apiVersion: v1
kind: PersistentVolume
metadata:
  name: package-cache
  labels:
    type: local
spec:
  storageClassName: manual
  capacity:
    storage: 5Mi
  accessModes:
    - ReadWriteOnce
  hostPath:
    path: "/cache"
EOF
  )"

  echo "${pv_yaml}" | "${KUBECTL}" create -f -

  echo_step "create persistent volume claim for mounting package-cache"

  local pvc_yaml="$( cat <<EOF
apiVersion: v1
kind: PersistentVolumeClaim
metadata:
  name: package-cache
  namespace: crossplane-system
spec:
  accessModes:
    - ReadWriteOnce
  volumeName: package-cache
  storageClassName: manual
  resources:
    requests:
      storage: 1Mi
EOF
  )"

  echo "${pvc_yaml}" | "${KUBECTL}" create -f -
}

cleanup_cluster() {
  "${KIND}" delete cluster --name="${K8S_CLUSTER}"
}

setup_crossplane() {
  echo_step "installing crossplane from stable channel"

  "${HELM}" repo add crossplane-stable https://charts.crossplane.io/stable/ --force-update
  local chart_version="$("${HELM}" search repo crossplane-stable/crossplane | awk 'FNR == 2 {print $2}')"
  echo_info "using crossplane version ${chart_version}"
  echo
  # we replace empty dir with our PVC so that the /cache dir in the kind node
  # container is exposed to the crossplane pod
  "${HELM}" install crossplane --namespace crossplane-system crossplane-stable/crossplane --version ${chart_version} --wait --set packageCache.pvc=package-cache
}

setup_provider() {
  echo_step "installing provider"

  local yaml="$( cat <<EOF
apiVersion: pkg.crossplane.io/v1beta1
kind: DeploymentRuntimeConfig
metadata:
  name: debug-config
spec:
  deploymentTemplate:
    spec:
      selector: {}
      template:
        spec:
          containers:
            - name: package-runtime
              args:
                - --debug
---
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: "${PACKAGE_NAME}"
spec:
  runtimeConfigRef:
    name: debug-config
  package: "${PACKAGE_NAME}"
  packagePullPolicy: Never
EOF
  )"

  echo "${yaml}" | "${KUBECTL}" apply -f -

  # printing the cache dir contents can be useful for troubleshooting failures
  echo_step "check kind node cache dir contents"
  docker exec "${K8S_CLUSTER}-control-plane" ls -la /cache

  echo_step "waiting for provider to be installed"
  "${KUBECTL}" wait "provider.pkg.crossplane.io/${PACKAGE_NAME}" --for=condition=healthy --timeout=60s
}

cleanup_provider() {
  echo_step "uninstalling provider"

  "${KUBECTL}" delete provider.pkg.crossplane.io "${PACKAGE_NAME}"
  "${KUBECTL}" delete deploymentruntimeconfig.pkg.crossplane.io debug-config

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
  echo_step "creating ProviderConfig with no TLS"
  local yaml="$( cat <<EOF
apiVersion: mysql.sql.crossplane.io/v1alpha1
kind: ProviderConfig
metadata:
  name: default
spec:
  credentials:
    source: MySQLConnectionSecret
    connectionSecretRef:
      namespace: default
      name: mariadb-creds
EOF
  )"

  echo "${yaml}" | "${KUBECTL}" apply -f -
}

setup_provider_config_tls() {
  echo_step "creating ProviderConfig with TLS"
  local yaml="$( cat <<EOF
apiVersion: mysql.sql.crossplane.io/v1alpha1
kind: ProviderConfig
metadata:
  name: default
spec:
  credentials:
    source: MySQLConnectionSecret
    connectionSecretRef:
      namespace: default
      name: mariadb-creds
  tls: custom
  tlsConfig:
    caCert:
      secretRef:
        namespace: default
        name: mariadb-creds
        key: ca-cert.pem
    clientCert:
      secretRef:
        namespace: default
        name: mariadb-creds
        key: client-cert.pem
    clientKey:
      secretRef:
        namespace: default
        name: mariadb-creds
        key: client-key.pem
    insecureSkipVerify: true
EOF
  )"

  echo "${yaml}" | "${KUBECTL}" apply -f -
}

cleanup_provider_config() {
  echo_step "cleaning up ProviderConfig"
  "${KUBECTL}" delete providerconfig.mysql.sql.crossplane.io default
}

setup_mariadb_no_tls() {
  echo_step "installing MariaDB with no TLS"
  "${KUBECTL}" create secret generic mariadb-creds \
      --from-literal username="root" \
      --from-literal password="${MARIADB_ROOT_PW}" \
      --from-literal endpoint="mariadb.default.svc.cluster.local" \
      --from-literal port="3306"

  "${HELM}" repo add bitnami https://charts.bitnami.com/bitnami >/dev/null
  "${HELM}" repo update
  "${HELM}" install mariadb bitnami/mariadb \
      --version 11.3.0 \
      --set auth.rootPassword="${MARIADB_ROOT_PW}" \
      --wait
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

  local values=$(cat <<EOF
auth:
  rootPassword: ${MARIADB_ROOT_PW}
primary:
  extraFlags: "--ssl --require-secure-transport=ON --ssl-ca=/opt/bitnami/mariadb/certs/ca-cert.pem --ssl-cert=/opt/bitnami/mariadb/certs/server-cert.pem --ssl-key=/opt/bitnami/mariadb/certs/server-key.pem"
  configurationSecret: mariadb-server-tls
  extraVolumes:
    - name: tls-certificates
      secret:
        secretName: mariadb-server-tls
  extraVolumeMounts:
    - name: tls-certificates
      mountPath: /opt/bitnami/mariadb/certs
      readOnly: true
initdbScripts:
  init.sql: |
    CREATE USER 'test'@'%' IDENTIFIED BY '${MARIADB_TEST_PW}' REQUIRE X509;
    GRANT ALL PRIVILEGES ON *.* TO 'test'@'%' WITH GRANT OPTION;
    FLUSH PRIVILEGES;
EOF
  )

  "${HELM}" repo add bitnami https://charts.bitnami.com/bitnami >/dev/null
  "${HELM}" repo update
  "${HELM}" install mariadb bitnami/mariadb \
      --version 11.3.0 \
      --values <(echo "$values") \
      --wait
}

cleanup_mariadb() {
  echo_step "uninstalling MariaDB"
  "${HELM}" uninstall mariadb
  "${KUBECTL}" delete secret mariadb-creds
}

test_create_database() {
  echo_step "test creating MySQL Database resource"
  "${KUBECTL}" apply -f ${projectdir}/examples/mysql/database.yaml

  echo_info "check if is ready"
  "${KUBECTL}" wait --timeout 2m --for condition=Ready -f ${projectdir}/examples/mysql/database.yaml
  echo_step_completed
}

test_create_user() {
  echo_step "test creating MySQL User resource"
  local user_pw="asdf1234"
  "${KUBECTL}" create secret generic example-pw --from-literal password="${user_pw}"
  "${KUBECTL}" apply -f ${projectdir}/examples/mysql/user.yaml

  echo_info "check if is ready"
  "${KUBECTL}" wait --timeout 2m --for condition=Ready -f ${projectdir}/examples/mysql/user.yaml
  echo_step_completed

  echo_info "check if connection secret exists"
  local pw=$("${KUBECTL}" get secret example-connection-secret -ojsonpath='{.data.password}' | base64 --decode)
  [ "${pw}" == "${user_pw}" ]
  echo_step_completed
}

test_update_user_password() {
  echo_step "test updating MySQL User password"
  local user_pw="newpassword"
  "${KUBECTL}" create secret generic example-pw --from-literal password="${user_pw}" --dry-run -oyaml | \
    "${KUBECTL}" apply -f -

  # trigger reconcile
  "${KUBECTL}" annotate -f ${projectdir}/examples/mysql/user.yaml reconcile=now

  sleep 3

  echo_info "check if connection secret has been updated"
  local pw=$("${KUBECTL}" get secret example-connection-secret -ojsonpath='{.data.password}' | base64 --decode)
  [ "${pw}" == "${user_pw}" ]
  echo_step_completed
}

test_create_grant() {
  echo_step "test creating MySQL Grant resource"
  "${KUBECTL}" apply -f ${projectdir}/examples/mysql/grant_database.yaml

  echo_info "check if is ready"
  "${KUBECTL}" wait --timeout 2m --for condition=Ready -f ${projectdir}/examples/mysql/grant_database.yaml
  echo_step_completed
}

test_all() {
  test_create_database
  test_create_user
  test_update_user_password
  test_create_grant
}

cleanup_test_resources() {
  echo_step "cleaning up test resources"
  "${KUBECTL}" delete -f ${projectdir}/examples/mysql/grant_database.yaml
  "${KUBECTL}" delete -f ${projectdir}/examples/mysql/database.yaml
  "${KUBECTL}" delete -f ${projectdir}/examples/mysql/user.yaml
  "${KUBECTL}" delete secret example-pw
}

setup_cluster
setup_crossplane
setup_provider

echo_step "--- INTEGRATION TESTS - NO TLS ---"

setup_mariadb_no_tls
setup_provider_config_no_tls

test_all

cleanup_test_resources
cleanup_provider_config
cleanup_mariadb

echo_step "--- INTEGRATION TESTS - TLS ---"

setup_tls_certs
setup_mariadb_tls
setup_provider_config_tls

test_all

cleanup_test_resources
cleanup_provider_config
cleanup_mariadb
cleanup_tls_certs

echo_step "--- INTEGRATION TESTS FOR MySQL ACCOMPLISHED SUCCESSFULLY ---"

echo_step "--- TESTING POSTGRESDB ---"
integration_tests_postgres
echo_step "--- INTEGRATION TESTS FOR POSTGRESDB ACCOMPLISHED SUCCESSFULLY ---"

integration_tests_end