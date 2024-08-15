#!/usr/bin/env bash
set -e

SCRIPT_DIR="$(dirname "$(realpath "$0")")"
# shellcheck source="$SCRIPT_DIR/common_functions.sh"
source "$SCRIPT_DIR/common_functions.sh"

source "$SCRIPT_DIR/integration_tests_begin.sh"
if [ $? -ne 0 ]; then
  echo "integration_tests_begin.sh failed. Exiting."
  exit 1
fi

source "$SCRIPT_DIR/integration_tests_mysql.sh"
if [ $? -ne 0 ]; then
  echo "integration_tests_mysql.sh failed. Exiting."
  exit 1
fi

sleep 60

source "$SCRIPT_DIR/integration_tests_postgres.sh"
if [ $? -ne 0 ]; then
  echo "integration_tests_postgres.sh failed. Exiting."
  exit 1
fi

source "$SCRIPT_DIR/integration_tests_end.sh"
if [ $? -ne 0 ]; then
  echo "integration_tests_postgres.sh failed."
  exit 1
fi