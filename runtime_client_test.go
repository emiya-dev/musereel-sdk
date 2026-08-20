package musereelsdk

import (
	"context"
	"crypto/ed25519"
	"errors"
	"fmt"
	"net"
	"strings"
	"sync"
	"sync/atomic"
	"testing"
	"time"

	runtimepb "github.com/emiya-dev/musereel-sdk/runtime"
	"google.golang.org/genproto/googleapis/rpc/errdetails"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/metadata"
	"google.golang.org/grpc/status"
	"google.golang.org/grpc/test/bufconn"
)

type runtimeBufconnServer struct {
	runtimepb.UnimplementedRuntimeServiceServer

	mu                sync.Mutex
	calls             map[string]int
	failFirst         map[string]bool
	resolveAuth       []string
	resolveDomains    []string
	confirmRequests   []*runtimepb.ConfirmRegistrationRequest
	createRequests    []*runtimepb.CreateOrderRequest
	paymentRequests   []*runtimepb.VerifyAndConfirmPaymentRequest
	balanceAssertions [][]byte
	ledgerAssertions  [][]byte
	ledgerPageCursors []string
	ledgerPageSizes   []int32
}

const (
	testRuntimeErrorDomain        = "sluice.runtime"
	testRuntimeUnauthenticatedMsg = "运行时请求未通过实例与操作者认证"
)

func runtimeErrorStatus(grpcCode codes.Code, message, reason, domain string, metadata map[string]string) error {
	result, err := status.New(grpcCode, message).WithDetails(&errdetails.ErrorInfo{
		Reason:   reason,
		Domain:   domain,
		Metadata: metadata,
	})
	if err != nil {
		panic(err)
	}
	return result.Err()
}

func runtimeUnauthenticatedStatus() error {
	return runtimeErrorStatus(
		codes.Unauthenticated,
		testRuntimeUnauthenticatedMsg,
		RuntimeUnauthenticated,
		testRuntimeErrorDomain,
		map[string]string{"retryable": "false"},
	)
}

func newRuntimeBufconnServer() *runtimeBufconnServer {
	return &runtimeBufconnServer{
		calls:     make(map[string]int),
		failFirst: map[string]bool{"CreateOrder": true, "GetBalance": true, "ListLedger": true},
	}
}

func (server *runtimeBufconnServer) begin(method string) bool {
	server.mu.Lock()
	defer server.mu.Unlock()
	server.calls[method]++
	return server.failFirst[method] && server.calls[method] == 1
}

func (server *runtimeBufconnServer) authorization(ctx context.Context) string {
	values, ok := metadata.FromIncomingContext(ctx)
	if !ok {
		return ""
	}
	authorization := values.Get("authorization")
	if len(authorization) == 0 {
		return ""
	}
	return authorization[0]
}

func (server *runtimeBufconnServer) ExchangeRuntimeToken(context.Context, *runtimepb.ExchangeRuntimeTokenRequest) (*runtimepb.ExchangeRuntimeTokenReply, error) {
	return &runtimepb.ExchangeRuntimeTokenReply{
		RequestId:        "buf-token-request",
		AccessToken:      "buf-token",
		TokenType:        "Bearer",
		ExpiresInSeconds: 300,
		ExpiresAtMs:      time.Now().Add(5 * time.Minute).UnixMilli(),
	}, nil
}

func (server *runtimeBufconnServer) ResolveRegistration(ctx context.Context, request *runtimepb.ResolveRegistrationRequest) (*runtimepb.RegistrationIntent, error) {
	server.mu.Lock()
	server.resolveAuth = append(server.resolveAuth, server.authorization(ctx))
	server.resolveDomains = append(server.resolveDomains, request.GetDomain())
	server.mu.Unlock()
	return &runtimepb.RegistrationIntent{
		RequestId:                     "intent-request",
		RegistrationIntentToken:       "intent-token",
		RegistrationIntentFingerprint: "intent-fingerprint",
		ExpiresAtMs:                   1_800_000_600_000,
		InviteCodeRequired:            request.GetInviteCode() == "",
	}, nil
}

func (server *runtimeBufconnServer) ConfirmRegistration(_ context.Context, request *runtimepb.ConfirmRegistrationRequest) (*runtimepb.RegistrationReply, error) {
	server.mu.Lock()
	server.confirmRequests = append(server.confirmRequests, &runtimepb.ConfirmRegistrationRequest{
		Actor:                   request.GetActor(),
		RegistrationIntentToken: request.GetRegistrationIntentToken(),
		ActorAssertion:          append([]byte(nil), request.GetActorAssertion()...),
	})
	server.mu.Unlock()
	return &runtimepb.RegistrationReply{RequestId: "registration-request", IdentityId: "identity-01", IdentityStatus: "active", MembershipStatus: "active"}, nil
}

func (server *runtimeBufconnServer) CreateOrder(_ context.Context, request *runtimepb.CreateOrderRequest) (*runtimepb.CreateOrderReply, error) {
	server.mu.Lock()
	server.createRequests = append(server.createRequests, &runtimepb.CreateOrderRequest{
		IdempotencyKey: request.GetIdempotencyKey(),
		Actor:          request.GetActor(),
		OfferPriceId:   request.GetOfferPriceId(),
		ActorAssertion: append([]byte(nil), request.GetActorAssertion()...),
	})
	server.mu.Unlock()
	if server.begin("CreateOrder") {
		return nil, runtimeUnauthenticatedStatus()
	}
	return &runtimepb.CreateOrderReply{
		RequestId: "order-request", OrderId: "order-01", PayableAmount: "00012.3400", Currency: "USD", Status: "pending",
	}, nil
}

