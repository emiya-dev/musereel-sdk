package musereelsdk

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"net"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	runtimepb "github.com/emiya-dev/musereel-sdk/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

func TestCachedTokenSourceRefreshWindowAndInvalidation(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	var calls atomic.Int32
	source := NewCachedTokenSource(func(context.Context) (Token, error) {
		call := calls.Add(1)
		return NewToken(fmt.Sprintf("token-%d", call), "Bearer", now.Add(5*time.Minute))
	}, WithClock(func() time.Time { return now }))
	first, err := source.Token(context.Background())
	if err != nil {
		t.Fatalf("first Token: %v", err)
	}
	if first.AccessToken() != "token-1" {
		t.Fatalf("first token = %q", first.AccessToken())
	}
	now = now.Add(239 * time.Second)
	if _, err := source.Token(context.Background()); err != nil {
		t.Fatalf("Token before 60-second threshold: %v", err)
	}
	if got := calls.Load(); got != 1 {
		t.Fatalf("exchange calls before threshold = %d, want 1", got)
	}
	now = now.Add(time.Second)
	second, err := source.Token(context.Background())
	if err != nil {
		t.Fatalf("Token at 60-second threshold: %v", err)
	}
	if second.AccessToken() != "token-2" || calls.Load() != 2 {
		t.Fatalf("refresh token/calls = %q/%d, want token-2/2", second.AccessToken(), calls.Load())
	}
	source.Invalidate()
	third, err := source.Token(context.Background())
	if err != nil {
		t.Fatalf("Token after invalidation: %v", err)
	}
	if third.AccessToken() != "token-3" || calls.Load() != 3 {
		t.Fatalf("invalidation token/calls = %q/%d, want token-3/3", third.AccessToken(), calls.Load())
	}
}

func TestCachedTokenSourceSingleFlight(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	entered := make(chan struct{})
	release := make(chan struct{})
	var calls atomic.Int32
	source := NewCachedTokenSource(func(context.Context) (Token, error) {
		if calls.Add(1) == 1 {
			close(entered)
			<-release
		}
		return NewToken("single-flight-token", "Bearer", now.Add(5*time.Minute))
	}, WithClock(func() time.Time { return now }))

	const callers = 24
	results := make(chan error, callers)
	var wait sync.WaitGroup
	wait.Add(callers)
	for index := 0; index < callers; index++ {
		go func() {
			defer wait.Done()
			_, err := source.Token(context.Background())
			results <- err
		}()
	}
	select {
	case <-entered:
	case <-time.After(2 * time.Second):
		t.Fatal("token exchange did not start")
	}
	close(release)
	wait.Wait()
	close(results)
	for err := range results {
		if err != nil {
			t.Errorf("single-flight caller error: %v", err)
		}
	}
	if calls.Load() != 1 {
		t.Fatalf("exchange calls = %d, want 1", calls.Load())
	}
}

type tokenRPCService struct {
	runtimepb.UnimplementedRuntimeServiceServer
	mu       sync.Mutex
	calls    int
	expiryMS int64
}

func (service *tokenRPCService) ExchangeRuntimeToken(_ context.Context, request *runtimepb.ExchangeRuntimeTokenRequest) (*runtimepb.ExchangeRuntimeTokenReply, error) {
	if request == nil {
		return nil, status.Error(codes.InvalidArgument, "request is nil")
	}
	service.mu.Lock()
	defer service.mu.Unlock()
	service.calls++
	return &runtimepb.ExchangeRuntimeTokenReply{
		RequestId:        fmt.Sprintf("request-%d", service.calls),
		AccessToken:      fmt.Sprintf("grpc-token-%d", service.calls),
		TokenType:        "Bearer",
		ExpiresInSeconds: 300,
		ExpiresAtMs:      service.expiryMS,
	}, nil
}

func TestGRPCTokenSourceUsesGeneratedEmptyRequest(t *testing.T) {
	listener := bufconn.Listen(1024 * 1024)
	service := &tokenRPCService{expiryMS: time.Unix(1_800_000_300, 0).UnixMilli()}
	server := grpc.NewServer()
	server.RegisterService(&runtimepb.RuntimeService_ServiceDesc, service)
	go func() { _ = server.Serve(listener) }()
	t.Cleanup(func() {
		server.Stop()
		_ = listener.Close()
	})

	connection, err := grpc.DialContext(
		context.Background(),
		"bufnet",
		grpc.WithContextDialer(func(context.Context, string) (net.Conn, error) { return listener.Dial() }),
		grpc.WithInsecure(),
	)
	if err != nil {
		t.Fatalf("DialContext: %v", err)
	}
	defer connection.Close()
	now := time.Unix(1_800_000_000, 0)
	source := NewGRPCTokenSource(connection, WithClock(func() time.Time { return now }))
	token, err := source.Token(context.Background())
	if err != nil {
		t.Fatalf("GRPCTokenSource.Token: %v", err)
	}
	if token.AccessToken() != "grpc-token-1" || token.RequestID() != "request-1" {
		t.Fatalf("token = %q request = %q", token.AccessToken(), token.RequestID())
	}
	service.mu.Lock()
	calls := service.calls
	service.mu.Unlock()
	if calls != 1 {
		t.Fatalf("server calls = %d, want 1", calls)
	}
}

