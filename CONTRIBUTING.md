# Contributing

This repository follows the S31 boundary and the frozen SDK-001 contract
discipline. Changes must stay within the public SDK boundary and must not add
ledger, pricing, compliance, or supplier behavior.

## Contract refresh

`contract-input/runtime.proto` is a frozen mirror, not a second fact source.
The sole source of truth is the pinned file in the Sluice repository described
by `contract-input/SOURCE.txt`. Do not hand-edit the mirror. Refresh it from
the source checkout, update the source commit, SHA-256, and freeze date in the
pin record, and run the local gate. A hash mismatch is a failed change.

The gateway HTTP surface is anchored by
`contract-input/GATEWAY_HTTP_ANCHOR.txt`: frozen chapter `06`, document
version `v0.9`, freeze date `2026-08-05`, and five routes. The route contract is
owned by Sluice and is not reimplemented or copied into this repository by
SDK-001.

## Breaking-change discipline (frozen §1.2)

- `runtime.v1` only permits backward-compatible additions of fields. Do not
  reuse an old tag, change the meaning of an existing field, or silently
  remove a field.
- Breaking RPC or message semantics go into `runtime.v2`. A breaking HTTP
  route uses a new versioned path.
- The following changes are SDK breaking changes even when they are
  protobuf-wire-compatible: assertion claims, operation strings, canonical
  paths, JCS rules, nonce semantics, idempotency scope, error-code `retryable`
  semantics, and the response whitelist.
- Documentation, proto, server implementation, SDK generated artifacts, and
  contract tests must be released at the same version.
- SDK CI does not replace Sluice server-side gates.

The rule applies to the full contract, including behavior that is not encoded
by protobuf wire format. Before making a change, classify it as compatible or
breaking and select the matching runtime version or HTTP path.

## Local gate

The required SDK-001 gate is:

```sh
./scripts/ci.sh check
```

It runs, in order, `go build ./...`, `go vet ./...`, `go test ./...`, and the
contract pin check. The repository has no hosted workflow in this milestone.

## S31 negative boundary

Do not add browser, mobile, or customer-controlled frontend integration. Do
not add a capability intended to bypass server-side validation. Ledger,
pricing, compliance, and supplier logic are permanently out of scope for the
SDK, even if a proposed implementation would be convenient for callers.

## Release and ownership TBDs

- License selection is owner TBD.
- Hosted-platform CI wiring is owner TBD.
- Protobuf codegen toolchain and version are owner TBD for SDK-002.
