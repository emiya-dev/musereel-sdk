# musereel-sdk

`musereel-sdk` is the public Go SDK boundary for controlled MuseReel workbench
instances. This repository is intentionally private until the public-release
decision is made.

SDK-001 establishes only the Go module baseline and the contract
synchronization mechanism. It does not contain client, transport, assertion,
mock, example-business, or generated protobuf code. Authentication and
transport belong to SDK-002, invocation wrappers to SDK-003, and control-plane
work to SDK-004.

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

Protobuf code generation is deliberately absent from SDK-001. Generated
artifacts will be introduced with the SDK-002 toolchain and must be released
with the matching proto, server implementation, documentation, and contract
tests.

Run the complete local baseline with:

```sh
./scripts/ci.sh check
```

The pin-only check is:

```sh
./scripts/check-contract-pin.sh
```

## S31 boundary

This SDK is for controlled workbench instances running on an owning or
third-party backend. It must not be embedded in a browser, mobile client, or
customer-controlled frontend.

The SDK boundary may eventually carry authentication materials, actor
assertions, idempotency, and safe-retry behavior. The ledger, pricing,
compliance, and supplier logic never belong in this SDK. The SDK must not
provide any capability that bypasses server-side validation.

This repository intentionally contains no business client logic yet. The
absence of client code is part of the SDK-001 boundary, not an incomplete
implementation.

## Project status

- Module: `github.com/emiya-dev/musereel-sdk`
- Go language version: `1.25`
- Runtime contract: `runtime.v1`, frozen by `contract-input/SOURCE.txt`
- External dependencies: none
- License: TBD; no license choice has been made by the owner
- Hosted CI/workflow wiring: TBD; the local shell gate is the only CI shape in
  this milestone
