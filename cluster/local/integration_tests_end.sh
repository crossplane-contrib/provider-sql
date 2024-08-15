#!/usr/bin/env bash
set -e

SCRIPT_DIR="$(dirname "$(realpath "$0")")"
source "$SCRIPT_DIR/common_functions.sh"

# ---- uninstall provider
uninstall_provider

echo_success " All integration tests succeeded!"
