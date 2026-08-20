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
`contract-input/GATEWAY_HTTP_ANCHOR.txt`. Read the chapter, source commit,
route count, and freeze date **from that file**. They are deliberately not
restated here: this paragraph used to say `v0.9` / `2026-08-05` / five routes
and stayed wrong for months after the anchor moved to four routes at
`2026-08-18`, because no gate compares prose against the anchor. The route
contract is owned by Sluice and is not reimplemented or copied into this
repository.

Every file under `contract-input/` must be hashed by
`scripts/check-contract-pin.sh`. Do not add an unpinned file there — it will
read as frozen fact while nothing keeps it current. If a reference copy is
worth keeping, pin it; if it is not worth pinning, it does not belong in
`contract-input/`.

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
  an animation is not a logo, and an earlier draft of this entry wrongly said it
  was being dropped too. Owner approved removing `logo_url` inside `runtime.v1`
  instead of opening `runtime.v2`.
  **Why the rule did not bite here:** neither Sluice nor the workbench has
  launched, `runtime.v1` has no external consumer, and its only consumer is our
  own workbench, which had not yet raised its pin. The compatibility this rule
  protects did not exist yet, while a `v2` split would have cost a duplicated
  package and an import-path migration for zero real benefit. Tag 4 is `reserved`, so the wire format stays unambiguous.
  **The exception is one-off and expires at launch:** once the workbench runs
  against a released Sluice, breaking changes go to `runtime.v2` as written
  above. Do not cite this entry as precedent for a post-launch removal.

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
