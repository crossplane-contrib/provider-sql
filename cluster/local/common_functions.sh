#!/usr/bin/env bash
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

# uninstall provider
uninstall_provider(){
    echo "${INSTALL_YAML}" | "${KUBECTL}" delete -f -
    # check pods deleted
    timeout=60
    current=0
    step=3
    while [[ $(kubectl get providerrevision.pkg.crossplane.io -o name | wc -l | xargs) != "0" ]]; do
      echo "waiting for provider to be deleted for another $step seconds"
      current=$current+$step
      if ! [[ $timeout > $current ]]; then
        echo_error "timeout of ${timeout}s has been reached"
      fi
      sleep $step;
    done
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
