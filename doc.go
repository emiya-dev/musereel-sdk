// Package musereelsdk is the public Go SDK boundary for controlled MuseReel
// workbench instances.
//
// SDK-002 adds the authentication and transport foundation: mTLS loading and
// rotation, short-lived runtime tokens, actor assertions, and the generic
// authenticated gRPC call boundary. SDK-004 adds generated runtime protobuf
// types and typed control-plane wrappers; ExchangeRuntimeToken uses the
// standard protobuf codec and is kept behind GRPCTokenSource.
package musereelsdk
