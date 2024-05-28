#!/usr/bin/env bash
set -e

# setting up colors
BLU='\033[0;34m'
YLW='\033[0;33m'
GRN='\033[0;32m'
RED='\033[0;31m'
NOC='\033[0m' # No Color
echo_info(){
    printf "\n${BLU}%s${NOC}" "$1"
}
echo_step(){
    printf "\n${BLU}>>>>>>> %s${NOC}\n" "$1"
}
echo_sub_step(){
    printf "\n${BLU}>>> %s${NOC}\n" "$1"
}

echo_step_completed(){
    printf "${GRN} [✔]${NOC}"
}

echo_success(){
    printf "\n${GRN}%s${NOC}\n" "$1"
}
echo_warn(){
    printf "\n${YLW}%s${NOC}" "$1"
}
echo_error(){
    printf "\n${RED}%s${NOC}" "$1"
    exit 1
}

# ------------------------------
projectdir="$( cd "$( dirname "${BASH_SOURCE[0]}")"/../.. && pwd )"

# get the build environment variables from the special build.vars target in the main makefile
eval $(make --no-print-directory -C ${projectdir} build.vars)
HOSTOS=linux

# ------------------------------

SAFEHOSTARCH="${SAFEHOSTARCH:-amd64}"
CONTROLLER_IMAGE="${BUILD_REGISTRY}/${PROJECT_NAME}-${SAFEHOSTARCH}"

version_tag="$(cat ${projectdir}/_output/version)"
# tag as latest version to load into kind cluster
K8S_CLUSTER="${K8S_CLUSTER:-${BUILD_REGISTRY}-inttests}"

CROSSPLANE_NAMESPACE="crossplane-system"
PACKAGE_NAME="provider-sql"

# cleanup on exit
if [ "$skipcleanup" != true ]; then
  function cleanup {
    echo_step "Cleaning up..."
    export KUBECONFIG=
    "${KIND}" delete cluster --name="${K8S_CLUSTER}"
  }

  trap cleanup EXIT
fi

# setup package cache
echo_step "setting up local package cache"
CACHE_PATH="${projectdir}/.work/inttest-package-cache"
mkdir -p "${CACHE_PATH}"
echo "created cache dir at ${CACHE_PATH}"
"${UP}" alpha xpkg xp-extract --from-xpkg "${OUTPUT_DIR}"/xpkg/"${HOSTOS}"_"${SAFEHOSTARCH}"/"${PACKAGE_NAME}"-"${VERSION}".xpkg -o "${CACHE_PATH}/${PACKAGE_NAME}.gz" && chmod 644 "${CACHE_PATH}/${PACKAGE_NAME}.gz"

# create kind cluster with extra mounts
KIND_NODE_IMAGE="kindest/node:${KIND_NODE_IMAGE_TAG}"
echo_step "creating k8s cluster using kind ${KIND_VERSION} and node image ${KIND_NODE_IMAGE}"
KIND_CONFIG="$( cat <<EOF
kind: Cluster
apiVersion: kind.x-k8s.io/v1alpha4
nodes:
- role: control-plane
  extraMounts:
  - hostPath: "${CACHE_PATH}/"
    containerPath: /cache
EOF
)"
echo "${KIND_CONFIG}" | "${KIND}" create cluster --name="${K8S_CLUSTER}" --wait=5m --image="${KIND_NODE_IMAGE}" --config=-

# tag controller image and load it into kind cluster
PACKAGE_TAG="xpkg.upbound.io/provider-sql:latest"
docker tag "${CONTROLLER_IMAGE}" "${PACKAGE_TAG}"
"${KIND}" load docker-image "${PACKAGE_TAG}" --name="${K8S_CLUSTER}"

echo_step "create crossplane-system namespace"
"${KUBECTL}" create ns crossplane-system

echo_step "create persistent volume and claim for mounting package-cache"
PV_YAML="$( cat <<EOF
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
echo "${PV_YAML}" | "${KUBECTL}" create -f -