func (server *runtimeBufconnServer) VerifyAndConfirmPayment(_ context.Context, request *runtimepb.VerifyAndConfirmPaymentRequest) (*runtimepb.VerifyAndConfirmPaymentReply, error) {
	clonedHeaders := make(map[string]string, len(request.GetSignedHeaders()))
	for key, value := range request.GetSignedHeaders() {
		clonedHeaders[key] = value
	}
	server.mu.Lock()
	server.paymentRequests = append(server.paymentRequests, &runtimepb.VerifyAndConfirmPaymentRequest{
		IdempotencyKey:   request.GetIdempotencyKey(),
		OrderId:          request.GetOrderId(),
		Channel:          request.GetChannel(),
		SignedPayload:    append([]byte(nil), request.GetSignedPayload()...),
		SignedHeaders:    clonedHeaders,
		ProviderQueryRef: request.GetProviderQueryRef(),
	})
	server.mu.Unlock()
	return &runtimepb.VerifyAndConfirmPaymentReply{
		RequestId: "payment-request",
		Order:     &runtimepb.Order{Id: "order-01", PayableAmount: "00012.3400", PaidAmount: "00012.3400", Currency: "USD"},
		Grant:     &runtimepb.RetailGrant{Id: "grant-01", GrantedUnits: "0007.5000"},
	}, nil
}

func (server *runtimeBufconnServer) GetOrder(_ context.Context, request *runtimepb.GetOrderRequest) (*runtimepb.GetOrderReply, error) {
	return &runtimepb.GetOrderReply{Order: &runtimepb.Order{Id: request.GetOrderId(), PayableAmount: "00012.3400"}}, nil
}

func (server *runtimeBufconnServer) SyncIdentity(_ context.Context, request *runtimepb.SyncIdentityRequest) (*runtimepb.IdentityReply, error) {
	return &runtimepb.IdentityReply{EventId: request.GetEventId(), Actor: request.GetActor(), IdentityStatus: "active"}, nil
}

func (server *runtimeBufconnServer) SyncVerificationStatus(_ context.Context, request *runtimepb.SyncVerificationStatusRequest) (*runtimepb.IdentityReply, error) {
	return &runtimepb.IdentityReply{EventId: request.GetEventId(), Actor: request.GetActor(), VerificationStatus: "verified"}, nil
}

func (server *runtimeBufconnServer) DisableIdentity(_ context.Context, request *runtimepb.DisableIdentityRequest) (*runtimepb.IdentityReply, error) {
	return &runtimepb.IdentityReply{EventId: request.GetEventId(), Actor: request.GetActor(), IdentityStatus: "disabled"}, nil
}

func (server *runtimeBufconnServer) GetBalance(_ context.Context, request *runtimepb.GetBalanceRequest) (*runtimepb.BalanceReply, error) {
	server.mu.Lock()
	server.balanceAssertions = append(server.balanceAssertions, append([]byte(nil), request.GetActorAssertion()...))
	server.mu.Unlock()
	if server.begin("GetBalance") {
		return nil, runtimeUnauthenticatedStatus()
	}
	return &runtimepb.BalanceReply{PostedUnits: "00010.0000", HeldUnits: "0001.2500", AvailableUnits: "0008.7500", PaidAvailableUnits: "0008.0000", FreeAvailableUnits: "0000.7500"}, nil
}

func (server *runtimeBufconnServer) ListLedger(_ context.Context, request *runtimepb.ListLedgerRequest) (*runtimepb.LedgerReply, error) {
	server.mu.Lock()
	server.ledgerAssertions = append(server.ledgerAssertions, append([]byte(nil), request.GetActorAssertion()...))
	server.ledgerPageCursors = append(server.ledgerPageCursors, request.GetPageCursor())
	server.ledgerPageSizes = append(server.ledgerPageSizes, request.GetPageSize())
	server.mu.Unlock()
	if server.begin("ListLedger") {
		return nil, runtimeUnauthenticatedStatus()
	}
	return &runtimepb.LedgerReply{Entries: []*runtimepb.LedgerItem{{EntryId: "entry-01", Units: "0000.5000"}}, NextPageCursor: "next-opaque"}, nil
}

func (server *runtimeBufconnServer) GetSkuCatalog(context.Context, *runtimepb.GetSkuCatalogRequest) (*runtimepb.SkuCatalogReply, error) {
	return &runtimepb.SkuCatalogReply{
		CatalogVersion: "sku-v1",
		Skus:           []*runtimepb.SkuCatalogItem{{SkuId: "sku-01", Price: &runtimepb.SkuPublicPrice{Rule: &runtimepb.SkuPublicPrice_Flat{Flat: &runtimepb.FlatPrice{Units: "0000.1250"}}}}},
	}, nil
}

func (server *runtimeBufconnServer) GetOfferCatalog(context.Context, *runtimepb.GetOfferCatalogRequest) (*runtimepb.OfferCatalogReply, error) {
	return &runtimepb.OfferCatalogReply{
		CatalogVersion: "offer-v1",
		Offers:         []*runtimepb.OfferCatalogItem{{OfferId: "offer-01", GrantedUnits: "0007.5000", PayableAmount: "00012.3400", Currency: "USD"}},
	}, nil
}

type runtimeErrorBufconnServer struct {
	runtimepb.UnimplementedRuntimeServiceServer
	err   error
	calls atomic.Int32
}

func (server *runtimeErrorBufconnServer) ResolveRegistration(context.Context, *runtimepb.ResolveRegistrationRequest) (*runtimepb.RegistrationIntent, error) {
	server.calls.Add(1)
	return nil, server.err
}

func newRuntimeBufconn(t *testing.T, serverImpl runtimepb.RuntimeServiceServer) *grpc.ClientConn {
	t.Helper()
	listener := bufconn.Listen(1024 * 1024)
	server := grpc.NewServer()
	server.RegisterService(&runtimepb.RuntimeService_ServiceDesc, serverImpl)
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
	t.Cleanup(func() { _ = connection.Close() })
	return connection
}

