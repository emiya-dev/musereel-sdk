package musereelsdk

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"unicode"
	"unicode/utf8"

	runtimepb "github.com/emiya-dev/musereel-sdk/runtime"
	"google.golang.org/grpc"
	"google.golang.org/grpc/codes"
	"google.golang.org/grpc/status"
)

const (
	// RuntimeRegistrationUnavailable 是注册解析面可重试的稳定错误码。
	RuntimeRegistrationUnavailable = "registration_unavailable"
	// RuntimeRegistrationCodeInvalid 是邀请码在本站不可用且不可重试的稳定错误码。
	RuntimeRegistrationCodeInvalid = "registration_code_invalid"
	// RuntimeRegistrationCodeExpired 是邀请码曾经可用但当前不可再用且不可重试的稳定错误码。
	RuntimeRegistrationCodeExpired = "registration_code_expired"
	// RuntimeRegistrationCodeNotFound 是邀请码不存在且不可重试的稳定错误码。
	//
	// Deprecated: 中枢 S78 起，registration_code_not_found 不再出现在传输边界；请使用
	// RuntimeRegistrationCodeInvalid 表示当前契约中的不可用邀请码。
	RuntimeRegistrationCodeNotFound = "registration_code_not_found"
	// RuntimeRegistrationCodeMerchantMismatch 是邀请码与站点所属商户不匹配且不可重试的稳定错误码。
	//
	// Deprecated: 中枢 S78 起，registration_code_merchant_mismatch 不再出现在传输边界；请使用
	// RuntimeRegistrationCodeInvalid 表示当前契约中的不可用邀请码。
	RuntimeRegistrationCodeMerchantMismatch = "registration_code_merchant_mismatch"
	// RuntimeQueryInvalid 是余额、流水等查询请求无效的稳定错误码。
	RuntimeQueryInvalid = "runtime_query_invalid"
	// RuntimeSubjectUnavailable 是运行时 subject 暂不可用的稳定错误码。
	RuntimeSubjectUnavailable = "runtime_subject_unavailable"
	// RuntimeIdentityInactive 是 identity 已停用的稳定错误码。
	RuntimeIdentityInactive = "identity_inactive"

	// runtimeAssertionMethod 是 gRPC 面 actor assertion 的 method 分量。
	//
	// ⚠ 必须是 "GRPC"，不是 "POST"。请求指纹是
	// base64url(SHA-256(method\npath\nactor\nidem\nJCS(body)))，method 是其中一个分量；
	// 服务端在 gRPC 面固定用 "GRPC" 参与计算（registration/confirm.go、checkout/handler.go、
	// querysurface/{balance,ledger}.go 共五处）。这里填 "POST" 会让指纹全盘对不上，
	// 每一次带断言的 gRPC 调用都被拒 actor_assertion_invalid，且错误里看不出是 method 的锅。
	//
	// gRPC 底层确实跑在 HTTP/2 POST 上——这正是当初填 "POST" 的由来。但指纹里的 method
	// 是**契约面**的标识而不是传输动词，两者不是一回事。
	runtimeAssertionMethod = "GRPC"
)

// RuntimeAssertionConfig 保存运行时 actor assertion 的签发上下文。
// operation、path、body 和幂等键由具体 RPC 按冻结契约生成，调用方不能覆盖。
type RuntimeAssertionConfig struct {
	Signer     Signer
	InstanceID string
	TenantID   string
	SessionID  string
}

// RuntimeClientOption 配置 RuntimeClient 的本地行为。
type RuntimeClientOption func(*RuntimeClient)

// WithRuntimeAssertionConfig 注入五个需要 actor assertion 的 RPC 所共用的
// signer 和 token-bound identity context。
func WithRuntimeAssertionConfig(config RuntimeAssertionConfig) RuntimeClientOption {
	return func(client *RuntimeClient) {
		client.assertion = config
	}
}

// WithRuntimeAssertion 是 WithRuntimeAssertionConfig 的便捷形式。
func WithRuntimeAssertion(signer Signer, instanceID, tenantID, sessionID string) RuntimeClientOption {
	return WithRuntimeAssertionConfig(RuntimeAssertionConfig{
		Signer:     signer,
		InstanceID: instanceID,
		TenantID:   tenantID,
		SessionID:  sessionID,
	})
}

