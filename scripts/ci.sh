#!/bin/sh

set -eu

root_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root_dir"

# Keep the local gate independent of machine-specific Go cache locations.
# No module dependencies are expected in SDK-001; GOPROXY=off makes that
# boundary explicit and prevents an accidental network fetch.
if [ -z "${GOCACHE+x}" ]; then
  GOCACHE="$root_dir/.cache/go-build"
  export GOCACHE
fi
if [ -z "${GOMODCACHE+x}" ]; then
  GOMODCACHE="$root_dir/.cache/go-mod"
  export GOMODCACHE
fi
GOPROXY=off
export GOPROXY
GOTOOLCHAIN=local
export GOTOOLCHAIN

if [ "$#" -ne 1 ] || [ "$1" != "check" ]; then
  echo "usage: $0 check" >&2
  exit 2
fi

go build ./...
go vet ./...
go test ./...
"$root_dir/scripts/check-contract-pin.sh"