type fakeConn struct {
	mu       sync.Mutex
	calls    int
	auth     []string
	requests []any
}

func (connection *fakeConn) Invoke(ctx context.Context, _ string, args, _ any, _ ...grpc.CallOption) error {
	connection.mu.Lock()
	defer connection.mu.Unlock()
	connection.calls++
	if values, ok := metadata.FromOutgoingContext(ctx); ok {
		connection.auth = append(connection.auth, values.Get("authorization")[0])
	}
	connection.requests = append(connection.requests, args)
	if connection.calls == 1 {
		return runtimeUnauthenticatedStatus()
	}
	return nil
}

func (connection *fakeConn) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, errors.New("not implemented")
}

type authenticatedRetryBufconnServer struct {
	runtimepb.UnimplementedRuntimeServiceServer

	mu            sync.Mutex
	calls         int
	authorization []string
}

func (server *authenticatedRetryBufconnServer) begin(ctx context.Context) bool {
	authorization := ""
	if values, ok := metadata.FromIncomingContext(ctx); ok {
		if headers := values.Get("authorization"); len(headers) > 0 {
			authorization = headers[0]
		}
	}
	server.mu.Lock()
	defer server.mu.Unlock()
	server.calls++
	server.authorization = append(server.authorization, authorization)
	return server.calls == 1
}

func (server *authenticatedRetryBufconnServer) GetOrder(ctx context.Context, request *runtimepb.GetOrderRequest) (*runtimepb.GetOrderReply, error) {
	if server.begin(ctx) {
		return nil, runtimeUnauthenticatedStatus()
	}
	return &runtimepb.GetOrderReply{Order: &runtimepb.Order{Id: request.GetOrderId()}}, nil
}

func (server *authenticatedRetryBufconnServer) GetBalance(ctx context.Context, _ *runtimepb.GetBalanceRequest) (*runtimepb.BalanceReply, error) {
	if server.begin(ctx) {
		return nil, runtimeUnauthenticatedStatus()
	}
	return &runtimepb.BalanceReply{AvailableUnits: "0001.0000"}, nil
}

func TestAuthenticatedInvokeRefreshesOnRuntimeDetailsOverBufconn(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	tokens, exchanges := runtimeTestTokens(now)
	server := &authenticatedRetryBufconnServer{}
	client := NewAuthenticatedClient(newRuntimeBufconn(t, server), tokens)
	reply := new(runtimepb.GetOrderReply)

	err := client.Invoke(
		context.Background(),
		runtimepb.RuntimeService_GetOrder_FullMethodName,
		&runtimepb.GetOrderRequest{OrderId: "order-01"},
		reply,
	)
	if err != nil {
		t.Fatalf("AuthenticatedClient.Invoke: %v", err)
	}
	if reply.GetOrder().GetId() != "order-01" {
		t.Fatalf("reply order id = %q, want order-01", reply.GetOrder().GetId())
	}
	server.mu.Lock()
	calls := server.calls
	authorization := append([]string(nil), server.authorization...)
	server.mu.Unlock()
	if calls != 2 || exchanges.Load() != 2 || len(authorization) != 2 || authorization[0] == authorization[1] {
		t.Fatalf("refresh evidence: server_calls=%d token_exchanges=%d authorization=%#v", calls, exchanges.Load(), authorization)
	}
	t.Logf("refresh trigger: server_calls=%d token_exchanges=%d authorization_rotated=%t", calls, exchanges.Load(), authorization[0] != authorization[1])
}