PVC_YAML="$( cat <<EOF
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
echo "${PVC_YAML}" | "${KUBECTL}" create -f -

# install crossplane from stable channel
echo_step "installing crossplane from stable channel"
"${HELM3}" repo add crossplane-stable https://charts.crossplane.io/stable/ --force-update
chart_version="$("${HELM3}" search repo crossplane-stable/crossplane | awk 'FNR == 2 {print $2}')"
echo_info "using crossplane version ${chart_version}"
echo
# we replace empty dir with our PVC so that the /cache dir in the kind node
# container is exposed to the crossplane pod
"${HELM3}" install crossplane --namespace crossplane-system crossplane-stable/crossplane --version ${chart_version} --wait --set packageCache.pvc=package-cache

# ----------- integration tests
echo_step "--- INTEGRATION TESTS ---"

# install package
echo_step "installing ${PROJECT_NAME} into \"${CROSSPLANE_NAMESPACE}\" namespace"

INSTALL_YAML="$( cat <<EOF
apiVersion: pkg.crossplane.io/v1alpha1
kind: ControllerConfig
metadata:
  name: debug-config
spec:
  args:
  - --debug
---
apiVersion: pkg.crossplane.io/v1
kind: Provider
metadata:
  name: "${PACKAGE_NAME}"
spec:
  controllerConfigRef:
    name: debug-config
  package: "${PACKAGE_NAME}"
  packagePullPolicy: Never
EOF
)"

echo "${INSTALL_YAML}" | "${KUBECTL}" apply -f -

# printing the cache dir contents can be useful for troubleshooting failures
echo_step "check kind node cache dir contents"
docker exec "${K8S_CLUSTER}-control-plane" ls -la /cache

echo_step "waiting for provider to be installed"
"${KUBECTL}" wait "provider.pkg.crossplane.io/${PACKAGE_NAME}" --for=condition=healthy --timeout=600s

# --- Install MariaDB with TLS enabled ---

# Generate CA key and certificate
openssl genpkey -algorithm RSA -out ca-key.pem
openssl req -new -x509 -key ca-key.pem -out ca-cert.pem -days 365 -subj "/CN=CA"

# Generate server key and certificate
openssl genpkey -algorithm RSA -out server-key.pem
openssl req -new -key server-key.pem -out server-req.pem -subj "/CN=mariadb.default.svc.cluster.local"
openssl x509 -req -in server-req.pem -CA ca-cert.pem -CAkey ca-key.pem -CAcreateserial -out server-cert.pem -days 365

# Generate client key and certificate
openssl genpkey -algorithm RSA -out client-key.pem
openssl req -new -key client-key.pem -out client-req.pem -subj "/CN=client"
openssl x509 -req -in client-req.pem -CA ca-cert.pem -CAkey ca-key.pem -CAcreateserial -out client-cert.pem -days 365

# Create a secret for the server TLS certificates and keys
"${KUBECTL}" create secret generic mariadb-server-tls \
    --from-file=ca-cert.pem \
    --from-file=server-cert.pem \
    --from-file=server-key.pem

# Create a secret for the client TLS certificates and keys
"${KUBECTL}" create secret generic mariadb-client-tls \
    --from-file=ca-cert.pem \
    --from-file=client-cert.pem \
    --from-file=client-key.pem

# install MariaDB chart with TLS enabled
echo_step "installing MariaDB Helm chart with TLS into default namespace"
mariadb_root_pw=$(LC_ALL=C tr -cd "A-Za-z0-9" </dev/urandom | head -c 32)

"${HELM3}" repo add bitnami https://charts.bitnami.com/bitnami

"${HELM3}" install mariadb bitnami/mariadb \
    --version 11.3.0 \
    --set auth.rootPassword="${mariadb_root_pw}" \
    --set primary.configuration=mariadb.cnf \
    --set primary.tls.certificatesSecretName=mariadb-server-tls \
    --set primary.tls.autoGenerated=false \
    --wait