func runtimeAssertionSigner(t *testing.T) *Ed25519Signer {
	t.Helper()
	signer, err := NewEd25519Signer("kid-runtime", ed25519.NewKeyFromSeed(make([]byte, ed25519.SeedSize)))
	if err != nil {
		t.Fatalf("NewEd25519Signer: %v", err)
	}
	return signer
}

func runtimeTestTokens(now time.Time) (*CachedTokenSource, *atomic.Int32) {
	var exchanges atomic.Int32
	source := NewCachedTokenSource(func(context.Context) (Token, error) {
		call := exchanges.Add(1)
		return NewToken(fmt.Sprintf("runtime-test-token-%d", call), "Bearer", now.Add(5*time.Minute))
	}, WithClock(func() time.Time { return now }))
	return source, &exchanges
}

func TestRuntimeClientBufconnContract(t *testing.T) {
	server := newRuntimeBufconnServer()
	connection := newRuntimeBufconn(t, server)
	signer := runtimeAssertionSigner(t)
	now := time.Unix(1_800_000_000, 0)
	tokens, exchanges := runtimeTestTokens(now)
	client := NewRuntimeClient(connection, tokens, WithRuntimeAssertion(signer, "instance-01", "tenant-01", "session-01"))

	const rawDomain = "Tenant.Example.TEST"
	if _, err := client.ResolveRegistration(context.Background(), &runtimepb.ResolveRegistrationRequest{Domain: rawDomain, InviteCode: "invite"}); err != nil {
		t.Fatalf("ResolveRegistration: %v", err)
	}
	intent := &runtimepb.RegistrationIntent{RegistrationIntentToken: "intent-token", RegistrationIntentFingerprint: "intent-fingerprint"}
	if _, err := client.ConfirmRegistration(context.Background(), &runtimepb.ConfirmRegistrationRequest{Actor: "actor-01", RegistrationIntentToken: intent.GetRegistrationIntentToken()}, intent); err != nil {
		t.Fatalf("ConfirmRegistration: %v", err)
	}
	if _, err := client.CreateOrder(context.Background(), &runtimepb.CreateOrderRequest{IdempotencyKey: "order-key", Actor: "actor-01", OfferPriceId: "offer-price-01"}); err != nil {
		t.Fatalf("CreateOrder: %v", err)
	}
	payload := []byte{0x00, 0xff, 0x70, 0x61, 0x79}
	if _, err := client.VerifyAndConfirmPayment(context.Background(), &runtimepb.VerifyAndConfirmPaymentRequest{
		IdempotencyKey: "payment-key", OrderId: "order-01", Channel: "provider-x", SignedPayload: payload,
		SignedHeaders: map[string]string{"x-signature": "raw-header"}, ProviderQueryRef: "query-ref/raw",
	}); err != nil {
		t.Fatalf("VerifyAndConfirmPayment: %v", err)
	}
	if _, err := client.GetBalance(context.Background(), &runtimepb.GetBalanceRequest{Actor: "actor-01"}); err != nil {
		t.Fatalf("GetBalance: %v", err)
	}
	if _, err := client.ListLedger(context.Background(), &runtimepb.ListLedgerRequest{Actor: "actor-01", PageCursor: "opaque.cursor/raw", PageSize: 0}); err != nil {
		t.Fatalf("ListLedger: %v", err)
	}
	if _, err := client.GetOrder(context.Background(), &runtimepb.GetOrderRequest{Actor: "actor-01", OrderId: "order-01"}); err != nil {
		t.Fatalf("GetOrder: %v", err)
	}
	validEvent := "event-identity-01"
	if _, err := client.SyncIdentity(context.Background(), &runtimepb.SyncIdentityRequest{EventId: validEvent, Actor: "actor-01"}); err != nil {
		t.Fatalf("SyncIdentity: %v", err)
	}
	if _, err := client.SyncVerificationStatus(context.Background(), &runtimepb.SyncVerificationStatusRequest{EventId: validEvent + "-v", Actor: "actor-01", Verified: true, CredentialRef: "cred-ref-01", Issuer: "issuer-01"}); err != nil {
		t.Fatalf("SyncVerificationStatus: %v", err)
	}
	if _, err := client.DisableIdentity(context.Background(), &runtimepb.DisableIdentityRequest{EventId: validEvent + "-d", Actor: "actor-01"}); err != nil {
		t.Fatalf("DisableIdentity: %v", err)
	}
	skuCatalog, err := client.GetSkuCatalog(context.Background())
	if err != nil {
		t.Fatalf("GetSkuCatalog: %v", err)
	}
	offerCatalog, err := client.GetOfferCatalog(context.Background())
	if err != nil {
		t.Fatalf("GetOfferCatalog: %v", err)
	}

	server.mu.Lock()
	defer server.mu.Unlock()
	if len(server.resolveAuth) != 1 || !strings.HasPrefix(server.resolveAuth[0], "Bearer ") {
		t.Fatalf("ResolveRegistration authorization = %#v, want a Bearer", server.resolveAuth)
	}
	if len(server.resolveDomains) != 1 || server.resolveDomains[0] != rawDomain {
		t.Fatalf("ResolveRegistration domain = %#v, want exact %q", server.resolveDomains, rawDomain)
	}
	if len(server.confirmRequests) != 1 {
		t.Fatalf("ConfirmRegistration calls = %d, want 1", len(server.confirmRequests))
	}
	confirmClaims := verifyRuntimeAssertion(t, signer, server.confirmRequests[0].GetActorAssertion())
	// 这里刻意写死 "GRPC" 而不是引用 runtimeAssertionMethod：引用常量会让本断言变成
	// 实现的镜子，method 换成任何值都能自洽通过——这正是它当初没能抓到 "POST" 的原因。
	// 写死字面量才能在有人改动那个常量时当场变红。
	wantConfirmFingerprint, err := RequestFingerprint("GRPC", runtimepb.RuntimeService_ConfirmRegistration_FullMethodName, "actor-01", "actor-01", []byte(`{"registration_intent_fingerprint":"intent-fingerprint"}`))
	if err != nil {
		t.Fatalf("RequestFingerprint(confirm): %v", err)
	}
	if confirmClaims.Operation != "registration:confirm" || confirmClaims.RequestFingerprint != wantConfirmFingerprint {
		t.Fatalf("confirm claims = %#v, want operation/fingerprint %q/%q", confirmClaims, "registration:confirm", wantConfirmFingerprint)
	}
	if bytesContain(server.confirmRequests[0].GetActorAssertion(), []byte("intent-token")) {
		t.Fatal("confirm assertion contains registration intent token")
	}
	if len(server.createRequests) != 2 {
		t.Fatalf("CreateOrder calls = %d, want refresh retry pair", len(server.createRequests))
	}
	for index, request := range server.createRequests {
		if request.GetIdempotencyKey() != "order-key" || request.GetActor() != "actor-01" || request.GetOfferPriceId() != "offer-price-01" {
			t.Fatalf("CreateOrder request %d changed business identity: %#v", index, request)
		}
	}
	createClaims := verifyRuntimeAssertion(t, signer, server.createRequests[0].GetActorAssertion())
	createRetryClaims := verifyRuntimeAssertion(t, signer, server.createRequests[1].GetActorAssertion())
	if createClaims.Operation != "order:create" || createClaims.RequestFingerprint != createRetryClaims.RequestFingerprint || createClaims.Nonce == createRetryClaims.Nonce {
		t.Fatalf("CreateOrder retry claims did not preserve identity and refresh nonce: %#v / %#v", createClaims, createRetryClaims)
	}
	// 同上：写死字面量，不引用常量。
	wantCreateFingerprint, err := RequestFingerprint("GRPC", runtimepb.RuntimeService_CreateOrder_FullMethodName, "actor-01", "order-key", []byte(`{"offer_price_id":"offer-price-01"}`))
	if err != nil {
		t.Fatalf("RequestFingerprint(create): %v", err)
	}
	if createClaims.RequestFingerprint != wantCreateFingerprint {
		t.Fatalf("CreateOrder fingerprint = %q, want %q", createClaims.RequestFingerprint, wantCreateFingerprint)
	}
	if len(server.paymentRequests) != 1 || string(server.paymentRequests[0].GetSignedPayload()) != string(payload) || server.paymentRequests[0].GetSignedHeaders()["x-signature"] != "raw-header" || server.paymentRequests[0].GetProviderQueryRef() != "query-ref/raw" {
		t.Fatalf("payment proof was not passed through byte-for-byte: %#v", server.paymentRequests)
	}
	if len(server.balanceAssertions) != 2 {
		t.Fatalf("GetBalance assertion calls = %d, want 2", len(server.balanceAssertions))
	}
	balanceFirst := verifyRuntimeAssertion(t, signer, server.balanceAssertions[0])
	balanceSecond := verifyRuntimeAssertion(t, signer, server.balanceAssertions[1])
	if balanceFirst.Operation != "balance:get" || balanceFirst.RequestFingerprint != balanceSecond.RequestFingerprint || balanceFirst.Nonce == balanceSecond.Nonce {
		t.Fatalf("GetBalance strict nonce claims = %#v / %#v", balanceFirst, balanceSecond)
	}
	if len(server.ledgerAssertions) != 2 || server.ledgerPageCursors[0] != "opaque.cursor/raw" || server.ledgerPageCursors[1] != "opaque.cursor/raw" || server.ledgerPageSizes[0] != 0 || server.ledgerPageSizes[1] != 0 {
		t.Fatalf("ListLedger pagination was not transparently forwarded: cursors=%#v sizes=%#v", server.ledgerPageCursors, server.ledgerPageSizes)
	}
	ledgerFirst := verifyRuntimeAssertion(t, signer, server.ledgerAssertions[0])
	ledgerSecond := verifyRuntimeAssertion(t, signer, server.ledgerAssertions[1])
	if ledgerFirst.Operation != "ledger:list" || ledgerFirst.RequestFingerprint != ledgerSecond.RequestFingerprint || ledgerFirst.Nonce == ledgerSecond.Nonce {
		t.Fatalf("ListLedger strict nonce claims = %#v / %#v", ledgerFirst, ledgerSecond)
	}
	if skuCatalog.GetSkus()[0].GetPrice().GetFlat().GetUnits() != "0000.1250" || offerCatalog.GetOffers()[0].GetPayableAmount() != "00012.3400" || offerCatalog.GetOffers()[0].GetGrantedUnits() != "0007.5000" {
		t.Fatalf("catalog string fields were changed: sku=%#v offer=%#v", skuCatalog, offerCatalog)
	}
	if exchanges.Load() != 4 {
		t.Fatalf("token exchanges = %d, want initial plus three refreshes", exchanges.Load())
	}
}

