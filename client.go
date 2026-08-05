package musereelsdk

import (
	"context"
	"fmt"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// AuthenticatedClient adds a Bearer token to generic unary grpc calls and
// retries exactly once after the stable runtime_unauthenticated code. It does
// not inspect or rewrite business request fields.
type AuthenticatedClient struct {
	connection grpc.ClientConnInterface
	tokens     TokenSource
}

// NewAuthenticatedClient constructs a generic authenticated unary caller.
func NewAuthenticatedClient(connection grpc.ClientConnInterface, tokens TokenSource) *AuthenticatedClient {
	return &AuthenticatedClient{connection: connection, tokens: tokens}
}

// Invoke calls method with a Bearer token. The same args and reply values are
// passed to the one allowed retry, preserving caller-owned idempotency keys
// and request fingerprints.
func (client *AuthenticatedClient) Invoke(ctx context.Context, method string, args, reply any, options ...grpc.CallOption) error {
	if client == nil || client.connection == nil || client.tokens == nil {
		return fmt.Errorf("authenticated grpc client is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for attempt := 0; attempt < 2; attempt++ {
		token, err := client.tokens.Token(ctx)
		if err != nil {
			return err
		}
		callContext := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token.AccessToken())
		err = client.connection.Invoke(callContext, method, args, reply, options...)
		if !IsRuntimeUnauthenticated(err) {
			return err
		}
		if attempt == 1 {
			return err
		}
		invalidator, ok := client.tokens.(TokenInvalidator)
		if !ok {
			return err
		}
		invalidator.Invalidate()
	}
	return nil
}

// AssertionCall describes the small amount of request-specific behavior
// needed when a retry must receive a fresh nonce. ApplyAssertion changes only
// the actor_assertion field in Args. ReadIdentity is an optional negative
// control: when supplied, the client proves that idempotency key and request
// fingerprint did not change across the refresh retry.
type AssertionCall struct {
	Args               any
	IdempotencyKey     string
	RequestFingerprint string
	Sign               func(Token) (JWS, error)
	ApplyAssertion     func(args any, assertion JWS) error
	ReadIdentity       func(args any) (idempotencyKey, requestFingerprint string, err error)
}

// InvokeWithAssertion is the assertion-aware form of Invoke. It signs once
// per attempt, so a token-refresh retry gets a new nonce while the supplied
// business identity remains fixed.
func (client *AuthenticatedClient) InvokeWithAssertion(ctx context.Context, method string, call AssertionCall, reply any, options ...grpc.CallOption) error {
	if client == nil || client.connection == nil || client.tokens == nil {
		return fmt.Errorf("authenticated grpc client is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	if call.Sign == nil || call.ApplyAssertion == nil {
		return fmt.Errorf("assertion call is not configured")
	}
	if call.ReadIdentity != nil {
		key, fingerprint, err := call.ReadIdentity(call.Args)
		if err != nil {
			return err
		}
		if key != call.IdempotencyKey || fingerprint != call.RequestFingerprint {
			return fmt.Errorf("assertion call identity changed before invocation")
		}
	}
	for attempt := 0; attempt < 2; attempt++ {
		token, err := client.tokens.Token(ctx)
		if err != nil {
			return err
		}
		assertion, err := call.Sign(token)
		if err != nil {
			return err
		}
		if err := call.ApplyAssertion(call.Args, assertion); err != nil {
			return err
		}
		if call.ReadIdentity != nil {
			key, fingerprint, err := call.ReadIdentity(call.Args)
			if err != nil {
				return err
			}
			if key != call.IdempotencyKey || fingerprint != call.RequestFingerprint {
				return fmt.Errorf("assertion call identity changed during invocation")
			}
		}
		callContext := metadata.AppendToOutgoingContext(ctx, "authorization", "Bearer "+token.AccessToken())
		err = client.connection.Invoke(callContext, method, call.Args, reply, options...)
		if !IsRuntimeUnauthenticated(err) {
			return err
		}
		if attempt == 1 {
			return err
		}
		invalidator, ok := client.tokens.(TokenInvalidator)
		if !ok {
			return err
		}
		invalidator.Invalidate()
	}
	return nil
}
