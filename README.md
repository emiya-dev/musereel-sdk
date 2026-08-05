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

The gateway HTTP surface is anchored to frozen chapter `06`, document version
`v0.9`, and the 2026-08-05 SDK-001 freeze baseline. The five-route HTTP
contract remains owned by Sluice; this SDK baseline records the anchor rather
than duplicating route implementation or an additional HTTP contract source.

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

Conformance 不进入默认 `check`；先确认骨架可编译，再在目标环境运行：

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
`MUSEREEL_CONFORMANCE_DELIVERY_MODE`（`async` 或 `stream`）、
`MUSEREEL_CONFORMANCE_ARTIFACT_ID`；可选项为
`MUSEREEL_CONFORMANCE_MTLS_SERVER_NAME`、
`MUSEREEL_CONFORMANCE_SPEC_INPUT_JSON`、
`MUSEREEL_CONFORMANCE_SPEC_PARAMETERS_JSON`、
`MUSEREEL_CONFORMANCE_MODERATION_RECEIPT`、
`MUSEREEL_CONFORMANCE_EVENT_ID`。

目标由 sluice 侧 compose 提供假上游（E14 夹具）；环境缺失时测试会 fail-fast 输出「需要 sluice compose 环境」，不会 skip。

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
