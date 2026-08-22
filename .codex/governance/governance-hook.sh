#!/bin/sh
set -eu

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
  mkdir -p "$binary_directory"
  (cd "$governance_root" && go build -trimpath -o "$binary" ./cmd/governance-cli)
fi

exec "$binary" "$@"