# Create a custom mariadb.cnf file with TLS configuration
cat <<EOF >mariadb.cnf
[mysqld]
ssl-ca=/etc/ssl/certs/ca-cert.pem
ssl-cert=/etc/ssl/certs/server-cert.pem
ssl-key=/etc/ssl/certs/server-key.pem
EOF

"${KUBECTL}" create configmap mariadb-config --from-file=mariadb.cnf

# create ProviderConfig with mTLS
"${KUBECTL}" create secret generic mariadb-creds \
    --from-literal username="root" \
    --from-literal password="${mariadb_root_pw}" \
    --from-literal endpoint="mariadb.default.svc.cluster.local" \
    --from-literal port="3306" \
    --from-file=ca-cert.pem \
    --from-file=client-cert.pem \
    --from-file=client-key.pem

PROVIDER_CONFIG_YAML="$( cat <<EOF
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
        name: mariadb-creds
        key: ca-cert.pem
    clientCert:
      secretRef:
        name: mariadb-creds
        key: client-cert.pem
    clientKey:
      secretRef:
        name: mariadb-creds
        key: client-key.pem
EOF
)"
echo "${PROVIDER_CONFIG_YAML}" | "${KUBECTL}" apply -f -

# ---

echo_step "creating MySQL Database resource"
# create DB
"${KUBECTL}" apply -f ${projectdir}/examples/mysql/database.yaml

echo_info "check if is ready"
"${KUBECTL}" wait --timeout 5m --for condition=Ready -f ${projectdir}/examples/mysql/database.yaml
echo_step_completed

echo_step "creating MySQL User resource"
# create user
user_pw="asdf1234"
"${KUBECTL}" create secret generic example-pw --from-literal password="${user_pw}"
"${KUBECTL}" apply -f ${projectdir}/examples/mysql/user.yaml

echo_info "check if is ready"
"${KUBECTL}" wait --timeout 2m --for condition=Ready -f ${projectdir}/examples/mysql/user.yaml
echo_step_completed

echo_info "check if connection secret exists"
pw=$("${KUBECTL}" get secret example-connection-secret -ojsonpath='{.data.password}' | base64 --decode)
[ "${pw}" == "${user_pw}" ]
echo_step_completed

echo_step "update MySQL User password"
user_pw="newpassword"
"${KUBECTL}" create secret generic example-pw --from-literal password="${user_pw}" --dry-run -oyaml | \
  "${KUBECTL}" apply -f -

# trigger reconcile
"${KUBECTL}" annotate -f ${projectdir}/examples/mysql/user.yaml reconcile=now

sleep 3

echo_info "check if connection secret has been updated"
pw=$("${KUBECTL}" get secret example-connection-secret -ojsonpath='{.data.password}' | base64 --decode)
[ "${pw}" == "${user_pw}" ]
echo_step_completed

echo_step "creating MySQL Grant resource"
# create grant
"${KUBECTL}" apply -f ${projectdir}/examples/mysql/grant_database.yaml

echo_info "check if is ready"
"${KUBECTL}" wait --timeout 2m --for condition=Ready -f ${projectdir}/examples/mysql/grant_database.yaml
echo_step_completed

# uninstall
echo_step "uninstalling ${PROJECT_NAME}"
"${KUBECTL}" delete -f ${projectdir}/examples/mysql/grant_database.yaml
"${KUBECTL}" delete -f ${projectdir}/examples/mysql/database.yaml
"${KUBECTL}" delete -f ${projectdir}/examples/mysql/user.yaml
echo "${PROVIDER_CONFIG_YAML}" | "${KUBECTL}" delete -f -
echo "${INSTALL_YAML}" | "${KUBECTL}" delete -f -

# check pods deleted
timeout=60
current=0
step=3
while [[ $(kubectl get providerrevision.pkg.crossplane.io -o name | wc -l) != "0" ]]; do
  echo "waiting for provider to be deleted for another $step seconds"
  current=$current+$step
  if ! [[ $timeout > $current ]]; then
    echo_error "timeout of ${timeout}s has been reached"
  fi
  sleep $step;
done

echo_success "Integration tests succeeded!"