func TestRuntimeErrorInfoRoundTripsThroughBufconn(t *testing.T) {
	const stableCode = "invocation_idempotency_conflict"
	server := &runtimeErrorBufconnServer{
		err: runtimeErrorStatus(codes.Aborted, "调用幂等键与既有请求冲突", stableCode, testRuntimeErrorDomain, map[string]string{}),
	}
	tokens, _ := runtimeTestTokens(time.Unix(1_800_000_000, 0))
	client := NewRuntimeClient(newRuntimeBufconn(t, server), tokens)

	_, err := client.ResolveRegistration(context.Background(), &runtimepb.ResolveRegistrationRequest{})
	if err == nil {
		t.Fatal("ResolveRegistration unexpectedly succeeded")
	}
	if got := ErrorCode(err); got != stableCode {
		t.Fatalf("ErrorCode = %q, want %q; err=%v", got, stableCode, err)
	}
	if status.Code(err) != codes.Aborted {
		t.Fatalf("status.Code = %s, want %s", status.Code(err), codes.Aborted)
	}
	if server.calls.Load() != 1 {
		t.Fatalf("server calls = %d, want 1", server.calls.Load())
	}
}

func TestRuntimeAbortedDetailsRemainDistinct(t *testing.T) {
	testCases := []string{
		"invocation_idempotency_conflict",
		"payment_idempotency_conflict",
		"insufficient_quota",
	}
	for _, stableCode := range testCases {
		t.Run(stableCode, func(t *testing.T) {
			server := &runtimeErrorBufconnServer{
				err: runtimeErrorStatus(codes.Aborted, "业务请求被拒绝", stableCode, testRuntimeErrorDomain, map[string]string{}),
			}
			tokens, _ := runtimeTestTokens(time.Unix(1_800_000_000, 0))
			client := NewRuntimeClient(newRuntimeBufconn(t, server), tokens)

			_, err := client.ResolveRegistration(context.Background(), &runtimepb.ResolveRegistrationRequest{})
			if err == nil {
				t.Fatal("ResolveRegistration unexpectedly succeeded")
			}
			if got := ErrorCode(err); got != stableCode {
				t.Fatalf("ErrorCode = %q, want fixture code %q", got, stableCode)
			}
			if status.Code(err) != codes.Aborted {
				t.Fatalf("status.Code = %s, want %s", status.Code(err), codes.Aborted)
			}
		})
	}
}

