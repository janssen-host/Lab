#!/usr/bin/env bash
set -euo pipefail
SCRIPT_DIR=$(cd -- "$(dirname -- "${BASH_SOURCE[0]}")" && pwd)
source "$SCRIPT_DIR/commission-client.sh"
if [[ $# -ne 1 ]]; then
  echo "Usage: $0 <ssh-host-alias>" >&2
  exit 2
fi
commission_host api "$1" "$SCRIPT_DIR"
