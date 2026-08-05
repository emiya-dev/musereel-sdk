#!/bin/sh

set -eu

root_dir=$(CDPATH= cd -- "$(dirname -- "$0")/.." && pwd)
proto_path="$root_dir/contract-input/runtime.proto"
source_path="$root_dir/contract-input/SOURCE.txt"

if [ ! -f "$proto_path" ]; then
  echo "contract pin check: missing $proto_path" >&2
  exit 1
fi

if [ ! -f "$source_path" ]; then
  echo "contract pin check: missing $source_path" >&2
  exit 1
fi

expected_hash=$(sed -n 's/^sha256:[[:space:]]*//p' "$source_path")
source_repo=$(sed -n 's/^source_repo:[[:space:]]*//p' "$source_path")
source_path_value=$(sed -n 's/^source_path:[[:space:]]*//p' "$source_path")
source_commit=$(sed -n 's/^source_commit:[[:space:]]*//p' "$source_path")
frozen_at=$(sed -n 's/^frozen_at:[[:space:]]*//p' "$source_path")

if [ -z "$expected_hash" ] || [ -z "$source_repo" ] || \
   [ -z "$source_path_value" ] || [ -z "$source_commit" ] || \
   [ -z "$frozen_at" ]; then
  echo "contract pin check: SOURCE.txt is missing required pin metadata" >&2
  exit 1
fi

hash_length=$(printf '%s' "$expected_hash" | awk '{print length}')
case "$hash_length" in
  64) ;;
  *)
    echo "contract pin check: SOURCE.txt sha256 must be 64 characters" >&2
    exit 1
    ;;
esac

case "$expected_hash" in
  *[!0123456789abcdef]*)
    echo "contract pin check: SOURCE.txt sha256 must be lowercase hex" >&2
    exit 1
    ;;
esac

if command -v shasum >/dev/null 2>&1; then
  actual_hash=$(shasum -a 256 "$proto_path" | awk 'NR == 1 { print $1 }')
elif command -v sha256sum >/dev/null 2>&1; then
  actual_hash=$(sha256sum "$proto_path" | awk 'NR == 1 { print $1 }')
else
  echo "contract pin check: no shasum or sha256sum available" >&2
  exit 1
fi

if [ "$actual_hash" != "$expected_hash" ]; then
  echo "contract pin check: SHA-256 mismatch" >&2
  echo "  source commit: $source_commit" >&2
  echo "  expected:      $expected_hash" >&2
  echo "  actual:        $actual_hash" >&2
  exit 1
fi

echo "contract pin check: OK ($actual_hash)"
