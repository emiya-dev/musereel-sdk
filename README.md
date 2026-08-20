# musereel-sdk

[English](README.md) | [简体中文](README.zh-CN.md)

`musereel-sdk` is the Go SDK boundary for controlled MuseReel workbench
instances and the backends that own or operate them. This repository is
intentionally private until the public-release decision is made.

“Public SDK” here describes the S31 boundary for third-party backends; it does
not announce a public repository release. This change makes no external
publication or hosting claim.

## Installation

The module path is:

```sh
go get github.com/emiya-dev/musereel-sdk
```

The repository remains private, so the command requires the caller's normal
private-module access to be configured. The module declares Go `1.25`.

## Quick start

The compile-checked starting points are in [`example_test.go`](example_test.go).
They use only exported SDK APIs, generate signing keys in memory, and use
placeholder configuration.

Every example carries an `// Output:` directive, which is what makes `go test`
actually **run** it rather than merely compile it. That is deliberate: an
example that is only compiled proves that the API names exist, not that the
code works. `ExampleGatewayCreateRequest` calls `Validate` on the request it
shows, so an invalid example request fails the package tests instead of
misleading a reader who copies it.

Because they really run, the examples never open a network connection, read a
real certificate, or depend on environment variables.
`ExampleRequestFingerprint` and `ExampleCanonicalGatewayPath` are fully
deterministic and therefore assert their exact output; the fingerprint value
pins the JCS canonicalization result, which cannot change without being a
breaking change under `CONTRIBUTING.md`.

The examples cover these first steps:

- `ExampleMTLSConfig` shows the local mTLS file configuration. A real client
  passes it to `NewTLSConfig` or `NewMTLSCredentials`, then uses `DialRuntime`.
- `ExampleNewEd25519Signer`, `ExampleNewEd25519SignerFromPEM`,
  `ExampleNewES256Signer`, and `ExampleNewES256SignerFromPEM` show the
  registered signing-key forms.
- `ExampleNewToken` and `ExampleNewCachedTokenSource` show a local
  `TokenSource`; a runtime
  connection normally uses `NewGRPCTokenSource` to exchange the mTLS identity
  for a short-lived token.
- `ExampleNewAuthenticatedClient`, `ExampleNewGatewayClient`,
  `ExampleGatewayCreateRequest`, and `ExampleNewRuntimeClient` show
  construction of the client surfaces and a validated request without making
  a request.
- `ExampleRequestFingerprint`, `ExampleCanonicalGatewayPath`, and
  `ExampleSignAssertion` and `ExampleSignActorAssertion` show the request
  identity that must remain stable across an authenticated retry.

## API overview

The package is deliberately a transport and control-plane boundary:

| Area | Main exported API |
| --- | --- |
| mTLS and transport | `MTLSConfig`, `NewTLSConfig`, `NewMTLSCredentials`, `DialRuntime` |
| Runtime tokens | `Token`, `NewToken`, `TokenSource`, `CachedTokenSource`, `GRPCTokenSource`, `NewCachedTokenSource`, `NewGRPCTokenSource`, `WithClock` |
| Assertions and identity | `Signer`, `NewEd25519Signer`, `NewEd25519SignerFromPEM`, `NewES256Signer`, `NewES256SignerFromPEM`, `AssertionInput`, `SignAssertion`, `SignActorAssertion`, `RequestFingerprint`, `CanonicalGatewayPath` |
| Authenticated gRPC | `AuthenticatedClient`, `NewAuthenticatedClient`, `AssertionCall` |
| Runtime control plane | `RuntimeClient`, `NewRuntimeClient`, `WithRuntimeAssertion`, typed `runtime.v1` methods, and `RuntimeRPCError` |
| Gateway invocation surface | `GatewayClient`, `NewGatewayClient`, `GatewayCreateRequest`, `GatewayInvocationSpec`, `CreateAsync`, `CreateStream`, `Get`, `GetWithETag`, `Cancel`, `DownloadArtifact`, and `NewPoller` |
| Canonical JSON | `jcs.CanonicalizeJSON` from the `github.com/emiya-dev/musereel-sdk/jcs` subpackage |

The generated protobuf messages and service clients live under the `runtime`
subpackage. `ExchangeRuntimeToken` is intentionally kept behind
`GRPCTokenSource`; `RuntimeClient` is for the typed runtime control plane after
the token exchange.

## Core concepts

### mTLS bootstrap

`MTLSConfig` names the client certificate, private key, CA bundle, and optional
server name. `NewTLSConfig` validates the current pair and re-reads the pair
for each handshake so an atomic file replacement can rotate the certificate.
The private key is kept inside the TLS stack and is not included in errors or
formatted configuration output. `DialRuntime` opens the gRPC connection with
these transport credentials.

### Runtime tokens and authenticated gRPC

