# Contributing

This repository follows the S31 boundary and the frozen SDK-001 contract
discipline. Changes must stay within the backend-facing SDK boundary and must
not add ledger, pricing, compliance, or supplier behavior.

## Scope and review order

Before changing code or contract material, identify the authoritative source,
the wire identity that must remain stable, and the local gate that proves the
change. Documentation, proto, server implementation, SDK generated artifacts,
and contract tests must move together when a contract version changes.

## Contract refresh

`contract-input/runtime.proto` is a frozen mirror, not a second fact source.
The sole source of truth is the pinned file in the Sluice repository described
by `contract-input/SOURCE.txt`. Do not hand-edit the mirror. Refresh it from
the source checkout, update the source commit, SHA-256, and freeze date in the
pin record, and run the local gate. A hash mismatch is a failed change.

The gateway HTTP surface is anchored by
`contract-input/GATEWAY_HTTP_ANCHOR.txt`. Read the chapter, source commit,
route count, and freeze date **from that file**. They are deliberately not
restated here: prose copies of an anchor can stay wrong because no gate
compares prose against the anchor. The route contract is owned by Sluice and
is not reimplemented or copied into this repository.

`contract-input/` holds exactly two kinds of file, and the distinction is what
keeps it honest:

- **Mirrors** — copies of something that lives in Sluice (`runtime.proto`,
  `frozen_public_error_codes.json`, whose Sluice source path is
  `backend/service/gateway/frozen_public_error_codes.json`). Every mirror
  **must** be hashed by `scripts/check-contract-pin.sh`. An unhashed mirror
  reads as frozen fact while nothing keeps it current.
- **Pin records** — the files that *carry* the expected values (`SOURCE.txt`,
  `GATEWAY_HTTP_ANCHOR.txt`). These are the gate's input, not its subject, so
  they are not self-hashed. `SOURCE.txt` is required to exist (the gate exits 1
  without it); `GATEWAY_HTTP_ANCHOR.txt` records a document anchor that this
  repository has no local way to verify — updating it is a reviewed change,
  not a gated one.

Do not add a third kind. A mirror that is not worth hashing does not belong in
`contract-input/` at all.

The removed `reference/jcs-server-reference.go.txt` is the concrete lesson:
it was an un-hashed server-side mirror that looked authoritative while nobody
kept it fresh. It sorted object keys with `sort.Strings` (UTF-8 byte order),
while `jcs/jcs.go` and the live Sluice implementation at
`backend/pkg/app/core/jcs.go` sort by RFC 8785 §3.2.3 UTF-16 code units. Those
orders disagree on non-BMP property names; copying the stale file therefore
produced `actor_assertion_invalid` request fingerprints with no clue why. JCS
behavior is sourced from `jcs/jcs.go` and the UTF-16 assertions in
`jcs/jcs_test.go`.

`ResolveRegistrationRequest.domain` is supplied by the frontend and is
forwarded unchanged. The SDK does not derive, normalize, or complete it from
Host, Origin, or configuration. `invite_code` remains the frozen wire field
and is a channel identifier only.

`runtime/runtime.pb.go` and `runtime/runtime_grpc.pb.go` are generated from
the frozen `contract-input/runtime.proto` with the pinned local protoc
toolchain. `ExchangeRuntimeToken` uses generated protobuf messages and the
standard gRPC protobuf codec; the hand-written transition codec was removed,
and its golden-byte assertions were migrated to generated types.

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

### Pre-launch exception log

The rule above is suspended only by an explicit, dated owner decision recorded
here. An undocumented breaking change in `runtime.v1` is still a failed change.

- **2026-08-14 — `SiteBrandingItem` drops `logo_url` (4), adds `assets` (7).**
  Site branding moved to five closed kind slots in Sluice (`site_branding_asset`),
  so the single named `logo_url` column was deleted rather than kept as a
  compatibility field. `home_animation_url` (5) is **unaffected and stays** —
  an animation is not a logo, and an earlier draft of this entry wrongly said
  it was being dropped too. Owner approved removing `logo_url` inside `runtime.v1`
  instead of opening `runtime.v2`.
  **Why the rule did not bite here:** neither Sluice nor the workbench has
  launched, `runtime.v1` has no external consumer, and its only consumer is our
  own workbench, which had not yet raised its pin. The compatibility this rule
  protects did not exist yet, while a `v2` split would have cost a duplicated
  package and an import-path migration for zero real benefit. Tag 4 is `reserved`,
  so the wire format stays unambiguous.
  **The exception is one-off and expires at launch:** once the workbench runs
  against a released Sluice, breaking changes go to `runtime.v2` as written
  above. Do not cite this entry as precedent for a post-launch removal.

## Local gate

The required SDK-001 gate is:

```sh
./scripts/ci.sh check
```

It runs, in order: `gofmt -l .` (a formatting failure stops the gate before
anything is built), `go build ./...` and `go build -tags conformance ./...`,
`go vet ./...` and `go vet -tags conformance ./...`, `go test ./...` and
`go test -tags conformance -short ./...`, and finally the contract pin check.
The real compose conformance run is a separate environmental exercise.

The `gofmt` step is first on purpose and was added at a cost: this repository
had no formatting gate for a while, and `gateway.go` reached master carrying
unformatted struct alignment. Format drift changes no semantics, but it mixes
alignment noise into every later diff and hides the real change surface. Note
that `gofmt -l` exits 0 even when it lists files, so `ci.sh` checks its output
for emptiness rather than trusting the exit code. The repository has no hosted workflow in this
milestone.

## S31 negative boundary

Do not add browser, mobile, or customer-controlled frontend integration. Do
not add a capability intended to bypass server-side validation. Ledger,
pricing, compliance, and supplier logic are permanently out of scope for the
SDK, even if a proposed implementation would be convenient for callers.

The runtime client passes server-provided string amounts and units through
without numeric interpretation. Decisions about ledger, pricing, compliance,
and suppliers remain server-side.

## Release and ownership

- License is [Apache-2.0](LICENSE). The copyright-holder line is intentionally
  left for the owner to fill in in `LICENSE`.
- Hosted-platform CI wiring is owner TBD.
- Protobuf codegen uses the pinned local protoc toolchain declared by
  `contract-input/SOURCE.txt` and checked against the generated file headers.