func TestRuntimeRetryableMetadataHasThreeStates(t *testing.T) {
	testCases := []struct {
		name       string
		stableCode string
		grpcCode   codes.Code
		metadata   map[string]string
		want       bool
	}{
		{name: "explicit true", stableCode: "internal_error", grpcCode: codes.Internal, metadata: map[string]string{"retryable": "true"}, want: true},
		{name: "explicit false", stableCode: "runtime_forbidden", grpcCode: codes.PermissionDenied, metadata: map[string]string{"retryable": "false"}, want: false},
		{name: "unflagged insufficient quota", stableCode: "insufficient_quota", grpcCode: codes.Aborted, metadata: map[string]string{}, want: false},
		{name: "missing flag uses sdk fallback", stableCode: RuntimeRegistrationUnavailable, grpcCode: codes.Unavailable, metadata: map[string]string{}, want: true},
		{name: "registration code not found is never retryable", stableCode: RuntimeRegistrationCodeNotFound, grpcCode: codes.InvalidArgument, metadata: map[string]string{"retryable": "true"}, want: false},
		{name: "registration code merchant mismatch is never retryable", stableCode: RuntimeRegistrationCodeMerchantMismatch, grpcCode: codes.InvalidArgument, metadata: map[string]string{"retryable": "true"}, want: false},
		{name: "registration code invalid is never retryable", stableCode: RuntimeRegistrationCodeInvalid, grpcCode: codes.InvalidArgument, metadata: map[string]string{"retryable": "true"}, want: false},
		{name: "registration code expired is never retryable", stableCode: RuntimeRegistrationCodeExpired, grpcCode: codes.InvalidArgument, metadata: map[string]string{"retryable": "true"}, want: false},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			server := &runtimeErrorBufconnServer{
				err: runtimeErrorStatus(testCase.grpcCode, "业务错误", testCase.stableCode, testRuntimeErrorDomain, testCase.metadata),
			}
			tokens, _ := runtimeTestTokens(time.Unix(1_800_000_000, 0))
			client := NewRuntimeClient(newRuntimeBufconn(t, server), tokens)

			_, err := client.ResolveRegistration(context.Background(), &runtimepb.ResolveRegistrationRequest{})
			if err == nil {
				t.Fatal("ResolveRegistration unexpectedly succeeded")
			}
			retryable, ok := err.(interface{ Retryable() bool })
			if !ok {
				t.Fatalf("error type %T does not expose Retryable", err)
			}
			if got := retryable.Retryable(); got != testCase.want {
				t.Fatalf("Retryable = %t, want %t for metadata=%#v", got, testCase.want, testCase.metadata)
			}
		})
	}
}

func TestErrorCodeIgnoresForeignErrorInfoDomain(t *testing.T) {
	err := runtimeErrorStatus(
		codes.Unauthenticated,
		testRuntimeUnauthenticatedMsg,
		RuntimeUnauthenticated,
		"other.service",
		map[string]string{"retryable": "false"},
	)
	if got := ErrorCode(err); got != "" {
		t.Fatalf("foreign ErrorInfo domain produced code %q", got)
	}
}

func verifyRuntimeAssertion(t *testing.T, signer *Ed25519Signer, raw []byte) AssertionClaims {
	t.Helper()
	if len(raw) == 0 {
		t.Fatal("server received an empty actor assertion")
	}
	claims, err := VerifyCompactJWS(string(raw), signerPublicKey(signer))
	if err != nil {
		t.Fatalf("VerifyCompactJWS: %v", err)
	}
	return claims
}

func bytesContain(value, fragment []byte) bool {
	if len(fragment) == 0 {
		return true
	}
	for start := 0; start+len(fragment) <= len(value); start++ {
		if string(value[start:start+len(fragment)]) == string(fragment) {
			return true
		}
	}
	return false
}