`GRPCTokenSource` sends the generated empty
`ExchangeRuntimeTokenRequest` over the mTLS connection and caches the returned
short-lived `Bearer` token. `CachedTokenSource` provides a fixed 60-second
refresh window and single-flight exchange behavior; `Token` remains opaque by
default and `NewToken` is available for custom local `TokenSource`
implementations. The exchange request carries no tenant, instance, or scope
fields.

`AuthenticatedClient` attaches the Bearer token to generic unary gRPC calls
and retries exactly once after the stable `runtime_unauthenticated` code. It
passes the same business arguments through the retry, preserving caller-owned
idempotency keys and request fingerprints. `RuntimeClient` uses this boundary
for its typed runtime methods.

### Actor assertions and JCS fingerprints

`Signer` supports the registered EdDSA and ES256 forms. `SignAssertion`
produces a compact JWS with a fresh nonce and a maximum validity window of 60
seconds; `SignActorAssertion` is the transport-only convenience form. The
`AssertionInput` identity context includes the already-bound instance, tenant,
session, and actor values.

`RequestFingerprint` computes the frozen SHA-256, unpadded base64url request
fingerprint from the method, canonical path, actor, idempotency key, and JCS
body. Empty body is canonicalized as `{}`. The `jcs` subpackage implements the
server's RFC 8785 subset and orders object property names by UTF-16 code units,
not Go's UTF-8 `sort.Strings` order.

### Idempotency and invocation delivery

Mutation calls use the caller's idempotency key; query calls do not carry one.
The assertion-aware retry signs a fresh nonce while the business identity and
fingerprint remain fixed. `GatewayClient` keeps async and stream creation as
separate methods, leaves `delivery_mode` out of the create body, and exposes
ETag-aware reads, cancellation, SSE handling, and artifact download with
`Content-Digest` verification.

`ResolveRegistrationRequest.domain` is supplied by the frontend and is passed
through unchanged. The SDK does not derive, normalize, or complete it from
Host, Origin, or configuration. `invite_code` is the frozen wire field for a
channel identifier only.

## Contract synchronization

`contract-input/` is the frozen contract layout. It has exactly two kinds of
file:

- **Mirrors** are copies of Sluice-owned files. `runtime.proto` is the frozen
  runtime mirror, and `frozen_public_error_codes.json` mirrors
  `backend/service/gateway/frozen_public_error_codes.json`. Every mirror must
  be hashed by `scripts/check-contract-pin.sh`.
- **Pin records** carry expected values and are the gate's input rather than
  its subject: `SOURCE.txt` and `GATEWAY_HTTP_ANCHOR.txt`. They are not
  self-hashed. There is no third kind of file.

`SOURCE.txt` pins the source repository, source path, source commit, SHA-256,
freeze date, and the pinned code-generation toolchain metadata.

`contract-input/runtime.proto` is a frozen mirror, not a second fact source.
The sole source of truth is in the `sluice` repository. Hand-editing the
mirror is a violation. A refresh must come from the pinned Sluice source and
update the source commit, SHA-256, and freeze date together as one reviewed
change. The local gate recomputes the mirror SHA-256 and fails unless it equals
the pinned value; it does not fetch the internal source repository.

The gateway HTTP surface is anchored by
[`contract-input/GATEWAY_HTTP_ANCHOR.txt`](contract-input/GATEWAY_HTTP_ANCHOR.txt).
That file alone carries the frozen gateway chapter, source commit, route
count, and freeze date. Read those values there; do not copy them into this
README. The route contract remains owned by Sluice rather than becoming a
second HTTP contract source here.

`contract-input/reference/jcs-server-reference.go.txt` was the warning example
of an unhashed mirror: it looked authoritative while nobody kept it fresh. It used
`sort.Strings` (UTF-8 byte order) for object keys, while `jcs/jcs.go` and the
live Sluice implementation at `backend/pkg/app/core/jcs.go` use the RFC 8785
§3.2.3 UTF-16 code-unit order. The two orders disagree on non-BMP property
names. Reimplementing from that stale file therefore produces
`actor_assertion_invalid` fingerprints with no useful clue. The file has been
deleted; the JCS behavior source is now `jcs/jcs.go` plus the UTF-16 assertion
in `jcs/jcs_test.go`.

`runtime/runtime.pb.go` and `runtime/runtime_grpc.pb.go` are generated from
the frozen `contract-input/runtime.proto` with the pinned local protoc
toolchain. `ExchangeRuntimeToken` uses generated protobuf messages and the
standard gRPC protobuf codec. The hand-written transition codec was removed,
and its golden-byte assertions were moved to the generated types.

## Conformance

Conformance uses a manual `conformance` build tag. The default local check
uses `go test -tags conformance -short ./...`; run the real compose environment
separately with:

```sh
go build -tags conformance ./...
go test -tags conformance ./conformance
```

The required environment variables are:

