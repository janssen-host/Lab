#!/usr/bin/env bash

set -euo pipefail

require_root() {
  if [[ ${EUID} -ne 0 ]]; then
    echo 'Run this script with sudo.' >&2
    exit 1
  fi
}

require_file() {
  if [[ ! -f $1 || ! -r $1 ]]; then
    echo "File is missing or unreadable: $1" >&2
    exit 1
  fi
}

prepare_controller() {
  local public_key=$1 home key
  require_file "$public_key"
  if ! command -v docker >/dev/null || ! getent group docker >/dev/null; then
    echo 'Docker and its docker group must be installed before commissioning.' >&2
    exit 1
  fi
  if ! id controller >/dev/null 2>&1; then
    useradd --create-home --shell /bin/bash controller
  fi
  home=$(getent passwd controller | cut -d: -f6)
  if [[ -z $home || ! -d $home ]]; then
    echo 'The controller account must have an existing home directory.' >&2
    exit 1
  fi
  usermod -aG docker controller
  install -d -o controller -g controller -m 700 "$home/.ssh"
  touch "$home/.ssh/authorized_keys"
  chown controller:controller "$home/.ssh/authorized_keys"
  chmod 600 "$home/.ssh/authorized_keys"
  while IFS= read -r key || [[ -n $key ]]; do
    [[ -z $key || $key == \#* ]] && continue
    if ! grep -Fqx -- "$key" "$home/.ssh/authorized_keys"; then
      printf '%s\n' "$key" >> "$home/.ssh/authorized_keys"
    fi
  done < "$public_key"
  install -d -o controller -g controller -m 700 "$home/stacks"
  CONTROLLER_HOME=$home
}

login_ghcr() {
  local username=$1 token
  read -r -s -p 'GHCR personal access token: ' token </dev/tty
  printf '\n' >/dev/tty
  if [[ -z $token ]]; then
    echo 'A GHCR personal access token is required.' >&2
    exit 1
  fi
  printf '%s' "$token" | sudo -H -u controller docker login ghcr.io --username "$username" --password-stdin
  unset token
}
