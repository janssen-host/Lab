#!/usr/bin/env bash
set -euo pipefail

ssh -t ctrl 'sudo -iu controller bash -c '\''set -euo pipefail; cd "$HOME/stacks/api"; docker compose pull; docker compose up -d'\'''