`MUSEREEL_CONFORMANCE_GATEWAY_URL`,
`MUSEREEL_CONFORMANCE_RUNTIME_TARGET`,
`MUSEREEL_CONFORMANCE_MTLS_CERT_FILE`,
`MUSEREEL_CONFORMANCE_MTLS_KEY_FILE`,
`MUSEREEL_CONFORMANCE_MTLS_CA_FILE`,
`MUSEREEL_CONFORMANCE_SIGNING_PRIVATE_KEY_FILE`,
`MUSEREEL_CONFORMANCE_SIGNING_KID`,
`MUSEREEL_CONFORMANCE_INSTANCE_ID`,
`MUSEREEL_CONFORMANCE_TENANT_ID`,
`MUSEREEL_CONFORMANCE_SESSION_ID`,
`MUSEREEL_CONFORMANCE_ACTOR`,
`MUSEREEL_CONFORMANCE_SKU_ID`,
`MUSEREEL_CONFORMANCE_TASK_REF`, and
`MUSEREEL_CONFORMANCE_DELIVERY_MODE` (`async` or `stream`).

Optional variables are:

`MUSEREEL_CONFORMANCE_MTLS_SERVER_NAME`,
`MUSEREEL_CONFORMANCE_SPEC_SCHEMA_VERSION`,
`MUSEREEL_CONFORMANCE_SPEC_INPUT_JSON`,
`MUSEREEL_CONFORMANCE_SPEC_PARAMETERS_JSON`, and
`MUSEREEL_CONFORMANCE_EVENT_ID`.

There is deliberately no environment variable for the moderation receipt. A
receipt cannot be supplied from outside: the harness mints one by first running
a `moderation.generate.v1` invocation and reading the receipt out of that
call's terminal result (`mintModerationReceipt` in `conformance.go`).

`MUSEREEL_CONFORMANCE_SPEC_SCHEMA_VERSION` has **no single default**. When it
is empty, `conformance.go`'s `conformanceSchemaVersionBySKU` supplies `3` for
`video.generate.v1` and `1` for each of the other six SKUs.

Artifact IDs are **not** supplied through an environment variable. The
contract requires `{artifact_id}` to be server-issued; `invocation_artifact.id`
is a random UUID, so a client-preseeded value cannot match. The harness parses
the ID from the terminal `snapshot.result`, then uses the SDK download
interface to verify `Content-Digest`.

The machine-readable stdout markers are:

```text
ARTIFACT_LEG=downloaded sku=<sku_id> count=<n>
ARTIFACT_LEG=skipped sku=<sku_id>
```

⚠ Running one non-artifact SKU produces only `skipped`; that does not prove
that the artifact transport leg works. `skipped` is for non-artifact text,
lyrics, and moderation SKUs; a `downloaded` marker must have a count of at
least one. To cover that leg, the driver matrix must produce `downloaded` once
each for video, image, music, and speech.

The target is the Sluice-side compose environment with its E14 fixture. If the
environment is missing, `TestSluiceComposeConformance` fails fast instead of
skipping. The only exception is `-short`, which skips the compose leg so the
offline tests in the same package can enter the default check.

## S31 boundary

This SDK is for controlled workbench instances running on an owning or
third-party backend. It must not be embedded in a browser, a mobile client, or
a customer-controlled frontend.

The SDK boundary may carry authentication materials, actor assertions,
idempotency, and safe-retry behavior. Ledger, pricing, compliance, and
supplier logic permanently do not belong in this SDK, even when implementing
them would be convenient for callers. The SDK must not provide any capability
that bypasses server-side validation.

The package contains typed transport and control-plane wrappers. The runtime
client passes server-provided string amounts and units through without numeric
interpretation; ledger, pricing, compliance, and supplier decisions remain on
the server side.

## Local gate

Run the complete local baseline with:

```sh
./scripts/ci.sh check
```

The gate checks formatting, builds the default and conformance-tagged
packages, vets both build surfaces, runs the default and short conformance
tests, and verifies the contract pins. The pin-only check is:

```sh
./scripts/check-contract-pin.sh
```

The repository has no hosted workflow in this milestone; the local shell gate
is the current CI shape.

## Project status and license

- Module: `github.com/emiya-dev/musereel-sdk`
- Go language version: `1.25`
- Runtime contract: `runtime.v1`, frozen by `contract-input/SOURCE.txt`
- SDK-002 provides mTLS loading and rotation, short-lived runtime-token
  caching, actor assertions, and the generic authenticated gRPC boundary.
- SDK-003 owns invocation wrappers.
- SDK-004 adds committed generated protobuf/gRPC code and the typed runtime
  control-plane client.
- External dependencies: grpc-go `v1.80.0`, protobuf `v1.36.11`, and the
  locked indirect closure recorded in `go.mod`/`go.sum`.
- License: [Apache-2.0](LICENSE). The copyright-holder line is intentionally
  left for the owner to fill in in `LICENSE`.
- Hosted CI/workflow wiring: TBD; the local shell gate is the only CI shape in
  this milestone.
