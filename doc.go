// Package musereelsdk is the public Go SDK boundary for controlled MuseReel
// workbench instances.
//
// SDK-002 adds the authentication and transport foundation: mTLS loading and
// rotation, short-lived runtime tokens, actor assertions, and the temporary
// hand-written wire codec for ExchangeRuntimeToken. The codec is deliberately
// isolated and is replaced or equivalently asserted when SDK-004 codegen
// lands; it does not expose protowire details in the public API.
package musereelsdk
