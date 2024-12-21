#!/usr/bin/env bash
set -e

SCRIPT_DIR="$(dirname "$(realpath "$0")")"
source "$SCRIPT_DIR/common_functions.sh"

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
"${UP}" alpha xpkg xp-extract --from-xpkg "${OUTPUT_DIR}"/xpkg/linux_"${SAFEHOSTARCH}"/"${PACKAGE_NAME}"-"${VERSION}".xpkg -o "${CACHE_PATH}/${PACKAGE_NAME}.gz" && chmod 644 "${CACHE_PATH}/${PACKAGE_NAME}.gz"

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
docker tag "${CONTROLLER_IMAGE}" "xpkg.upbound.io/${PACKAGE_NAME}"
"${KIND}" load docker-image "xpkg.upbound.io/${PACKAGE_NAME}" --name="${K8S_CLUSTER}"

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
"${HELM}" repo add crossplane-stable https://charts.crossplane.io/stable/ --force-update
chart_version="$("${HELM}" search repo crossplane-stable/crossplane | awk 'FNR == 2 {print $2}')"
echo_info "using crossplane version ${chart_version}"
echo
# we replace empty dir with our PVC so that the /cache dir in the kind node
# container is exposed to the crossplane pod
"${HELM}" install crossplane --namespace crossplane-system crossplane-stable/crossplane --version ${chart_version} --wait --set packageCache.pvc=package-cache

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
"${KUBECTL}" get "provider.pkg.crossplane.io/${PACKAGE_NAME}"
"${KUBECTL}" wait "provider.pkg.crossplane.io/${PACKAGE_NAME}" --for=condition=healthy --timeout=180s