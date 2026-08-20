# musereel-sdk

`musereel-sdk` is the public Go SDK boundary for controlled MuseReel workbench
instances. This repository is intentionally private until the public-release
decision is made.

SDK-002 adds the authentication and transport foundation: mTLS loading and
certificate rotation, short-lived runtime-token caching, actor assertions, and
the generic authenticated gRPC call boundary. Invocation wrappers belong to
SDK-003. SDK-004 adds committed protobuf/gRPC generated code and the typed
runtime control-plane client.

## Contract synchronization

`contract-input/` is the formal frozen contract layout:

- `runtime.proto` is a frozen mirror copied from the Sluice repository.
- `SOURCE.txt` pins the source repository, source path, source commit,
  SHA-256, and freeze date.
- `GATEWAY_HTTP_ANCHOR.txt` records the frozen gateway HTTP chapter and
  document-version anchor.

The copy of `runtime.proto` is a frozen mirror. The sole source of truth is in
the `sluice` repository; manually editing the copy is a violation. A contract
refresh must be made from the pinned Sluice source and must update the pin
metadata as one reviewed change. The local gate recomputes the mirror's
SHA-256 and fails unless it equals the pinned value. It does not fetch the
internal source repository.

Every **mirror** under `contract-input/` must be hashed by
`scripts/check-contract-pin.sh`. (The pin records themselves — `SOURCE.txt` and
`GATEWAY_HTTP_ANCHOR.txt` — are the gate's input rather than its subject; see
`CONTRIBUTING.md`.) A mirror that lives there but is not hashed by the gate
reads as authoritative while nothing keeps it current.
`contract-input/reference/jcs-server-reference.go.txt` was exactly that: an
unpinned server-side JCS copy that had gone stale on the one rule most likely
to break request fingerprints — it sorted object keys with `sort.Strings`
(UTF-8 byte order) while `jcs/jcs.go` and the live Sluice implementation both
sort by UTF-16 code units per RFC 8785 §3.2.3. The two disagree on non-BMP
property names, so anyone reimplementing from that copy would have produced
fingerprints that fail `actor_assertion_invalid` with no hint at the cause. It
has been removed. The behavioural fact source for JCS is `jcs/jcs.go` together
with the UTF-16 ordering assertions in `jcs/jcs_test.go`.

`ResolveRegistrationRequest.domain` is supplied by the frontend and forwarded
unchanged through the SDK; the SDK does not derive, normalize, or complete it
from Host, Origin, or configuration. `invite_code` remains the frozen wire
field and is a channel identifier only.

The gateway HTTP surface is anchored by `contract-input/GATEWAY_HTTP_ANCHOR.txt`.
That file — not this README — carries the frozen chapter, source commit, route
count, and freeze date; read it there rather than trusting a copy. The route
contract remains owned by Sluice; this SDK records the anchor rather than
duplicating route implementation or an additional HTTP contract source.

Anchor values are deliberately not restated here. An earlier copy in this file
and in `CONTRIBUTING.md` still claimed document version `v0.9`, the 2026-08-05
baseline, and five routes long after the anchor moved to four routes at
`2026-08-18` (BE-166/S74 removed the public site-context route). A restated
constant has no gate keeping it honest, so it silently rots; the anchor file is
the single place to change.

`runtime/runtime.pb.go` and `runtime/runtime_grpc.pb.go` are generated from the
frozen `contract-input/runtime.proto` with the pinned local protoc toolchain.
`ExchangeRuntimeToken` now uses the generated protobuf messages and the
standard gRPC protobuf codec; the former hand-written transition codec was
removed after its golden-byte assertions were migrated to generated types.

Run the complete local baseline with:

```sh
./scripts/ci.sh check
```

The pin-only check is:

```sh
./scripts/check-contract-pin.sh
```

## Conformance（手动 build tag）

Conformance 的离线单测由默认 `check` 以 `go test -tags conformance -short ./...` 门禁；真实 compose 环境运行：

```sh
go build -tags conformance ./...
go test -tags conformance ./conformance
```

