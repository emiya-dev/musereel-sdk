package musereelsdk

import (
	"context"
	"encoding/json"
	"fmt"
	"sync"
	"time"

	runtimepb "github.com/emiya-dev/musereel-sdk/runtime"
	"google.golang.org/grpc"
)

const (
	runtimeTokenMethod = "/runtime.v1.RuntimeService/ExchangeRuntimeToken"
	refreshBefore      = 60 * time.Second
	fixedTokenLifetime = 300 * time.Second
)

// TokenSource supplies a currently usable runtime token.
type TokenSource interface {
	Token(context.Context) (Token, error)
}

// TokenInvalidator is implemented by SDK token sources whose cache can be
// discarded after a stable runtime_unauthenticated response.
type TokenInvalidator interface {
	Invalidate()
}

// Clock is injectable for deterministic token lifetime tests and callers with
// a controlled time source.
type Clock func() time.Time

func systemClock() time.Time { return time.Now() }

type tokenSourceConfig struct {
	now Clock
}

// TokenSourceOption customizes local token-source behavior without changing
// the wire request. In particular, there is no tenant, instance, or scope
// field here because ExchangeRuntimeToken is intentionally an empty message.
type TokenSourceOption func(*tokenSourceConfig)

// WithClock injects the clock used for cache expiry decisions.
func WithClock(now Clock) TokenSourceOption {
	return func(config *tokenSourceConfig) {
		if now != nil {
			config.now = now
		}
	}
}

// Token is an opaque short-lived runtime access token. The raw token can only
// be obtained through the explicitly named AccessToken method; default
// formatting and JSON encoding are redacted.
type Token struct {
	accessToken SecretString
	tokenType   string
	requestID   string
	expiresAt   time.Time
}

// NewToken constructs a token for custom TokenSource implementations. It does
// not accept or encode tenant, instance, or scope data.
func NewToken(accessToken, tokenType string, expiresAt time.Time) (Token, error) {
	if accessToken == "" {
		return Token{}, fmt.Errorf("runtime token is empty")
	}
	if tokenType != "Bearer" {
		return Token{}, fmt.Errorf("runtime token type is not Bearer")
	}
	if expiresAt.IsZero() {
		return Token{}, fmt.Errorf("runtime token expiry is missing")
	}
	return Token{
		accessToken: newSecretString(accessToken),
		tokenType:   tokenType,
		expiresAt:   expiresAt,
	}, nil
}

// AccessToken reveals the opaque token for an Authorization header.
func (token Token) AccessToken() string { return token.accessToken.Reveal() }

// TokenType returns the protocol token type. Valid SDK tokens always return
// Bearer.
func (token Token) TokenType() string { return token.tokenType }

// ExpiresAt returns the server-provided expiry instant.
func (token Token) ExpiresAt() time.Time { return token.expiresAt }

// RequestID returns the exchange request identifier, when the server sent one.
func (token Token) RequestID() string { return token.requestID }

func (token Token) usableAt(now time.Time) bool {
	return !token.accessToken.isEmpty() && now.Before(token.expiresAt.Add(-refreshBefore))
}

func (token Token) String() string { return redactedText }

func (token Token) GoString() string { return redactedText }

func (token Token) Format(state fmt.State, verb rune) {
	_, _ = state.Write([]byte(redactedText))
}

func (token Token) MarshalJSON() ([]byte, error) {
	return json.Marshal(struct {
		AccessToken string    `json:"access_token"`
		TokenType   string    `json:"token_type"`
		RequestID   string    `json:"request_id,omitempty"`
		ExpiresAt   time.Time `json:"expires_at"`
	}{
		AccessToken: redactedText,
		TokenType:   token.tokenType,
		RequestID:   token.requestID,
		ExpiresAt:   token.expiresAt,
	})
}

// CachedTokenSource provides lazy refresh, a fixed 60-second refresh window,
// and single-flight exchange behavior for any exchange function.
type CachedTokenSource struct {
	exchange TokenExchangeFunc
	now      Clock

	mu         sync.Mutex
	cached     Token
	refreshing *tokenRefresh
}

// TokenExchangeFunc exchanges the bootstrap mTLS identity for a token.
type TokenExchangeFunc func(context.Context) (Token, error)

type tokenRefresh struct {
	done  chan struct{}
	token Token
	err   error
}

// NewCachedTokenSource constructs a cache around an exchange function.
func NewCachedTokenSource(exchange TokenExchangeFunc, options ...TokenSourceOption) *CachedTokenSource {
	config := tokenSourceConfig{now: systemClock}
	for _, option := range options {
		if option != nil {
			option(&config)
		}
	}
	return &CachedTokenSource{exchange: exchange, now: config.now}
}