func TestAuthenticatedInvokeWithAssertionRefreshesOnRuntimeDetailsOverBufconn(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	tokens, exchanges := runtimeTestTokens(now)
	server := &authenticatedRetryBufconnServer{}
	client := NewAuthenticatedClient(newRuntimeBufconn(t, server), tokens)
	reply := new(runtimepb.BalanceReply)

	err := client.InvokeWithAssertion(
		context.Background(),
		runtimepb.RuntimeService_GetBalance_FullMethodName,
		AssertionCall{
			Args:           &runtimepb.GetBalanceRequest{Actor: "actor-01"},
			Sign:           func(Token) (JWS, error) { return JWS{}, nil },
			ApplyAssertion: func(any, JWS) error { return nil },
		},
		reply,
	)
	if err != nil {
		t.Fatalf("AuthenticatedClient.InvokeWithAssertion: %v", err)
	}
	if reply.GetAvailableUnits() != "0001.0000" {
		t.Fatalf("available units = %q, want 0001.0000", reply.GetAvailableUnits())
	}
	server.mu.Lock()
	calls := server.calls
	authorization := append([]string(nil), server.authorization...)
	server.mu.Unlock()
	if calls != 2 || exchanges.Load() != 2 || len(authorization) != 2 || authorization[0] == authorization[1] {
		t.Fatalf("refresh evidence: server_calls=%d token_exchanges=%d authorization=%#v", calls, exchanges.Load(), authorization)
	}
	t.Logf("refresh trigger: server_calls=%d token_exchanges=%d authorization_rotated=%t", calls, exchanges.Load(), authorization[0] != authorization[1])
}

type assertionTestRequest struct {
	IdempotencyKey string
	Fingerprint    string
	Assertion      string
}

func TestAuthenticatedRetryRefreshesAssertionButPreservesBusinessIdentity(t *testing.T) {
	now := time.Unix(1_800_000_000, 0)
	var exchanges atomic.Int32
	source := NewCachedTokenSource(func(context.Context) (Token, error) {
		call := exchanges.Add(1)
		return NewToken(fmt.Sprintf("auth-token-%d", call), "Bearer", now.Add(5*time.Minute))
	}, WithClock(func() time.Time { return now }))
	connection := &fakeConn{}
	client := NewAuthenticatedClient(connection, source)
	input := assertionInputForTest(t)
	input.Operation = "balance:get"
	input.Method = "POST"
	input.CanonicalPath = "/runtime.v1.RuntimeService/GetBalance"
	input.IdempotencyKey = ""
	fingerprint, err := RequestFingerprint(input.Method, input.CanonicalPath, input.Actor, input.IdempotencyKey, input.Body)
	if err != nil {
		t.Fatalf("RequestFingerprint: %v", err)
	}
	request := &assertionTestRequest{IdempotencyKey: input.IdempotencyKey, Fingerprint: fingerprint}
	signer, err := NewEd25519Signer("kid-retry", ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)))
	if err != nil {
		t.Fatalf("NewEd25519Signer: %v", err)
	}
	var assertions []JWS
	err = client.InvokeWithAssertion(context.Background(), "/runtime.v1.RuntimeService/GetBalance", AssertionCall{
		Args:               request,
		IdempotencyKey:     request.IdempotencyKey,
		RequestFingerprint: request.Fingerprint,
		Sign: func(Token) (JWS, error) {
			jws, _, err := SignAssertion(signer, input)
			if err == nil {
				assertions = append(assertions, jws)
			}
			return jws, err
		},
		ApplyAssertion: func(args any, assertion JWS) error {
			args.(*assertionTestRequest).Assertion = assertion.Compact()
			return nil
		},
		ReadIdentity: func(args any) (string, string, error) {
			typed := args.(*assertionTestRequest)
			return typed.IdempotencyKey, typed.Fingerprint, nil
		},
	}, nil)
	if err != nil {
		t.Fatalf("InvokeWithAssertion: %v", err)
	}
	if exchanges.Load() != 2 || len(assertions) != 2 {
		t.Fatalf("exchanges/assertions = %d/%d, want 2/2", exchanges.Load(), len(assertions))
	}
	firstClaims, err := VerifyJWS(assertions[0], signerPublicKey(signer))
	if err != nil {
		t.Fatalf("verify first assertion: %v", err)
	}
	secondClaims, err := VerifyJWS(assertions[1], signerPublicKey(signer))
	if err != nil {
		t.Fatalf("verify second assertion: %v", err)
	}
	if firstClaims.RequestFingerprint != secondClaims.RequestFingerprint || firstClaims.Nonce == secondClaims.Nonce {
		t.Fatalf("retry claims changed fingerprint or reused nonce")
	}
	if request.IdempotencyKey != input.IdempotencyKey || request.Fingerprint != fingerprint {
		t.Fatalf("retry changed business identity: %#v", request)
	}
	connection.mu.Lock()
	defer connection.mu.Unlock()
	if connection.calls != 2 || connection.auth[0] == connection.auth[1] {
		t.Fatalf("calls/auth = %d/%#v, want two calls with refreshed auth", connection.calls, connection.auth)
	}
	if connection.requests[0] != connection.requests[1] {
		t.Fatal("retry did not pass the same request object")
	}
}

func signerPublicKey(signer *Ed25519Signer) ed25519.PublicKey {
	return signer.key.Public().(ed25519.PublicKey)
}
