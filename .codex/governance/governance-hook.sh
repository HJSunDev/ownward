#!/bin/sh
set -u

is_hook=false
if [ "${1-}" = "hook" ]; then
  is_hook=true
fi

fail_governance() {
  code=$1
  if [ "$is_hook" = true ]; then
    printf '{}\n'
    exit 0
  fi
  exit "$code"
}

governance_root=$(CDPATH= cd -- "$(dirname -- "$0")" && pwd)
binary_directory="$governance_root/bin"
binary="$binary_directory/governance-cli"

must_build=false
if [ ! -x "$binary" ]; then
  must_build=true
elif find "$governance_root" -name '*.go' -newer "$binary" -print -quit | grep -q .; then
  must_build=true
elif [ "$governance_root/go.mod" -nt "$binary" ]; then
  must_build=true
fi

if [ "$must_build" = true ]; then
  mkdir -p "$binary_directory" || fail_governance $?
  (cd "$governance_root" && go build -trimpath -o "$binary" ./cmd/governance-cli) || fail_governance $?
fi

if [ "$is_hook" = true ]; then
  output=$("$binary" "$@") || fail_governance $?
  printf '%s\n' "$output"
  exit 0
fi

exec "$binary" "$@"