// Token returns the cached token when more than 60 seconds remain. At or
// below the threshold, exactly one caller exchanges while other callers wait
// for that same result.
func (source *CachedTokenSource) Token(ctx context.Context) (Token, error) {
	if source == nil || source.exchange == nil {
		return Token{}, fmt.Errorf("runtime token source is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	for {
		now := source.now()
		source.mu.Lock()
		if source.cached.usableAt(now) {
			token := source.cached
			source.mu.Unlock()
			return token, nil
		}
		if refresh := source.refreshing; refresh != nil {
			source.mu.Unlock()
			select {
			case <-refresh.done:
				if refresh.err != nil {
					return Token{}, refresh.err
				}
				return refresh.token, nil
			case <-ctx.Done():
				return Token{}, ctx.Err()
			}
		}
		refresh := &tokenRefresh{done: make(chan struct{})}
		source.refreshing = refresh
		source.mu.Unlock()

		token, err := source.exchange(ctx)
		source.mu.Lock()
		refresh.token = token
		refresh.err = err
		if err == nil {
			source.cached = token
		}
		close(refresh.done)
		source.refreshing = nil
		source.mu.Unlock()
		return token, err
	}
}

// Invalidate discards the cached token. An exchange already in flight is not
// cancelled; its result is still the single-flight result for waiting callers.
func (source *CachedTokenSource) Invalidate() {
	if source == nil {
		return
	}
	source.mu.Lock()
	source.cached = Token{}
	source.mu.Unlock()
}

// GRPCTokenSource exchanges the empty runtime token request through the
// generated protobuf client message, then applies CachedTokenSource lifetime
// and single-flight semantics.
type GRPCTokenSource struct {
	cache *CachedTokenSource
}

// NewGRPCTokenSource constructs a token source backed by RuntimeService.
func NewGRPCTokenSource(connection grpc.ClientConnInterface, options ...TokenSourceOption) *GRPCTokenSource {
	var config tokenSourceConfig
	config.now = systemClock
	for _, option := range options {
		if option != nil {
			option(&config)
		}
	}
	cache := NewCachedTokenSource(func(ctx context.Context) (Token, error) {
		return exchangeRuntimeToken(ctx, connection, config.now())
	}, WithClock(config.now))
	return &GRPCTokenSource{cache: cache}
}

func exchangeRuntimeToken(ctx context.Context, connection grpc.ClientConnInterface, now time.Time) (Token, error) {
	if connection == nil {
		return Token{}, fmt.Errorf("runtime grpc connection is not configured")
	}
	request := &runtimepb.ExchangeRuntimeTokenRequest{}
	reply := &runtimepb.ExchangeRuntimeTokenReply{}
	if err := connection.Invoke(ctx, runtimeTokenMethod, request, reply); err != nil {
		return Token{}, fmt.Errorf("exchange runtime token: %w", err)
	}
	if reply.AccessToken == "" {
		return Token{}, fmt.Errorf("exchange runtime token returned an empty token")
	}
	if reply.TokenType != "Bearer" {
		return Token{}, fmt.Errorf("exchange runtime token returned a non-Bearer token type")
	}
	if reply.ExpiresInSeconds != 0 && reply.ExpiresInSeconds != int64(fixedTokenLifetime/time.Second) {
		return Token{}, fmt.Errorf("exchange runtime token returned an invalid lifetime")
	}
	expiresAt := time.Time{}
	if reply.ExpiresAtMs != 0 {
		expiresAt = time.UnixMilli(reply.ExpiresAtMs)
	} else if reply.ExpiresInSeconds > 0 {
		expiresAt = now.Add(time.Duration(reply.ExpiresInSeconds) * time.Second)
	}
	if expiresAt.IsZero() {
		return Token{}, fmt.Errorf("exchange runtime token returned no expiry")
	}
	if !expiresAt.After(now) {
		return Token{}, fmt.Errorf("exchange runtime token returned an expired token")
	}
	return Token{
		accessToken: newSecretString(reply.AccessToken),
		tokenType:   reply.TokenType,
		requestID:   reply.RequestId,
		expiresAt:   expiresAt,
	}, nil
}

// Token delegates to the cached gRPC source.
func (source *GRPCTokenSource) Token(ctx context.Context) (Token, error) {
	if source == nil || source.cache == nil {
		return Token{}, fmt.Errorf("runtime grpc token source is not configured")
	}
	return source.cache.Token(ctx)
}

// Invalidate discards the gRPC source's cached token.
func (source *GRPCTokenSource) Invalidate() {
	if source != nil && source.cache != nil {
		source.cache.Invalidate()
	}
}
