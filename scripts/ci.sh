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

# gofmt 必须排在最前，且必须显式判空——`gofmt -l` 对未格式化的文件仍然退出 0，
# 只把文件名打到 stdout，所以 `set -e` 一个都拦不住。
#
# 为什么补这一腿：本仓此前**没有**格式门禁，gateway.go 就带着未格式化的结构体
# 对齐一路合进了 master（SDK-006 落地时才被 `gofmt -l` 顺手照出来）。
# 格式漂移本身不改语义，但它让后续每个 diff 都掺进无关的对齐噪声，
# 掩盖真正的改动面——而对齐是按「同一段连续字段」算的，加一行就可能重排一整块。
unformatted=$(gofmt -l .)
if [ -n "$unformatted" ]; then
  echo "gofmt 未通过，以下文件需要格式化：" >&2
  echo "$unformatted" >&2
  exit 1
fi

go build ./...

# conformance 代码使用 build tag；短测试会跳过真实 E14 compose 环境，
# 但仍会执行所有不依赖 gateway、mTLS 或签名输入的离线断言。
go build -tags conformance ./...
go vet ./...
go vet -tags conformance ./...
go test ./...
go test -tags conformance -short ./...
"$root_dir/scripts/check-contract-pin.sh"