// RuntimeClient 是 runtime.v1.RuntimeService 的类型化控制面封装。
// ExchangeRuntimeToken 不在此 client 中：它由 GRPCTokenSource 通过 mTLS
// bootstrap 承载。ResolveRegistration 与其余无 assertion 的 RPC 一样，均
// 通过 AuthenticatedClient 发送 Bearer 并保留 SDK-002 的一次稳定未认证刷新重试。
type RuntimeClient struct {
	connection    grpc.ClientConnInterface
	authenticated *AuthenticatedClient
	assertion     RuntimeAssertionConfig
}

// NewRuntimeClient 构造 runtime 控制面 client。tokens 用于所有需要 Bearer
// 的 RPC，包括 ResolveRegistration。
func NewRuntimeClient(connection grpc.ClientConnInterface, tokens TokenSource, options ...RuntimeClientOption) *RuntimeClient {
	client := &RuntimeClient{
		connection:    connection,
		authenticated: NewAuthenticatedClient(connection, tokens),
	}
	for _, option := range options {
		if option != nil {
			option(client)
		}
	}
	return client
}

// RuntimeRPCError 保留底层 gRPC 错误，同时把服务端稳定 code 和 retryable
// 语义暴露给调用方。服务端 status code 已正确设置时保持原值；对冻结的
// 稳定 code 也提供契约规定的 status 映射。
type RuntimeRPCError struct {
	cause      error
	stableCode string
	retryable  bool
	statusCode codes.Code
}

func (err *RuntimeRPCError) Error() string {
	if err == nil || err.cause == nil {
		return ""
	}
	return err.cause.Error()
}

func (err *RuntimeRPCError) Unwrap() error {
	if err == nil {
		return nil
	}
	return err.cause
}

// ErrorCode implements ErrorCodeProvider without changing errors.go's frozen
// SDK-002 implementation。
func (err *RuntimeRPCError) ErrorCode() string {
	if err == nil {
		return ""
	}
	return err.stableCode
}

// Retryable reports the frozen retryability of the returned runtime error。
// RuntimeClient itself does not retry registration_unavailable。
func (err *RuntimeRPCError) Retryable() bool {
	return err != nil && err.retryable
}

// GRPCStatus keeps status.Code and status.Convert useful after the stable-code
// wrapper is applied。
func (err *RuntimeRPCError) GRPCStatus() *status.Status {
	if err == nil || err.cause == nil {
		return status.New(codes.Unknown, "runtime RPC failed")
	}
	message := status.Convert(err.cause).Message()
	return status.New(err.statusCode, message)
}

