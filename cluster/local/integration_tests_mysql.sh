#!/usr/bin/env bash
set -e

SCRIPT_DIR="$(dirname "$(realpath "$0")")"
source "$SCRIPT_DIR/common_functions.sh"

echo_step "--- INTEGRATION TESTS FOR MySQL ---"
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

# ----------- success
echo_success "Mysql Integration tests succeeded!"