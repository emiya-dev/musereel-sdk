// Package musereelsdk is the public Go SDK for controlled MuseReel workbench
// instances. It provides the authentication, assertion-signing, Gateway HTTP,
// and runtime control-plane clients used by an authorized workbench process.
//
// This package is intended for a controlled workbench instance, not for code
// embedded in a browser, mobile application, or other customer-controlled
// frontend. The main entry points are NewTLSConfig or DialRuntime for mTLS,
// NewGRPCTokenSource and NewRuntimeClient for runtime operations, and
// NewGatewayClient for authenticated invocation operations.
package musereelsdk