type runtimeRoutingConn struct {
	mu      sync.Mutex
	methods []string
}

func (connection *runtimeRoutingConn) Invoke(_ context.Context, method string, _ any, reply any, _ ...grpc.CallOption) error {
	connection.mu.Lock()
	connection.methods = append(connection.methods, method)
	connection.mu.Unlock()
	switch typed := reply.(type) {
	case *runtimepb.ExchangeRuntimeTokenReply:
		typed.RequestId, typed.AccessToken, typed.TokenType, typed.ExpiresInSeconds, typed.ExpiresAtMs = "request", "token", "Bearer", 300, time.Unix(1_800_000_300, 0).UnixMilli()
	case *runtimepb.RegistrationIntent:
		typed.RegistrationIntentFingerprint = "fingerprint"
	case *runtimepb.RegistrationReply:
		typed.IdentityId = "identity"
	case *runtimepb.CreateOrderReply:
		typed.OrderId, typed.PayableAmount = "order", "0001.0000"
	case *runtimepb.VerifyAndConfirmPaymentReply:
		typed.RequestId = "payment"
	case *runtimepb.GetOrderReply:
		typed.Order = &runtimepb.Order{Id: "order"}
	case *runtimepb.IdentityReply:
		typed.IdentityId = "identity"
	case *runtimepb.BalanceReply:
		typed.AvailableUnits = "0001.0000"
	case *runtimepb.LedgerReply:
		typed.NextPageCursor = "opaque-next"
	case *runtimepb.SkuCatalogReply:
		typed.CatalogVersion = "sku"
	case *runtimepb.OfferCatalogReply:
		typed.CatalogVersion = "offer"
	}
	return nil
}

func (connection *runtimeRoutingConn) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, errors.New("not implemented")
}

func TestRuntimeClientRoutesEveryGeneratedRPCMethod(t *testing.T) {
	connection := &runtimeRoutingConn{}
	now := time.Unix(1_800_000_000, 0)
	tokens := NewGRPCTokenSource(connection, WithClock(func() time.Time { return now }))
	signer := runtimeAssertionSigner(t)
	client := NewRuntimeClient(connection, tokens, WithRuntimeAssertion(signer, "instance-01", "tenant-01", "session-01"))

	if _, err := tokens.Token(context.Background()); err != nil {
		t.Fatalf("ExchangeRuntimeToken route: %v", err)
	}
	intent := &runtimepb.RegistrationIntent{RegistrationIntentFingerprint: "fingerprint"}
	validEvent := "event-routing-01"
	calls := []func() error{
		func() error {
			_, err := client.ResolveRegistration(context.Background(), &runtimepb.ResolveRegistrationRequest{})
			return err
		},
		func() error {
			_, err := client.ConfirmRegistration(context.Background(), &runtimepb.ConfirmRegistrationRequest{Actor: "actor-01"}, intent)
			return err
		},
		func() error {
			_, err := client.CreateOrder(context.Background(), &runtimepb.CreateOrderRequest{IdempotencyKey: "order-key", Actor: "actor-01"})
			return err
		},
		func() error {
			_, err := client.VerifyAndConfirmPayment(context.Background(), &runtimepb.VerifyAndConfirmPaymentRequest{IdempotencyKey: "payment-key"})
			return err
		},
		func() error {
			_, err := client.GetOrder(context.Background(), &runtimepb.GetOrderRequest{Actor: "actor-01"})
			return err
		},
		func() error {
			_, err := client.SyncIdentity(context.Background(), &runtimepb.SyncIdentityRequest{EventId: validEvent, Actor: "actor-01"})
			return err
		},
		func() error {
			_, err := client.SyncVerificationStatus(context.Background(), &runtimepb.SyncVerificationStatusRequest{EventId: validEvent + "-v", Actor: "actor-01", CredentialRef: "credential", Issuer: "issuer"})
			return err
		},
		func() error {
			_, err := client.DisableIdentity(context.Background(), &runtimepb.DisableIdentityRequest{EventId: validEvent + "-d", Actor: "actor-01"})
			return err
		},
		func() error {
			_, err := client.GetBalance(context.Background(), &runtimepb.GetBalanceRequest{Actor: "actor-01"})
			return err
		},
		func() error {
			_, err := client.ListLedger(context.Background(), &runtimepb.ListLedgerRequest{Actor: "actor-01", PageSize: 1})
			return err
		},
		func() error { _, err := client.GetSkuCatalog(context.Background()); return err },
		func() error { _, err := client.GetOfferCatalog(context.Background()); return err },
	}
	for index, call := range calls {
		if err := call(); err != nil {
			t.Fatalf("route call %d: %v", index, err)
		}
	}
	want := []string{
		runtimepb.RuntimeService_ExchangeRuntimeToken_FullMethodName,
		runtimepb.RuntimeService_ResolveRegistration_FullMethodName,
		runtimepb.RuntimeService_ConfirmRegistration_FullMethodName,
		runtimepb.RuntimeService_CreateOrder_FullMethodName,
		runtimepb.RuntimeService_VerifyAndConfirmPayment_FullMethodName,
		runtimepb.RuntimeService_GetOrder_FullMethodName,
		runtimepb.RuntimeService_SyncIdentity_FullMethodName,
		runtimepb.RuntimeService_SyncVerificationStatus_FullMethodName,
		runtimepb.RuntimeService_DisableIdentity_FullMethodName,
		runtimepb.RuntimeService_GetBalance_FullMethodName,
		runtimepb.RuntimeService_ListLedger_FullMethodName,
		runtimepb.RuntimeService_GetSkuCatalog_FullMethodName,
		runtimepb.RuntimeService_GetOfferCatalog_FullMethodName,
	}
	connection.mu.Lock()
	defer connection.mu.Unlock()
	if len(connection.methods) != len(want) {
		t.Fatalf("routed methods = %#v, want %#v", connection.methods, want)
	}
	for index := range want {
		if connection.methods[index] != want[index] {
			t.Errorf("method %d = %q, want %q", index, connection.methods[index], want[index])
		}
	}
}

