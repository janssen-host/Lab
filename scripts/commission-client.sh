#!/usr/bin/env bash
set -euo pipefail

commission_host() {
  local role=$1 host=$2 script_dir=$3 repo_dir key_dir tmp_dir remote_dir file
  repo_dir=$(cd "$script_dir/.." && pwd)
  key_dir="$HOME/.ssh"
  if [[ ! $host =~ ^[a-zA-Z0-9._:-]+$ || $host == -* ]]; then
    echo 'Provide an SSH config host alias without a username.' >&2
    return 2
  fi
  for file in "$key_dir/lab_controller.pub" "$script_dir/commission-common.sh" "$script_dir/commission-remote.sh"; do
    if [[ ! -r $file ]]; then echo "File is missing or unreadable: $file" >&2; return 1; fi
  done
  if [[ $role == api ]]; then
    for file in "$key_dir/lab_controller" "$repo_dir/src/api/compose/docker-compose.yml" "$repo_dir/src/api/compose/.env"; do
      if [[ ! -r $file ]]; then echo "File is missing or unreadable: $file" >&2; return 1; fi
    done
  elif [[ $role == database && ! -r $repo_dir/src/dbserver/docker-compose.yml ]]; then
    echo 'Database Compose file is missing or unreadable.' >&2
    return 1
  fi
  tmp_dir=$(mktemp -d "${TMPDIR:-/tmp}/lab-commission.XXXXXXXX")
  chmod 700 "$tmp_dir"
  cp "$script_dir/commission-common.sh" "$script_dir/commission-remote.sh" "$key_dir/lab_controller.pub" "$tmp_dir/"
  case "$role" in
    api)
      cp "$key_dir/lab_controller" "$tmp_dir/lab_controller"
      cp "$repo_dir/src/api/compose/docker-compose.yml" "$tmp_dir/api-compose.yml"
      cp "$repo_dir/src/api/compose/.env" "$tmp_dir/api.env"
      ;;
    database) cp "$repo_dir/src/dbserver/docker-compose.yml" "$tmp_dir/db-compose.yml" ;;
  esac
  chmod 600 "$tmp_dir"/*
  remote_dir=$(ssh -n "$host" 'umask 077; mktemp -d /tmp/lab-commission.XXXXXXXX') || { rm -rf "$tmp_dir"; return 1; }
  if [[ ! $remote_dir =~ ^/tmp/lab-commission\.[a-zA-Z0-9]+$ ]]; then
    echo 'Unexpected remote temporary directory; aborting.' >&2
    rm -rf "$tmp_dir"
    return 1
  fi
  if ! scp -q "$tmp_dir"/* "$host:$remote_dir/"; then
    ssh -n "$host" "rm -rf -- '$remote_dir'" || true
    rm -rf "$tmp_dir"
    return 1
  fi
  rm -rf "$tmp_dir"
  local status=0
  ssh -t "$host" "sudo bash '$remote_dir/commission-remote.sh' '$role' '$remote_dir'" || status=$?
  ssh -n "$host" "rm -rf -- '$remote_dir'" || { echo "Could not remove remote temporary directory: $remote_dir" >&2; status=1; }
  return "$status"
}
