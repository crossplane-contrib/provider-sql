#!/usr/bin/env bash
set -e

SCRIPT_DIR="$(dirname "$(realpath "$0")")"
source "$SCRIPT_DIR/common_functions.sh"

# install MariaDB chart
echo_step "installing MariaDB Helm chart into default namespace"
mariadb_root_pw=$(LC_ALL=C tr -cd "A-Za-z0-9" </dev/urandom | head -c 32)

if ${HELM3} repo list | grep -q "https://charts.bitnami.com/bitnami"; then
  echo "Bitnami repository already exists, updating it..."
  ${HELM3} repo update
else
  echo "Adding Bitnami repository..."
  ${HELM3} repo add bitnami https://charts.bitnami.com/bitnami
fi

"${HELM3}" install mariadb bitnami/mariadb \
    --version 11.0.9 \
    --set auth.rootPassword="${mariadb_root_pw}" \
    --wait

# create ProviderConfig
"${KUBECTL}" create secret generic mariadb-creds \
    --from-literal username="root" \
    --from-literal password="${mariadb_root_pw}" \
    --from-literal endpoint="mariadb.default.svc.cluster.local" \
    --from-literal port="3306"

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
EOF
)"
echo "${PROVIDER_CONFIG_YAML}" | "${KUBECTL}" apply -f -

echo_step "creating MySQL Database resource"
# create DB
"${KUBECTL}" apply -f ${projectdir}/examples/mysql/database.yaml

echo_info "check if is ready"
"${KUBECTL}" wait --timeout 2m --for condition=Ready -f ${projectdir}/examples/mysql/database.yaml
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

# ----------- cleaning mysql related resources

echo_step "uninstalling secret and provider config for mysql"
"${KUBECTL}" delete secret mariadb-creds

echo_step "uninstalling MariaDB Helm chart from default namespace"
"${HELM3}" uninstall mariadb

# ----------- success
echo_success "Mysql Integration tests succeeded!"