func TestRuntimeClientValidationNegativeControls(t *testing.T) {
	// ⚠ 这里**必须**用一个能正常应答的 connection，不能用 NewRuntimeClient(nil, nil)。
	// 原先用的是 nil connection：那样合法请求也会因为 connection 为空而报错，
	// 于是 `err == nil` 这个断言永远不成立——把 validateEventID /
	// validateCredentialRef / validateIssuer / validateActor 四个校验函数
	// 全部改成 `return nil`，整个测试照样绿（2026-08-20 实测）。
	// 一个名字里带 NegativeControls、却什么都不守的测试，比没有这条测试更糟：
	// 它让人以为字段校验有守卫。
	//
	// 现在的形态是：正控先证明「这个 conn 上合法请求确实能过」，
	// 负控才证明「非法字段确实被本地拒住」。缺了正控那一半，负控随时会退化回空断言。
	connection := &runtimeRoutingConn{}
	now := time.Unix(1_800_000_000, 0)
	tokens := NewGRPCTokenSource(connection, WithClock(func() time.Time { return now }))
	// ListLedger / GetBalance 走 actor assertion，缺 signer 会在字段校验之后被拒——
	// 正控第三条就是这么抓出来的，别把它换成不带 assertion 的 RPC。
	client := NewRuntimeClient(connection, tokens,
		WithRuntimeAssertion(runtimeAssertionSigner(t), "instance-01", "tenant-01", "session-01"))

	// 正控：合法字段必须通过，否则下面每条负控都会因为别的原因变绿。
	if _, err := client.SyncIdentity(context.Background(), &runtimepb.SyncIdentityRequest{
		EventId: "event-positive-01", Actor: "actor-01",
	}); err != nil {
		t.Fatalf("正控失败——合法 SyncIdentity 被拒，负控全部失去意义: %v", err)
	}
	if _, err := client.SyncVerificationStatus(context.Background(), &runtimepb.SyncVerificationStatusRequest{
		EventId: "event-positive-02", Actor: "actor-01", CredentialRef: "credential", Issuer: "issuer",
	}); err != nil {
		t.Fatalf("正控失败——合法 SyncVerificationStatus 被拒: %v", err)
	}
	if _, err := client.ListLedger(context.Background(), &runtimepb.ListLedgerRequest{
		Actor: "actor-01", PageSize: 100,
	}); err != nil {
		t.Fatalf("正控失败——合法 ListLedger 被拒: %v", err)
	}

	for _, testCase := range []struct {
		name string
		call func() error
	}{
		{"event_id 过短", func() error {
			_, err := client.SyncIdentity(context.Background(), &runtimepb.SyncIdentityRequest{EventId: "too-short", Actor: "actor-01"})
			return err
		}},
		{"event_id 含非法字符", func() error {
			_, err := client.SyncIdentity(context.Background(), &runtimepb.SyncIdentityRequest{EventId: "event-invalid-01!", Actor: "actor-01"})
			return err
		}},
		{"page_size 超过 100", func() error {
			_, err := client.ListLedger(context.Background(), &runtimepb.ListLedgerRequest{Actor: "actor-01", PageSize: 101})
			return err
		}},
		{"page_size 为负", func() error {
			_, err := client.ListLedger(context.Background(), &runtimepb.ListLedgerRequest{Actor: "actor-01", PageSize: -1})
			return err
		}},
		{"credential_ref 含空白", func() error {
			_, err := client.SyncVerificationStatus(context.Background(), &runtimepb.SyncVerificationStatusRequest{EventId: "event-credential-01", Actor: "actor-01", CredentialRef: "credential ref", Issuer: "issuer"})
			return err
		}},
		{"issuer 不是冻结的小写形态", func() error {
			_, err := client.SyncVerificationStatus(context.Background(), &runtimepb.SyncVerificationStatusRequest{EventId: "event-credential-02", Actor: "actor-01", CredentialRef: "credential", Issuer: "Issuer"})
			return err
		}},
		// 06:220-222 用同一句给这三个 identity RPC 定 event_id 与 actor 两条制式，
		// 表里三者都有 actor 字段。SDK 一直只校验了 event_id 那半句。
		{"SyncIdentity 的 actor 首尾有空白", func() error {
			_, err := client.SyncIdentity(context.Background(), &runtimepb.SyncIdentityRequest{EventId: "event-actor-01", Actor: " actor"})
			return err
		}},
		{"SyncVerificationStatus 的 actor 含控制字符", func() error {
			_, err := client.SyncVerificationStatus(context.Background(), &runtimepb.SyncVerificationStatusRequest{EventId: "event-actor-02", Actor: "act\x00or", CredentialRef: "credential", Issuer: "issuer"})
			return err
		}},
		{"DisableIdentity 的 actor 为空", func() error {
			_, err := client.DisableIdentity(context.Background(), &runtimepb.DisableIdentityRequest{EventId: "event-actor-03", Actor: ""})
			return err
		}},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			if err := testCase.call(); err == nil {
				t.Fatal("非法字段被接受了")
			}
		})
	}
}

