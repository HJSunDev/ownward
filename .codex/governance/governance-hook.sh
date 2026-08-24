#!/bin/sh
set -u

governance_root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
binary="$governance_root/bin/governance-cli"

if [ ! -x "$binary" ]; then
  if [ "${1-}" = "hook" ]; then
    printf '{}\n'
    exit 0
  fi
  printf '%s\n' 'governance runtime is not installed; run install-runtime.ps1 or the platform installer' >&2
  exit 1
fi

if [ "${1-}" = "hook" ]; then
  output=$("$binary" "$@") || {
    printf '{}\n'
    exit 0
  }
  printf '%s\n' "$output"
  exit 0
fi

exec "$binary" "$@"