必填环境变量：`MUSEREEL_CONFORMANCE_GATEWAY_URL`、
`MUSEREEL_CONFORMANCE_RUNTIME_TARGET`、
`MUSEREEL_CONFORMANCE_MTLS_CERT_FILE`、
`MUSEREEL_CONFORMANCE_MTLS_KEY_FILE`、
`MUSEREEL_CONFORMANCE_MTLS_CA_FILE`、
`MUSEREEL_CONFORMANCE_SIGNING_PRIVATE_KEY_FILE`、
`MUSEREEL_CONFORMANCE_SIGNING_KID`、
`MUSEREEL_CONFORMANCE_INSTANCE_ID`、`MUSEREEL_CONFORMANCE_TENANT_ID`、
`MUSEREEL_CONFORMANCE_SESSION_ID`、`MUSEREEL_CONFORMANCE_ACTOR`、
`MUSEREEL_CONFORMANCE_SKU_ID`、`MUSEREEL_CONFORMANCE_TASK_REF`、
`MUSEREEL_CONFORMANCE_DELIVERY_MODE`（`async` 或 `stream`）。可选项为
`MUSEREEL_CONFORMANCE_MTLS_SERVER_NAME`、
`MUSEREEL_CONFORMANCE_SPEC_SCHEMA_VERSION`（**没有单一默认值**：留空时按 SKU 取——
`video.generate.v1` 为 `3`，其余六个 SKU 为 `1`；见 `conformance.go` 的
`conformanceSchemaVersionBySKU`）、
`MUSEREEL_CONFORMANCE_SPEC_INPUT_JSON`、
`MUSEREEL_CONFORMANCE_SPEC_PARAMETERS_JSON`、
`MUSEREEL_CONFORMANCE_MODERATION_RECEIPT`、
`MUSEREEL_CONFORMANCE_EVENT_ID`。

产物 SKU 的 artifact ID **不通过环境变量提供**：契约要求 `{artifact_id}` 由服务端签发
（`invocation_artifact.id` 是随机 UUID），客户端预置的值不可能命中。harness 从本次调用的
终态 `snapshot.result` 解析该 ID，再经 SDK 下载接口校验 `Content-Digest`。

这条腿跑没跑过，唯一的机器可读锚是 stdout 上的一行：

```
ARTIFACT_LEG=downloaded sku=<sku_id> count=<n>   # 产物 SKU，n 必 ≥ 1
ARTIFACT_LEG=skipped sku=<sku_id>                # 非产物 SKU（text / lyrics / moderation）
```

⚠ 单跑一个非产物 SKU 只会打 `skipped`——那证明不了 artifact 传输腿是通的。
要覆盖这条腿，驱动矩阵必须让 `downloaded` 在 video / image / music / speech 上各出现一次。

目标由 sluice 侧 compose 提供假上游（E14 夹具）。缺环境时 `TestSluiceComposeConformance`
会 fail-fast 输出「需要 sluice compose 环境」而不是 skip；**唯一的例外是 `-short`**，
它整条跳过这个 compose 腿，好让同包的离线单测能进默认 `check`。

## S31 boundary

This SDK is for controlled workbench instances running on an owning or
third-party backend. It must not be embedded in a browser, mobile client, or
customer-controlled frontend.

The SDK boundary may eventually carry authentication materials, actor
assertions, idempotency, and safe-retry behavior. The ledger, pricing,
compliance, and supplier logic never belong in this SDK. The SDK must not
provide any capability that bypasses server-side validation.

The SDK contains only typed transport/control-plane wrappers. Ledger, pricing,
compliance, and supplier decisions remain server-side; the runtime client
passes their string amounts and units through without numeric interpretation.

## Project status

- Module: `github.com/emiya-dev/musereel-sdk`
- Go language version: `1.25`
- Runtime contract: `runtime.v1`, frozen by `contract-input/SOURCE.txt`
- External dependencies: grpc-go v1.80.0, protobuf v1.36.11, and the locked
  indirect closure recorded in `go.mod`/`go.sum`
- License: TBD; no license choice has been made by the owner
- Hosted CI/workflow wiring: TBD; the local shell gate is the only CI shape in
  this milestone