func TestRuntimeActorValidationContract(t *testing.T) {
	testCases := []struct {
		name  string
		value string
		valid bool
	}{
		{name: "leading space", value: " a"},
		{name: "trailing space", value: "a "},
		{name: "leading tab", value: "\ta"},
		{name: "embedded NUL", value: "a\x00b"},
		{name: "embedded newline", value: "a\nb"},
		{name: "hash ID", value: strings.Repeat("a", 64), valid: true},
		{name: "Chinese", value: "用户-01", valid: true},
		{name: "emoji", value: "actor-😀", valid: true},
		{name: "256 bytes", value: strings.Repeat("a", 256), valid: true},
		{name: "257 bytes", value: strings.Repeat("a", 257)},
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			err := validateActor(testCase.value)
			if (err == nil) != testCase.valid {
				t.Fatalf("validateActor(%q) error = %v, valid = %t", testCase.value, err, testCase.valid)
			}
		})
	}
}

type runtimeErrorConn struct {
	err   error
	calls atomic.Int32
}

func (connection *runtimeErrorConn) Invoke(context.Context, string, any, any, ...grpc.CallOption) error {
	connection.calls.Add(1)
	return connection.err
}

func (connection *runtimeErrorConn) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, errors.New("not implemented")
}

func TestRuntimeStableErrorMappingDoesNotAddRetry(t *testing.T) {
	testCases := []struct {
		name          string
		err           error
		rawCode       string
		wantCode      string
		wantStatus    codes.Code
		wantRetryable bool
	}{
		{
			name:          "registration unavailable ErrorInfo details",
			rawCode:       RuntimeRegistrationUnavailable,
			wantCode:      RuntimeRegistrationUnavailable,
			wantStatus:    codes.Unavailable,
			wantRetryable: true,
			err: runtimeErrorStatus(
				codes.Unknown,
				"服务暂时不可用",
				RuntimeRegistrationUnavailable,
				testRuntimeErrorDomain,
				map[string]string{"retryable": "true"},
			),
		},
		{
			name:          "registration unavailable legacy message prefix",
			wantCode:      RuntimeRegistrationUnavailable,
			wantStatus:    codes.Unavailable,
			wantRetryable: true,
			err:           status.Error(codes.Unknown, RuntimeRegistrationUnavailable+": temporarily unavailable"),
		},
		{
			name:          "registration code not found ErrorInfo details",
			rawCode:       RuntimeRegistrationCodeNotFound,
			wantCode:      RuntimeRegistrationCodeNotFound,
			wantStatus:    codes.InvalidArgument,
			wantRetryable: false,
			err: runtimeErrorStatus(
				codes.Unknown,
				"邀请码不存在",
				"registration_code_not_found",
				testRuntimeErrorDomain,
				map[string]string{"retryable": "false"},
			),
		},
		{
			name:          "registration code merchant mismatch ErrorInfo details",
			rawCode:       RuntimeRegistrationCodeMerchantMismatch,
			wantCode:      RuntimeRegistrationCodeMerchantMismatch,
			wantStatus:    codes.InvalidArgument,
			wantRetryable: false,
			err: runtimeErrorStatus(
				codes.Unknown,
				"邀请码不属于本站点所属商户",
				"registration_code_merchant_mismatch",
				testRuntimeErrorDomain,
				map[string]string{"retryable": "false"},
			),
		},
		{
			name:          "registration code invalid ErrorInfo details",
			rawCode:       RuntimeRegistrationCodeInvalid,
			wantCode:      RuntimeRegistrationCodeInvalid,
			wantStatus:    codes.InvalidArgument,
			wantRetryable: false,
			err: runtimeErrorStatus(
				codes.Unknown,
				"邀请码在本站不可用",
				"registration_code_invalid",
				testRuntimeErrorDomain,
				map[string]string{"retryable": "true"},
			),
		},
		{
			name:          "registration code expired ErrorInfo details",
			rawCode:       RuntimeRegistrationCodeExpired,
			wantCode:      RuntimeRegistrationCodeExpired,
			wantStatus:    codes.InvalidArgument,
			wantRetryable: false,
			err: runtimeErrorStatus(
				codes.Unknown,
				"邀请码已不可再用",
				"registration_code_expired",
				testRuntimeErrorDomain,
				map[string]string{"retryable": "true"},
			),
		},
	}
	legacyAuthentication := status.Error(codes.Unauthenticated, RuntimeUnauthenticated+": legacy compatibility")
	if got := ErrorCode(legacyAuthentication); got != RuntimeUnauthenticated {
		t.Fatalf("legacy auth ErrorCode = %q, want %q", got, RuntimeUnauthenticated)
	}
	for _, testCase := range testCases {
		t.Run(testCase.name, func(t *testing.T) {
			if testCase.rawCode != "" {
				if got := ErrorCode(testCase.err); got != testCase.rawCode {
					t.Fatalf("raw ErrorCode = %q, want %q", got, testCase.rawCode)
				}
			}
			connection := &runtimeErrorConn{err: testCase.err}
			tokens, _ := runtimeTestTokens(time.Unix(1_800_000_000, 0))
			client := NewRuntimeClient(connection, tokens)
			_, err := client.ResolveRegistration(context.Background(), &runtimepb.ResolveRegistrationRequest{})
			if err == nil {
				t.Fatal("ResolveRegistration unexpectedly succeeded")
			}
			if ErrorCode(err) != testCase.wantCode || status.Code(err) != testCase.wantStatus {
				t.Fatalf("stable error = %v, code=%q want=%q grpc=%s want=%s", err, ErrorCode(err), testCase.wantCode, status.Code(err), testCase.wantStatus)
			}
			retryable, ok := err.(interface{ Retryable() bool })
			if !ok || retryable.Retryable() != testCase.wantRetryable {
				t.Fatalf("%s retryable = %#v, want %t", testCase.wantCode, err, testCase.wantRetryable)
			}
			if connection.calls.Load() != 1 {
				t.Fatalf("ResolveRegistration calls = %d, want no SDK retry", connection.calls.Load())
			}
		})
	}
}
