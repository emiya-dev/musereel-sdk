// Package musereelsdk is the public SDK boundary for controlled MuseReel
// workbench instances. It provides the authentication, assertion-signing,
// Gateway HTTP, and runtime control-plane clients used by an authorized
// workbench process.
//
// "Public" here names the S31 boundary offered to third-party backends. It is
// not a statement about publication: this repository stays private until the
// owner makes a public-release decision.
//
// This package is intended for a controlled workbench instance, not for code
// embedded in a browser, mobile application, or other customer-controlled
// frontend. The main entry points are NewTLSConfig or DialRuntime for mTLS,
// NewGRPCTokenSource and NewRuntimeClient for runtime operations, and
// NewGatewayClient for authenticated invocation operations.
package musereelsdk
