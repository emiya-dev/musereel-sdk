#!/bin/sh

set -eu

root_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
cd "$root_dir"

# Keep build artifacts local while using the preloaded module cache. The
# dependency set is intentionally resolved offline; GOPROXY/GOSUMDB make an
# accidental network fetch impossible.
if [ -z "${GOCACHE+x}" ]; then
  GOCACHE="$root_dir/.cache/go-build"
  export GOCACHE
fi
GOPROXY=off
export GOPROXY
GOSUMDB=off
export GOSUMDB
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
