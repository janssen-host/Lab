#!/usr/bin/env bash
set -euo pipefail
source "$(dirname "${BASH_SOURCE[0]}")/commission-common.sh"

role=$1
files=$2
require_root
prepare_controller "$files/lab_controller.pub"
case "$role" in
  api)
    install -o controller -g controller -m 600 "$files/lab_controller" "$CONTROLLER_HOME/.ssh/id_ed25519"
    install -d -o controller -g controller -m 700 "$CONTROLLER_HOME/stacks/api"
    install -o controller -g controller -m 644 "$files/api-compose.yml" "$CONTROLLER_HOME/stacks/api/docker-compose.yml"
    install -o controller -g controller -m 600 "$files/api.env" "$CONTROLLER_HOME/stacks/api/.env"
    login_ghcr llllogan
    ;;
  workload)
    for network in cloudflare caddy; do
      if ! docker network inspect "$network" >/dev/null 2>&1; then
        docker network create --driver bridge "$network" >/dev/null
      fi
    done
    login_ghcr llllogan
    ;;
  database)
    install -d -o controller -g controller -m 700 "$CONTROLLER_HOME/stacks/dbserver"
    install -o controller -g controller -m 644 "$files/db-compose.yml" "$CONTROLLER_HOME/stacks/dbserver/docker-compose.yml"
    ;;
  *) echo "Unknown host role: $role" >&2; exit 2 ;;
esac
echo "$role host commissioned."