// ResolveRegistration 使用已认证的 mTLS + Bearer 实例 scope 解析注册 intent。
// request.Domain 是前端提供的字符串；SDK 将它原样放在 protobuf request 中，
// 不从 Host、Origin 或配置推导、规范化或补全。request.InviteCode 仅是渠道标识。
// 邀请码在本站不可用时，传输边界只返回 RuntimeRegistrationCodeInvalid；邀请码曾经可用
// 但当前不能再用时，只返回 RuntimeRegistrationCodeExpired。中枢内部的 not_found、
// merchant_mismatch 等原因不会在 SDK 传输边界中继续细分；两者都是调用方输入错误，不可重试。
//
// registration_unavailable 的服务端响应应在外部 2 秒总预算内最多重试一次；
// SDK 不内建该重试。
func (client *RuntimeClient) ResolveRegistration(ctx context.Context, request *runtimepb.ResolveRegistrationRequest, options ...grpc.CallOption) (*runtimepb.RegistrationIntent, error) {
	if request == nil {
		return nil, fmt.Errorf("resolve registration request is nil")
	}
	if client == nil || client.connection == nil {
		return nil, fmt.Errorf("runtime grpc connection is not configured")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	reply := new(runtimepb.RegistrationIntent)
	if err := client.invokeAuthenticated(ctx, runtimepb.RuntimeService_ResolveRegistration_FullMethodName, request, reply, options...); err != nil {
		return nil, normalizeRuntimeError(err)
	}
	return reply, nil
}

// ConfirmRegistration 以 intent 的 fingerprint 生成固定 JCS body。intent
// token 只留在 protobuf request，不进入 assertion fingerprint；actor 自身
// 是本 RPC 的幂等键例外，且按契约允许最多 256 bytes 的 UTF-8。
func (client *RuntimeClient) ConfirmRegistration(ctx context.Context, request *runtimepb.ConfirmRegistrationRequest, intent *runtimepb.RegistrationIntent, options ...grpc.CallOption) (*runtimepb.RegistrationReply, error) {
	if request == nil {
		return nil, fmt.Errorf("confirm registration request is nil")
	}
	if intent == nil || intent.GetRegistrationIntentFingerprint() == "" {
		return nil, fmt.Errorf("registration intent fingerprint is missing")
	}
	if err := validateActor(request.GetActor()); err != nil {
		return nil, err
	}
	body, err := json.Marshal(struct {
		RegistrationIntentFingerprint string `json:"registration_intent_fingerprint"`
	}{RegistrationIntentFingerprint: intent.GetRegistrationIntentFingerprint()})
	if err != nil {
		return nil, fmt.Errorf("marshal confirm registration assertion body: %w", err)
	}
	reply := new(runtimepb.RegistrationReply)
	err = client.invokeAssertion(ctx, runtimepb.RuntimeService_ConfirmRegistration_FullMethodName, request, reply,
		request.GetActor(), "registration:confirm", request.GetActor(), body,
		func(assertion JWS) error {
			request.ActorAssertion = assertion.Bytes()
			return nil
		},
		func() (string, string, []byte, error) {
			return request.GetActor(), request.GetActor(), body, nil
		}, options...)
	if err != nil {
		return nil, err
	}
	return reply, nil
}

// CreateOrder 冻结 offer_price_id，并以 idempotency_key 保护一次订单创建。
// 价格金额由生成类型以 string 原样传递，SDK 不解析或重格式化。
func (client *RuntimeClient) CreateOrder(ctx context.Context, request *runtimepb.CreateOrderRequest, options ...grpc.CallOption) (*runtimepb.CreateOrderReply, error) {
	if request == nil {
		return nil, fmt.Errorf("create order request is nil")
	}
	if err := validateIdempotencyKey(request.GetIdempotencyKey()); err != nil {
		return nil, err
	}
	if err := validateActor(request.GetActor()); err != nil {
		return nil, err
	}
	body, err := json.Marshal(struct {
		OfferPriceID string `json:"offer_price_id"`
	}{OfferPriceID: request.GetOfferPriceId()})
	if err != nil {
		return nil, fmt.Errorf("marshal create order assertion body: %w", err)
	}
	reply := new(runtimepb.CreateOrderReply)
	err = client.invokeAssertion(ctx, runtimepb.RuntimeService_CreateOrder_FullMethodName, request, reply,
		request.GetActor(), "order:create", request.GetIdempotencyKey(), body,
		func(assertion JWS) error {
			request.ActorAssertion = assertion.Bytes()
			return nil
		},
		func() (string, string, []byte, error) {
			currentBody, bodyErr := json.Marshal(struct {
				OfferPriceID string `json:"offer_price_id"`
			}{OfferPriceID: request.GetOfferPriceId()})
			return request.GetActor(), request.GetIdempotencyKey(), currentBody, bodyErr
		}, options...)
	if err != nil {
		return nil, err
	}
	return reply, nil
}

// VerifyAndConfirmPayment 原样转交支付证明。SDK 不解析 signed_payload、
// signed_headers 或 provider_query_ref，也不提供“标记已支付”的语义。
func (client *RuntimeClient) VerifyAndConfirmPayment(ctx context.Context, request *runtimepb.VerifyAndConfirmPaymentRequest, options ...grpc.CallOption) (*runtimepb.VerifyAndConfirmPaymentReply, error) {
	if request == nil {
		return nil, fmt.Errorf("verify and confirm payment request is nil")
	}
	if err := validateIdempotencyKey(request.GetIdempotencyKey()); err != nil {
		return nil, err
	}
	reply := new(runtimepb.VerifyAndConfirmPaymentReply)
	if err := client.invokeAuthenticated(ctx, runtimepb.RuntimeService_VerifyAndConfirmPayment_FullMethodName, request, reply, options...); err != nil {
		return nil, err
	}
	return reply, nil
}

// GetOrder 使用固定 body {"order_id":"..."} 生成 query assertion。
func (client *RuntimeClient) GetOrder(ctx context.Context, request *runtimepb.GetOrderRequest, options ...grpc.CallOption) (*runtimepb.GetOrderReply, error) {
	if request == nil {
		return nil, fmt.Errorf("get order request is nil")
	}
	if err := validateActor(request.GetActor()); err != nil {
		return nil, err
	}
	body, err := json.Marshal(struct {
		OrderID string `json:"order_id"`
	}{OrderID: request.GetOrderId()})
	if err != nil {
		return nil, fmt.Errorf("marshal get order assertion body: %w", err)
	}
	reply := new(runtimepb.GetOrderReply)
	err = client.invokeAssertion(ctx, runtimepb.RuntimeService_GetOrder_FullMethodName, request, reply,
		request.GetActor(), "order:get", "", body,
		func(assertion JWS) error {
			request.ActorAssertion = assertion.Bytes()
			return nil
		},
		func() (string, string, []byte, error) {
			currentBody, bodyErr := json.Marshal(struct {
				OrderID string `json:"order_id"`
			}{OrderID: request.GetOrderId()})
			return request.GetActor(), "", currentBody, bodyErr
		}, options...)
	if err != nil {
		return nil, err
	}
	return reply, nil
}

// SyncIdentity 发送 instance-scoped identity lifecycle 事件。event_id 的
// 唯一空间由三个 identity RPC 共享；SDK 只校验制式，不做跨 RPC 查重。
func (client *RuntimeClient) SyncIdentity(ctx context.Context, request *runtimepb.SyncIdentityRequest, options ...grpc.CallOption) (*runtimepb.IdentityReply, error) {
	if request == nil {
		return nil, fmt.Errorf("sync identity request is nil")
	}
	if err := validateEventID(request.GetEventId()); err != nil {
		return nil, err
	}
	reply := new(runtimepb.IdentityReply)
	if err := client.invokeAuthenticated(ctx, runtimepb.RuntimeService_SyncIdentity_FullMethodName, request, reply, options...); err != nil {
		return nil, err
	}
	return reply, nil
}

// SyncVerificationStatus 只允许 proto 冻结的 verified、verified_at_ms、
// credential_ref、issuer 载荷；不存在 PII 入口。credential_ref 与 issuer
// 在客户端做 ASCII 制式校验。
func (client *RuntimeClient) SyncVerificationStatus(ctx context.Context, request *runtimepb.SyncVerificationStatusRequest, options ...grpc.CallOption) (*runtimepb.IdentityReply, error) {
	if request == nil {
		return nil, fmt.Errorf("sync verification status request is nil")
	}
	if err := validateEventID(request.GetEventId()); err != nil {
		return nil, err
	}
	if err := validateCredentialRef(request.GetCredentialRef()); err != nil {
		return nil, err
	}
	if err := validateIssuer(request.GetIssuer()); err != nil {
		return nil, err
	}
	reply := new(runtimepb.IdentityReply)
	if err := client.invokeAuthenticated(ctx, runtimepb.RuntimeService_SyncVerificationStatus_FullMethodName, request, reply, options...); err != nil {
		return nil, err
	}
	return reply, nil
}

// DisableIdentity 发送 instance-scoped disabled 事件。
func (client *RuntimeClient) DisableIdentity(ctx context.Context, request *runtimepb.DisableIdentityRequest, options ...grpc.CallOption) (*runtimepb.IdentityReply, error) {
	if request == nil {
		return nil, fmt.Errorf("disable identity request is nil")
	}
	if err := validateEventID(request.GetEventId()); err != nil {
		return nil, err
	}
	reply := new(runtimepb.IdentityReply)
	if err := client.invokeAuthenticated(ctx, runtimepb.RuntimeService_DisableIdentity_FullMethodName, request, reply, options...); err != nil {
		return nil, err
	}
	return reply, nil
}

// GetBalance 使用空 JSON body 和 strict-nonce balance:get assertion。
func (client *RuntimeClient) GetBalance(ctx context.Context, request *runtimepb.GetBalanceRequest, options ...grpc.CallOption) (*runtimepb.BalanceReply, error) {
	if request == nil {
		return nil, fmt.Errorf("get balance request is nil")
	}
	if err := validateActor(request.GetActor()); err != nil {
		return nil, err
	}
	body := []byte("{}")
	reply := new(runtimepb.BalanceReply)
	err := client.invokeAssertion(ctx, runtimepb.RuntimeService_GetBalance_FullMethodName, request, reply,
		request.GetActor(), "balance:get", "", body,
		func(assertion JWS) error {
			request.ActorAssertion = assertion.Bytes()
			return nil
		},
		func() (string, string, []byte, error) {
			return request.GetActor(), "", body, nil
		}, options...)
	if err != nil {
		return nil, err
	}
	return reply, nil
}

// ListLedger 透传不透明 page_cursor，page_size=0 保持服务端默认 50，显式值
// 只允许 1-100；strict nonce body 使用请求中的原始 page_size。
func (client *RuntimeClient) ListLedger(ctx context.Context, request *runtimepb.ListLedgerRequest, options ...grpc.CallOption) (*runtimepb.LedgerReply, error) {
	if request == nil {
		return nil, fmt.Errorf("list ledger request is nil")
	}
	if err := validateActor(request.GetActor()); err != nil {
		return nil, err
	}
	if request.GetPageSize() < 0 || request.GetPageSize() > 100 {
		return nil, fmt.Errorf("page size must be 0 or in the range 1-100")
	}
	body, err := ledgerAssertionBody(request.GetPageCursor(), request.GetPageSize())
	if err != nil {
		return nil, err
	}
	reply := new(runtimepb.LedgerReply)
	err = client.invokeAssertion(ctx, runtimepb.RuntimeService_ListLedger_FullMethodName, request, reply,
		request.GetActor(), "ledger:list", "", body,
		func(assertion JWS) error {
			request.ActorAssertion = assertion.Bytes()
			return nil
		},
		func() (string, string, []byte, error) {
			currentBody, bodyErr := ledgerAssertionBody(request.GetPageCursor(), request.GetPageSize())
			return request.GetActor(), "", currentBody, bodyErr
		}, options...)
	if err != nil {
		return nil, err
	}
	return reply, nil
}

// GetSkuCatalog 请求空 protobuf message，使用 Bearer 但不生成 assertion。
func (client *RuntimeClient) GetSkuCatalog(ctx context.Context, options ...grpc.CallOption) (*runtimepb.SkuCatalogReply, error) {
	reply := new(runtimepb.SkuCatalogReply)
	request := &runtimepb.GetSkuCatalogRequest{}
	if err := client.invokeAuthenticated(ctx, runtimepb.RuntimeService_GetSkuCatalog_FullMethodName, request, reply, options...); err != nil {
		return nil, err
	}
	return reply, nil
}

// GetOfferCatalog 请求空 protobuf message，使用 Bearer 但不生成 assertion。
func (client *RuntimeClient) GetOfferCatalog(ctx context.Context, options ...grpc.CallOption) (*runtimepb.OfferCatalogReply, error) {
	reply := new(runtimepb.OfferCatalogReply)
	request := &runtimepb.GetOfferCatalogRequest{}
	if err := client.invokeAuthenticated(ctx, runtimepb.RuntimeService_GetOfferCatalog_FullMethodName, request, reply, options...); err != nil {
		return nil, err
	}
	return reply, nil
}

// ListSiteBranding 请求空 protobuf message，使用 Bearer 但不生成 assertion。
//
// 它是实例级查询（返回本实例全部站点的品牌引用），契约上不携带 actor assertion，
// 因此与两个 catalog 同型：登记进 runtimeMethods 与 runtimeQueryMethods，
// 但不进 runtimeAssertionOperations。
func (client *RuntimeClient) ListSiteBranding(ctx context.Context, options ...grpc.CallOption) (*runtimepb.ListSiteBrandingReply, error) {
	reply := new(runtimepb.ListSiteBrandingReply)
	request := &runtimepb.ListSiteBrandingRequest{}
	if err := client.invokeAuthenticated(ctx, runtimepb.RuntimeService_ListSiteBranding_FullMethodName, request, reply, options...); err != nil {
		return nil, err
	}
	return reply, nil
}

type runtimeAssertionIdentity func() (actor, idempotencyKey string, body []byte, err error)

func (client *RuntimeClient) invokeAssertion(
	ctx context.Context,
	method string,
	request, reply any,
	actor, operation, idempotencyKey string,
	body []byte,
	apply func(JWS) error,
	identity runtimeAssertionIdentity,
	options ...grpc.CallOption,
) error {
	if client == nil || client.authenticated == nil {
		return fmt.Errorf("runtime authenticated client is not configured")
	}
	if client.assertion.Signer == nil {
		return fmt.Errorf("runtime assertion signer is not configured")
	}
	fingerprint, err := RequestFingerprint(runtimeAssertionMethod, method, actor, idempotencyKey, body)
	if err != nil {
		return err
	}
	input := AssertionInput{
		InstanceID:     client.assertion.InstanceID,
		TenantID:       client.assertion.TenantID,
		SessionID:      client.assertion.SessionID,
		Actor:          actor,
		Operation:      operation,
		Method:         runtimeAssertionMethod,
		CanonicalPath:  method,
		Body:           body,
		IdempotencyKey: idempotencyKey,
	}
	call := AssertionCall{
		Args:               request,
		IdempotencyKey:     idempotencyKey,
		RequestFingerprint: fingerprint,
		Sign: func(Token) (JWS, error) {
			return SignActorAssertion(client.assertion.Signer, input)
		},
		ApplyAssertion: func(_ any, assertion JWS) error {
			return apply(assertion)
		},
	}
	if identity != nil {
		call.ReadIdentity = func(any) (string, string, error) {
			currentActor, currentKey, currentBody, identityErr := identity()
			if identityErr != nil {
				return "", "", identityErr
			}
			currentFingerprint, fingerprintErr := RequestFingerprint(runtimeAssertionMethod, method, currentActor, currentKey, currentBody)
			if fingerprintErr != nil {
				return "", "", fingerprintErr
			}
			return currentKey, currentFingerprint, nil
		}
	}
	return normalizeRuntimeError(client.authenticated.InvokeWithAssertion(ctx, method, call, reply, options...))
}

func (client *RuntimeClient) invokeAuthenticated(ctx context.Context, method string, request, reply any, options ...grpc.CallOption) error {
	if client == nil || client.authenticated == nil {
		return fmt.Errorf("runtime authenticated client is not configured")
	}
	return normalizeRuntimeError(client.authenticated.Invoke(ctx, method, request, reply, options...))
}

func validateEventID(value string) error {
	if len(value) < 16 || len(value) > 128 {
		return fmt.Errorf("event_id must be 16-128 ASCII bytes")
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if (character >= 'A' && character <= 'Z') ||
			(character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') ||
			character == '.' || character == '_' || character == ':' || character == '-' {
			continue
		}
		return fmt.Errorf("event_id must match [A-Za-z0-9._:-]")
	}
	return nil
}

func validateCredentialRef(value string) error {
	if value == "" {
		return fmt.Errorf("credential_ref is missing")
	}
	if len(value) > 256 {
		return fmt.Errorf("credential_ref must be at most 256 ASCII bytes")
	}
	for index := 0; index < len(value); index++ {
		if value[index] < 0x21 || value[index] > 0x7e {
			return fmt.Errorf("credential_ref must contain printable ASCII without whitespace or control characters")
		}
	}
	return nil
}

func validateIssuer(value string) error {
	if len(value) == 0 || len(value) > 64 {
		return fmt.Errorf("issuer must match [a-z0-9][a-z0-9._-]{0,63}")
	}
	for index := 0; index < len(value); index++ {
		character := value[index]
		if index == 0 {
			if !((character >= 'a' && character <= 'z') || (character >= '0' && character <= '9')) {
				return fmt.Errorf("issuer must match [a-z0-9][a-z0-9._-]{0,63}")
			}
			continue
		}
		if (character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') ||
			character == '.' || character == '_' || character == '-' {
			continue
		}
		return fmt.Errorf("issuer must match [a-z0-9][a-z0-9._-]{0,63}")
	}
	return nil
}

func validateIdempotencyKey(value string) error {
	if value == "" || strings.ContainsAny(value, "\r\n") {
		return fmt.Errorf("idempotency_key is missing or invalid")
	}
	return nil
}

func validateActor(value string) error {
	if value == "" || !utf8.ValidString(value) || len(value) > 256 || strings.TrimSpace(value) != value {
		return fmt.Errorf("actor must be non-empty valid UTF-8 of at most 256 bytes without leading or trailing whitespace")
	}
	for _, character := range value {
		if unicode.IsControl(character) {
			return fmt.Errorf("actor must not contain control characters")
		}
	}
	return nil
}

func ledgerAssertionBody(pageCursor string, pageSize int32) ([]byte, error) {
	body, err := json.Marshal(struct {
		PageCursor string `json:"page_cursor"`
		PageSize   int32  `json:"page_size"`
	}{PageCursor: pageCursor, PageSize: pageSize})
	if err != nil {
		return nil, fmt.Errorf("marshal list ledger assertion body: %w", err)
	}
	return body, nil
}

func normalizeRuntimeError(err error) error {
	if err == nil {
		return nil
	}
	if _, alreadyWrapped := err.(*RuntimeRPCError); alreadyWrapped {
		return err
	}
	code := ErrorCode(err)
	if code == "" {
		code = stableRuntimeCodeFromMessage(status.Convert(err).Message())
	}
	if code == "" || code == RuntimeUnauthenticated || code == ActorAssertionInvalid || code == ActorAssertionReplayed {
		return err
	}
	return &RuntimeRPCError{
		cause:      err,
		stableCode: code,
		retryable:  runtimeRetryable(err, code),
		statusCode: runtimeStatusCode(code, status.Code(err)),
	}
}

func runtimeRetryable(err error, code string) bool {
	// 这些邀请码错误码先于 metadata 判定，是本函数里唯一「SDK 覆盖服务端信号」的位置，
	// 因此要说清为什么它们与下面那些码不同：它们描述的是**分类事实**——调用方给的
	// 邀请码在本站不可用或已不可再用，重试同一个请求永远不会变成对的。而
	// registration_unavailable 之流描述的是**运行时状态**，服务端最清楚此刻能不能再试，
	// 所以那些码让 metadata 说了算。
	// ⇒ 服务端即使误发 retryable=true，也不能让调用方对一个永不成功的输入反复重试。
	// 对应用例见 TestRuntimeRetryableMetadataHasThreeStates 里邀请码错误码 metadata 为
	// "true" 而 want 为 false 的行；删掉对应分支它们会立刻变红。
	switch code {
	case RuntimeRegistrationCodeNotFound, RuntimeRegistrationCodeMerchantMismatch:
		return false
	case RuntimeRegistrationCodeInvalid, RuntimeRegistrationCodeExpired:
		return false
	}
	if info, ok := runtimeErrorInfo(err); ok {
		switch info.Metadata["retryable"] {
		case "true":
			return true
		case "false":
			return false
		}
	}
	return code == RuntimeRegistrationUnavailable
}

func stableRuntimeCodeFromMessage(message string) string {
	candidate := message
	if separator := strings.IndexAny(candidate, ": \t\r\n"); separator >= 0 {
		candidate = candidate[:separator]
	}
	if candidate == "" {
		return ""
	}
	underscore := false
	for index := 0; index < len(candidate); index++ {
		character := candidate[index]
		if character == '_' {
			underscore = true
			continue
		}
		if (character >= 'a' && character <= 'z') || (character >= '0' && character <= '9') {
			continue
		}
		return ""
	}
	if !underscore || candidate[0] < 'a' || candidate[0] > 'z' {
		return ""
	}
	return candidate
}

func runtimeStatusCode(stableCode string, fallback codes.Code) codes.Code {
	switch stableCode {
	case RuntimeRegistrationUnavailable:
		return codes.Unavailable
	case RuntimeRegistrationCodeNotFound, RuntimeRegistrationCodeMerchantMismatch:
		return codes.InvalidArgument
	case RuntimeRegistrationCodeInvalid, RuntimeRegistrationCodeExpired:
		return codes.InvalidArgument
	case RuntimeQueryInvalid:
		return codes.InvalidArgument
	case RuntimeSubjectUnavailable:
		return codes.FailedPrecondition
	case RuntimeIdentityInactive:
		return codes.PermissionDenied
	default:
		return fallback
	}
}
