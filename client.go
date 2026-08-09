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

// assertNoOutgoingAuthorization 在挂 Bearer 之前确认调用方没有自己先挂一个。
//
// 为什么必须拦在本地：metadata.AppendToOutgoingContext 是**追加不是替换**，
// 调用方已经放了 authorization 时请求会带两个值；而中枢要求恰好一个
// （runtime rpc 的认证拦截器：len(values) != 1 即视为「没有 Bearer」）。
// 于是服务端给出的错误是「未认证」或「注册请求无效」——**两种都不指向真正的原因**，
// 排查的人会去查 token、查 mTLS、查注册夹具，唯独不会想到是头重复了。
//
// 实测代价：SDK-007 修好 ResolveRegistration 之后，彩排里那段早年为绕开
// 该缺陷而手工挂 Bearer 的代码就与 SDK 自己挂的叠在一起，
// golden 六条腿全挂在 invalid_registration 上，其中三条是与本次改动
// 完全无关的 text / moderation / image。
//
// ⚠ 这里**不做「调用方的优先」**：那会让一个过期的手工 token 静默覆盖
// SDK 刚换来的新 token，把认证问题变成偶发问题。宁可当场报错。
func assertNoOutgoingAuthorization(ctx context.Context) error {
	values, ok := metadata.FromOutgoingContext(ctx)
	if !ok {
		return nil
	}
	if len(values.Get("authorization")) == 0 {
		return nil
	}
	return fmt.Errorf(
		"authorization is already set on the outgoing context; " +
			"musereel-sdk attaches the Bearer itself, remove the manual one",
	)
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
	if err := assertNoOutgoingAuthorization(ctx); err != nil {
		return err
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
	if err := assertNoOutgoingAuthorization(ctx); err != nil {
		return err
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
