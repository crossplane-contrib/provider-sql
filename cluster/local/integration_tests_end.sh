#!/usr/bin/env bash
set -e

SCRIPT_DIR="$(dirname "$(realpath "$0")")"
source "$SCRIPT_DIR/common_functions.sh"

echo_step "--- CLEAN-UP ---"
cleanup_provider

echo_success " All integration tests succeeded!"
