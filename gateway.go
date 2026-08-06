package musereelsdk

import (
	"bytes"
	"context"
	"crypto/sha256"
	"crypto/tls"
	"encoding/base64"
	"encoding/json"
	"fmt"
	"io"
	"mime"
	"net/http"
	"net/url"
	"strconv"
	"strings"
	"sync"
	"time"
)

const gatewaySiteContextPath = "/runtime/v1/registration/site-context"

// GatewayDeliveryMode 由 Gateway 侧 SKU 目录决定。SDK 提供分开的调用方法，
// delivery_mode 不会进入请求体，也不会被 SDK 静默覆盖。
type GatewayDeliveryMode string

const (
	GatewayDeliveryStream GatewayDeliveryMode = "stream"
	GatewayDeliveryAsync  GatewayDeliveryMode = "async"
)

// GatewayInvocationState 是封闭的 snapshot state 集合。
type GatewayInvocationState string

const (
	GatewayStateAccepted            GatewayInvocationState = "accepted"
	GatewayStateRunning             GatewayInvocationState = "running"
	GatewayStateCancelPending       GatewayInvocationState = "cancel_pending"
	GatewayStateReconciling         GatewayInvocationState = "reconciling"
	GatewayStateSettlementShortfall GatewayInvocationState = "settlement_shortfall"
	GatewayStateCompleted           GatewayInvocationState = "completed"
	GatewayStateFailed              GatewayInvocationState = "failed"
	GatewayStateCancelled           GatewayInvocationState = "cancelled"
)

// GatewayActorFunc 在请求时提供 actor。每个逻辑请求只解析一次，token 刷新重试沿用同一值。
type GatewayActorFunc func(context.Context) (string, error)

// GatewayIdentity 是既有 SignAssertion 实现所需的、与 token 绑定的身份上下文。
type GatewayIdentity struct {
	InstanceID string
	TenantID   string
	SessionID  string
	Actor      string
	ActorFunc  GatewayActorFunc
}

// GatewayInvocationSpec 是冻结的 spec 对象。Input 和 Parameters 使用 interface，
// 调用方可传 json.RawMessage 或普通 Go JSON 值；Validate 会拒绝 Parameters 内的 JSON 数值。
type GatewayInvocationSpec struct {
	SchemaVersion string `json:"schema_version"`
	Input         any    `json:"input"`
	Parameters    any    `json:"parameters"`
}

// GatewayCreateRequest 恰好包含 create body 的四个字段，不包含 delivery_mode；
// 交付方式属于 SKU 目录属性。
type GatewayCreateRequest struct {
	SKU               string                `json:"sku_id"`
	TaskRef           string                `json:"task_ref"`
	Spec              GatewayInvocationSpec `json:"spec"`
	ModerationReceipt string                `json:"moderation_receipt"`
}

// Validate 在创建 token 或 assertion 前校验请求形状。
func (request GatewayCreateRequest) Validate() error {
	if strings.TrimSpace(request.SKU) == "" || strings.TrimSpace(request.TaskRef) == "" {
		return fmt.Errorf("create request requires sku_id and task_ref")
	}
	if strings.TrimSpace(request.Spec.SchemaVersion) == "" {
		return fmt.Errorf("create request requires spec.schema_version")
	}
	input, err := json.Marshal(request.Spec.Input)
	if err != nil || gatewayJSONNull(input) {
		return fmt.Errorf("create request requires a non-null spec.input")
	}
	parameters, err := json.Marshal(request.Spec.Parameters)
	if err != nil || gatewayJSONNull(parameters) {
		return fmt.Errorf("create request requires a non-null spec.parameters")
	}
	if err := validateGatewayParameters(parameters); err != nil {
		return err
	}
	return nil
}

// GatewayInvocationSnapshot 由 async create、GET、cancel 共用。Result 和 LotDeductions
// 保持 raw JSON，SDK 不会把 units 或类似金额的值转成浮点数。
type GatewayInvocationSnapshot struct {
	ID            string                 `json:"id"`
	// Version 是 int64：服务端 respond.go 与 06 契约示例（"version": 1）都是 JSON 数字。
	// 这里曾声明成 string，导致 SDK 对真 gateway 的**每一个** snapshot 都解码失败
	// （async create 202 / GET 200 / cancel 全线折叠为协议错误）——而 SDK 自己的
	// fixture 用 %q 发带引号的字符串，测试因此反过来认证了这个错误假设。
	Version       int64                  `json:"version"`
	State         GatewayInvocationState `json:"state"`
	Terminal      bool                   `json:"terminal"`
	SKU           string                 `json:"sku_id"`
	TaskRef       string                 `json:"task_ref"`
	CreatedAtMS   int64                  `json:"created_at_ms"`
	UpdatedAtMS   int64                  `json:"updated_at_ms"`
	ReservedUnits *string                `json:"reserved_units"`
	SettledUnits  *string                `json:"settled_units"`
	Result        json.RawMessage        `json:"result"`
	Error         *GatewayError          `json:"error"`
	LotDeductions json.RawMessage        `json:"lot_deductions"`
}

// GatewayCreateResponse 由两种 create 模式共用。303 用 AlreadyExists=true、
// InvocationID 有值且 error=nil 表示；Stream 由调用方持有并负责关闭。
type GatewayCreateResponse struct {
	StatusCode    int
	RequestID     string
	InvocationID  string
	Location      string
	AlreadyExists bool
	Snapshot      *GatewayInvocationSnapshot
	Stream        *GatewaySSEStream
}

// GatewayGetResponse 包含新 snapshot 或 304 结果；ETag 和 RetryAfter 是 GatewayPoller 使用的元数据。
type GatewayGetResponse struct {
	StatusCode  int
	RequestID   string
	Snapshot    *GatewayInvocationSnapshot
	NotModified bool
	ETag        string
	RetryAfter  time.Duration
}

// GatewayCancelResponse 表示新接受的取消意图（202）或当前 snapshot（200）。
type GatewayCancelResponse struct {
	StatusCode   int
	RequestID    string
	InvocationID string
	Accepted     bool
	Snapshot     *GatewayInvocationSnapshot
}

// GatewaySiteContext 是一次成功的无认证 site-context 交换；SiteContextToken 使用
// SecretString，只能通过显式操作 reveal。
type GatewaySiteContext struct {
	RequestID        string       `json:"request_id"`
	SiteContextToken SecretString `json:"site_context_token"`
	ExpiresAtMS      int64        `json:"expires_at_ms"`
}

type gatewayClientConfig struct {
	now Clock
}

// GatewayClientOption 只定制 SDK 本地行为。
type GatewayClientOption func(*gatewayClientConfig)

// WithGatewayClock 注入 assertion 签发和轮询元数据测试使用的时钟，不改变最多 60 秒的 wire TTL。
func WithGatewayClock(now Clock) GatewayClientOption {
	return func(config *gatewayClientConfig) {
		if now != nil {
			config.now = now
		}
	}
}

// GatewayWithClock 是与既有 token-source WithClock 命名形状一致的别名。
func GatewayWithClock(now Clock) GatewayClientOption { return WithGatewayClock(now) }

// GatewayClient 封装四条带认证的 invocation 路由。
type GatewayClient struct {
	baseURL    *url.URL
	tokens     TokenSource
	signer     Signer
	identity   GatewayIdentity
	httpClient *http.Client
	now        Clock
}

// NewGatewayClient 构造带认证的 Gateway HTTP 客户端。调用方传入 NewTLSConfig
// 产出的 TLS 配置，以及 SDK 其余部分共用的 token 和 signer 抽象。
func NewGatewayClient(baseURL string, tlsConfig *tls.Config, tokens TokenSource, signer Signer, identity GatewayIdentity, options ...GatewayClientOption) (*GatewayClient, error) {
	parsedURL, err := parseGatewayBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	if tokens == nil {
		return nil, fmt.Errorf("gateway token source is not configured")
	}
	if signer == nil {
		return nil, fmt.Errorf("gateway assertion signer is not configured")
	}
	if identity.InstanceID == "" || identity.TenantID == "" || identity.SessionID == "" || (identity.Actor == "" && identity.ActorFunc == nil) {
		return nil, fmt.Errorf("gateway identity context is incomplete")
	}
	config := gatewayClientConfig{now: systemClock}
	for _, option := range options {
		if option != nil {
			option(&config)
		}
	}
	return &GatewayClient{
		baseURL:    parsedURL,
		tokens:     tokens,
		signer:     signer,
		identity:   identity,
		httpClient: newGatewayHTTPClient(tlsConfig),
		now:        config.now,
	}, nil
}

// Create 按指定传输模式发送 create 请求。模式只影响 Accept 和响应处理；服务端 SKU
// 目录始终是权威，冲突时返回稳定的 406 mismatch。
func (client *GatewayClient) Create(ctx context.Context, request GatewayCreateRequest, idempotencyKey string, mode GatewayDeliveryMode) (GatewayCreateResponse, error) {
	return client.create(ctx, request, idempotencyKey, mode)
}

// CreateStream 发送 stream 模式 create 请求并返回 SSE 流。
func (client *GatewayClient) CreateStream(ctx context.Context, request GatewayCreateRequest, idempotencyKey string) (GatewayCreateResponse, error) {
	return client.create(ctx, request, idempotencyKey, GatewayDeliveryStream)
}

// CreateAsync 发送 async 模式 create 请求并返回 snapshot。
func (client *GatewayClient) CreateAsync(ctx context.Context, request GatewayCreateRequest, idempotencyKey string) (GatewayCreateResponse, error) {
	return client.create(ctx, request, idempotencyKey, GatewayDeliveryAsync)
}

func (client *GatewayClient) create(ctx context.Context, request GatewayCreateRequest, idempotencyKey string, mode GatewayDeliveryMode) (GatewayCreateResponse, error) {
	if client == nil {
		return GatewayCreateResponse{}, fmt.Errorf("gateway client is not configured")
	}
	if mode != GatewayDeliveryStream && mode != GatewayDeliveryAsync {
		return GatewayCreateResponse{}, newGatewayError(GatewayInvalidInvocationRequest, 0, "", "unsupported delivery mode")
	}
	if err := request.Validate(); err != nil {
		return GatewayCreateResponse{}, newGatewayError(GatewayInvalidInvocationRequest, 0, "", "invalid create request")
	}
	if err := validateGatewayIdempotencyKey(idempotencyKey); err != nil {
		return GatewayCreateResponse{}, newGatewayError(GatewayInvalidInvocationRequest, 0, "", "invalid idempotency key")
	}
	path, err := CanonicalGatewayPath(GatewayInvocationCreate)
	if err != nil {
		return GatewayCreateResponse{}, err
	}
	body, err := json.Marshal(request)
	if err != nil {
		return GatewayCreateResponse{}, newGatewayError(GatewayInvalidInvocationRequest, 0, "", "invalid create request")
	}
	accept := "application/json"
	if mode == GatewayDeliveryStream {
		accept = "text/event-stream"
	}
	response, err := client.doInvocation(ctx, http.MethodPost, path, string(GatewayInvocationCreate), body, idempotencyKey, http.Header{
		"Accept": []string{accept},
	})
	if err != nil {
		return GatewayCreateResponse{}, err
	}
	if response.StatusCode == http.StatusSeeOther {
		location := response.Header.Get("Location")
		defer response.Body.Close()
		invocationID, locationErr := client.invocationIDFromLocation(location)
		if locationErr != nil {
			return GatewayCreateResponse{}, newGatewayProtocolError(response.StatusCode)
		}
		return GatewayCreateResponse{
			StatusCode:    response.StatusCode,
			InvocationID:  invocationID,
			Location:      location,
			AlreadyExists: true,
		}, nil
	}
	if response.StatusCode >= 400 {
		return GatewayCreateResponse{}, gatewayErrorFromResponse(response, isGatewayInvocationErrorCode)
	}

	if mode == GatewayDeliveryStream {
		if response.StatusCode != http.StatusOK || !gatewayMediaType(response, "text/event-stream") {
			response.Body.Close()
			return GatewayCreateResponse{}, newGatewayProtocolError(response.StatusCode)
		}
		return GatewayCreateResponse{
			StatusCode: response.StatusCode,
			Stream:     newGatewaySSEStream(response.Body),
		}, nil
	}

	if response.StatusCode != http.StatusAccepted || !gatewayMediaType(response, "application/json") {
		response.Body.Close()
		return GatewayCreateResponse{}, newGatewayProtocolError(response.StatusCode)
	}
	requestID, snapshot, err := decodeGatewaySnapshotResponse(response)
	if err != nil {
		return GatewayCreateResponse{}, err
	}
	location := response.Header.Get("Location")
	locationID, locationErr := client.invocationIDFromLocation(location)
	if locationErr != nil || locationID != snapshot.ID {
		return GatewayCreateResponse{}, newGatewayProtocolError(response.StatusCode)
	}
	return GatewayCreateResponse{
		StatusCode:   response.StatusCode,
		RequestID:    requestID,
		InvocationID: snapshot.ID,
		Location:     location,
		Snapshot:     snapshot,
	}, nil
}

// Get 不带幂等键获取当前 snapshot。
func (client *GatewayClient) Get(ctx context.Context, invocationID string) (GatewayGetResponse, error) {
	return client.GetWithETag(ctx, invocationID, "")
}

// GetWithETag 在 etag 非空时发送 If-None-Match；304 以 NotModified=true 返回，而非错误。
func (client *GatewayClient) GetWithETag(ctx context.Context, invocationID, etag string) (GatewayGetResponse, error) {
	if client == nil {
		return GatewayGetResponse{}, fmt.Errorf("gateway client is not configured")
	}
	path, err := CanonicalGatewayPath(GatewayInvocationGet, invocationID)
	if err != nil {
		return GatewayGetResponse{}, newGatewayError(GatewayInvalidInvocationRequest, 0, "", "invalid invocation id")
	}
	if strings.ContainsAny(etag, "\r\n") {
		return GatewayGetResponse{}, newGatewayError(GatewayInvalidInvocationRequest, 0, "", "invalid ETag")
	}
	extra := http.Header{"Accept": []string{"application/json"}}
	if etag != "" {
		extra.Set("If-None-Match", etag)
	}
	response, err := client.doInvocation(ctx, http.MethodGet, path, string(GatewayInvocationGet), nil, "", extra)
	if err != nil {
		return GatewayGetResponse{}, err
	}
	retryAfter := gatewayRetryAfter(response)
	if response.StatusCode == http.StatusNotModified {
		response.Body.Close()
		responseETag := response.Header.Get("ETag")
		if responseETag == "" {
			responseETag = etag
		}
		return GatewayGetResponse{
			StatusCode:  response.StatusCode,
			NotModified: true,
			ETag:        responseETag,
			RetryAfter:  retryAfter,
		}, nil
	}
	if response.StatusCode >= 400 {
		return GatewayGetResponse{}, gatewayErrorFromResponse(response, isGatewayInvocationErrorCode)
	}
	if response.StatusCode != http.StatusOK || !gatewayMediaType(response, "application/json") {
		response.Body.Close()
		return GatewayGetResponse{}, newGatewayProtocolError(response.StatusCode)
	}
	requestID, snapshot, err := decodeGatewaySnapshotResponse(response)
	if err != nil {
		return GatewayGetResponse{}, err
	}
	responseETag := response.Header.Get("ETag")
	if responseETag == "" || responseETag != strconv.Quote(strconv.FormatInt(snapshot.Version, 10)) {
		return GatewayGetResponse{}, newGatewayProtocolError(response.StatusCode)
	}
	return GatewayGetResponse{
		StatusCode: response.StatusCode,
		RequestID:  requestID,
		Snapshot:   snapshot,
		ETag:       responseETag,
		RetryAfter: retryAfter,
	}, nil
}

// GatewayPoller 维护 ETag，并在下一次 GET 前等待服务端 Retry-After；SSE 断线后不会 POST 或 DELETE。
type GatewayPoller struct {
	client       *GatewayClient
	invocationID string

	mu       sync.Mutex
	etag     string
	nextPoll time.Time
}

// NewPoller 为一个 invocation 创建带 ETag 的轮询器。
func (client *GatewayClient) NewPoller(invocationID string) (*GatewayPoller, error) {
	if client == nil {
		return nil, fmt.Errorf("gateway client is not configured")
	}
	if _, err := CanonicalGatewayPath(GatewayInvocationGet, invocationID); err != nil {
		return nil, newGatewayError(GatewayInvalidInvocationRequest, 0, "", "invalid invocation id")
	}
	return &GatewayPoller{client: client, invocationID: invocationID}, nil
}

// Poll 执行一次 GET，遵守上次 Retry-After 并更新 ETag。
func (poller *GatewayPoller) Poll(ctx context.Context) (GatewayGetResponse, error) {
	if poller == nil || poller.client == nil {
		return GatewayGetResponse{}, fmt.Errorf("gateway poller is not configured")
	}
	poller.mu.Lock()
	defer poller.mu.Unlock()
	if !poller.nextPoll.IsZero() {
		wait := poller.nextPoll.Sub(poller.client.now())
		if wait > 0 {
			timer := time.NewTimer(wait)
			select {
			case <-timer.C:
			case <-gatewayContext(ctx).Done():
				if !timer.Stop() {
					<-timer.C
				}
				return GatewayGetResponse{}, gatewayContext(ctx).Err()
			}
		}
	}
	response, err := poller.client.GetWithETag(ctx, poller.invocationID, poller.etag)
	if err != nil {
		return GatewayGetResponse{}, err
	}
	if response.ETag != "" {
		poller.etag = response.ETag
	}
	if response.RetryAfter > 0 {
		poller.nextPoll = poller.client.now().Add(response.RetryAfter)
	} else {
		poller.nextPoll = time.Time{}
	}
	return response, nil
}

// ETag 返回轮询器当前的 validator。
func (poller *GatewayPoller) ETag() string {
	if poller == nil {
		return ""
	}
	poller.mu.Lock()
	defer poller.mu.Unlock()
	return poller.etag
}

// DownloadArtifact 在向 dst 写入任何字节前校验 Content-Digest。
func (client *GatewayClient) DownloadArtifact(ctx context.Context, invocationID, artifactID string, dst io.Writer) error {
	if client == nil {
		return fmt.Errorf("gateway client is not configured")
	}
	if dst == nil {
		return newGatewayError(GatewayInvalidInvocationRequest, 0, "", "artifact destination is not configured")
	}
	path, err := CanonicalGatewayPath(GatewayInvocationGetArtifact, invocationID, artifactID)
	if err != nil {
		return newGatewayError(GatewayInvalidInvocationRequest, 0, "", "invalid artifact id")
	}
	response, err := client.doInvocation(ctx, http.MethodGet, path, string(GatewayInvocationGetArtifact), nil, "", http.Header{"Accept": []string{"*/*"}})
	if err != nil {
		return err
	}
	if response.StatusCode >= 400 {
		return gatewayErrorFromResponse(response, isGatewayInvocationErrorCode)
	}
	if response.StatusCode >= 300 && response.StatusCode < 400 {
		response.Body.Close()
		return newGatewayProtocolError(response.StatusCode)
	}
	if response.StatusCode != http.StatusOK {
		response.Body.Close()
		return newGatewayProtocolError(response.StatusCode)
	}
	body, readErr := readGatewayResponseBody(response, 0)
	if readErr != nil {
		return newGatewayProtocolError(response.StatusCode)
	}
	if err := verifyGatewayContentDigest(response.Header.Get("Content-Digest"), body); err != nil {
		return err
	}
	_, err = io.Copy(dst, bytes.NewReader(body))
	return err
}

// Cancel 使用调用方持有的幂等键发送无 body 的 DELETE。
func (client *GatewayClient) Cancel(ctx context.Context, invocationID, idempotencyKey string) (GatewayCancelResponse, error) {
	if client == nil {
		return GatewayCancelResponse{}, fmt.Errorf("gateway client is not configured")
	}
	if err := validateGatewayIdempotencyKey(idempotencyKey); err != nil {
		return GatewayCancelResponse{}, newGatewayError(GatewayInvalidInvocationRequest, 0, "", "invalid idempotency key")
	}
	path, err := CanonicalGatewayPath(GatewayInvocationCancel, invocationID)
	if err != nil {
		return GatewayCancelResponse{}, newGatewayError(GatewayInvalidInvocationRequest, 0, "", "invalid invocation id")
	}
	response, err := client.doInvocation(ctx, http.MethodDelete, path, string(GatewayInvocationCancel), []byte{}, idempotencyKey, nil)
	if err != nil {
		return GatewayCancelResponse{}, err
	}
	if response.StatusCode >= 400 {
		return GatewayCancelResponse{}, gatewayErrorFromResponse(response, isGatewayInvocationErrorCode)
	}
	if response.StatusCode != http.StatusOK && response.StatusCode != http.StatusAccepted {
		response.Body.Close()
		return GatewayCancelResponse{}, newGatewayProtocolError(response.StatusCode)
	}
	body, readErr := readGatewayResponseBody(response, 4<<20)
	if readErr != nil {
		return GatewayCancelResponse{}, newGatewayProtocolError(response.StatusCode)
	}
	result := GatewayCancelResponse{StatusCode: response.StatusCode, Accepted: response.StatusCode == http.StatusAccepted}
	if len(bytes.TrimSpace(body)) == 0 {
		if !result.Accepted {
			return GatewayCancelResponse{}, newGatewayProtocolError(response.StatusCode)
		}
		result.InvocationID, _ = client.invocationIDFromLocation(response.Header.Get("Location"))
		return result, nil
	}
	requestID, snapshot, decodeErr := decodeGatewaySnapshotBytes(body, response.StatusCode)
	if decodeErr != nil {
		return GatewayCancelResponse{}, decodeErr
	}
	result.RequestID = requestID
	result.InvocationID = snapshot.ID
	result.Snapshot = snapshot
	return result, nil
}

// GatewaySiteContextClient 与 GatewayClient 刻意分离：请求没有 Bearer token、actor、
// assertion，也不接受调用方传入 site 数据。
type GatewaySiteContextClient struct {
	baseURL    *url.URL
	httpClient *http.Client
}

// NewGatewaySiteContextClient 构造独立的 site-context 客户端。
func NewGatewaySiteContextClient(baseURL string, tlsConfig *tls.Config) (*GatewaySiteContextClient, error) {
	parsedURL, err := parseGatewayBaseURL(baseURL)
	if err != nil {
		return nil, err
	}
	return &GatewaySiteContextClient{baseURL: parsedURL, httpClient: newGatewayHTTPClient(tlsConfig)}, nil
}

// Issue 发送契约要求的空 body site-context 请求。
func (client *GatewaySiteContextClient) Issue(ctx context.Context) (GatewaySiteContext, error) {
	if client == nil {
		return GatewaySiteContext{}, fmt.Errorf("site-context client is not configured")
	}
	request, err := http.NewRequestWithContext(gatewayContext(ctx), http.MethodPost, client.endpoint(gatewaySiteContextPath), nil)
	if err != nil {
		return GatewaySiteContext{}, newGatewayError(GatewayInternalError, 0, "", "could not create site-context request")
	}
	request.Header.Set("Accept", "application/json")
	response, err := client.httpClient.Do(request)
	if err != nil {
		if gatewayContext(ctx).Err() != nil {
			return GatewaySiteContext{}, gatewayContext(ctx).Err()
		}
		return GatewaySiteContext{}, newGatewayError(GatewayInternalError, 0, "", "site-context HTTP request failed")
	}
	if response.StatusCode != http.StatusOK {
		if response.StatusCode >= 400 {
			return GatewaySiteContext{}, gatewayErrorFromResponse(response, isGatewaySiteContextErrorCode)
		}
		response.Body.Close()
		return GatewaySiteContext{}, newGatewayProtocolError(response.StatusCode)
	}
	if !gatewayMediaType(response, "application/json") {
		response.Body.Close()
		return GatewaySiteContext{}, newGatewayProtocolError(response.StatusCode)
	}
	body, readErr := readGatewayResponseBody(response, 1<<20)
	if readErr != nil {
		return GatewaySiteContext{}, newGatewayProtocolError(response.StatusCode)
	}
	var wire struct {
		RequestID        string `json:"request_id"`
		SiteContextToken string `json:"site_context_token"`
		ExpiresAtMS      int64  `json:"expires_at_ms"`
	}
	if err := json.Unmarshal(body, &wire); err != nil || wire.RequestID == "" || wire.SiteContextToken == "" || wire.ExpiresAtMS <= 0 {
		return GatewaySiteContext{}, newGatewayProtocolError(response.StatusCode)
	}
	return GatewaySiteContext{
		RequestID:        wire.RequestID,
		SiteContextToken: newSecretString(wire.SiteContextToken),
		ExpiresAtMS:      wire.ExpiresAtMS,
	}, nil
}

func (client *GatewayClient) doInvocation(ctx context.Context, method, path, operation string, body []byte, idempotencyKey string, extra http.Header) (*http.Response, error) {
	ctx = gatewayContext(ctx)
	actor, err := client.resolveActor(ctx)
	if err != nil {
		return nil, err
	}
	for attempt := 0; attempt < 2; attempt++ {
		token, err := client.tokens.Token(ctx)
		if err != nil {
			return nil, err
		}
		accessToken := token.AccessToken()
		if token.TokenType() != "Bearer" || accessToken == "" {
			return nil, newGatewayError(GatewayInternalError, 0, "", "token source returned an invalid token")
		}
		assertion, _, err := SignAssertion(client.signer, AssertionInput{
			InstanceID:     client.identity.InstanceID,
			TenantID:       client.identity.TenantID,
			SessionID:      client.identity.SessionID,
			Actor:          actor,
			Operation:      operation,
			Method:         method,
			CanonicalPath:  path,
			Body:           body,
			IdempotencyKey: idempotencyKey,
			IssuedAt:       client.now(),
			TTL:            60 * time.Second,
		})
		if err != nil {
			return nil, err
		}
		request, err := http.NewRequestWithContext(ctx, method, client.endpoint(path), bytes.NewReader(body))
		if err != nil {
			return nil, newGatewayError(GatewayInternalError, 0, "", "could not create gateway request")
		}
		for key, values := range extra {
			for _, value := range values {
				request.Header.Add(key, value)
			}
		}
		request.Header.Set("Authorization", "Bearer "+accessToken)
		request.Header.Set("X-Sluice-Actor", actor)
		request.Header.Set("X-Sluice-Actor-Assertion", assertion.Compact())
		if idempotencyKey != "" {
			request.Header.Set("Idempotency-Key", idempotencyKey)
		}
		if method == http.MethodPost {
			request.Header.Set("Content-Type", "application/json")
		}
		response, err := client.httpClient.Do(request)
		if err != nil {
			if ctx.Err() != nil {
				return nil, ctx.Err()
			}
			return nil, newGatewayError(GatewayInternalError, 0, "", "gateway HTTP request failed")
		}
		if response.StatusCode != http.StatusUnauthorized {
			return response, nil
		}
		gatewayErr := gatewayErrorFromResponse(response, isGatewayInvocationErrorCode)
		if gatewayErr.Code != GatewayRuntimeUnauthenticated || attempt != 0 {
			return nil, gatewayErr
		}
		invalidator, ok := client.tokens.(TokenInvalidator)
		if !ok {
			return nil, gatewayErr
		}
		invalidator.Invalidate()
	}
	return nil, newGatewayError(GatewayInternalError, 0, "", "gateway request retry exhausted")
}

func (client *GatewayClient) resolveActor(ctx context.Context) (string, error) {
	if client.identity.ActorFunc != nil {
		actor, err := client.identity.ActorFunc(ctx)
		if err != nil {
			return "", err
		}
		return actor, nil
	}
	return client.identity.Actor, nil
}

func (client *GatewayClient) endpoint(path string) string {
	copyURL := *client.baseURL
	copyURL.Path = strings.TrimRight(client.baseURL.Path, "/") + path
	copyURL.RawPath = ""
	copyURL.RawQuery = ""
	copyURL.Fragment = ""
	return copyURL.String()
}

func (client *GatewaySiteContextClient) endpoint(path string) string {
	copyURL := *client.baseURL
	copyURL.Path = strings.TrimRight(client.baseURL.Path, "/") + path
	copyURL.RawPath = ""
	copyURL.RawQuery = ""
	copyURL.Fragment = ""
	return copyURL.String()
}

func (client *GatewayClient) invocationIDFromLocation(location string) (string, error) {
	if location == "" {
		return "", fmt.Errorf("gateway Location is empty")
	}
	parsed, err := url.Parse(location)
	if err != nil || parsed.Path == "" || parsed.RawQuery != "" || parsed.Fragment != "" {
		return "", fmt.Errorf("gateway Location is invalid")
	}
	if parsed.IsAbs() && (parsed.Scheme != client.baseURL.Scheme || parsed.Host != client.baseURL.Host) {
		return "", fmt.Errorf("gateway Location has an unexpected origin")
	}
	prefix := strings.TrimRight(client.baseURL.Path, "/") + gatewayInvocationPrefix + "/"
	if !strings.HasPrefix(parsed.Path, prefix) {
		return "", fmt.Errorf("gateway Location is not an invocation path")
	}
	id := strings.TrimPrefix(parsed.Path, prefix)
	if strings.Contains(id, "/") {
		return "", fmt.Errorf("gateway Location has an invalid invocation path")
	}
	if _, err := CanonicalGatewayPath(GatewayInvocationGet, id); err != nil {
		return "", err
	}
	return id, nil
}

func parseGatewayBaseURL(raw string) (*url.URL, error) {
	parsed, err := url.Parse(raw)
	if err != nil || parsed.Scheme != "https" || parsed.Host == "" || parsed.User != nil || parsed.RawQuery != "" || parsed.Fragment != "" {
		return nil, fmt.Errorf("gateway base URL must be an https origin")
	}
	return parsed, nil
}

func newGatewayHTTPClient(tlsConfig *tls.Config) *http.Client {
	transport, ok := http.DefaultTransport.(*http.Transport)
	if ok {
		transport = transport.Clone()
	} else {
		transport = &http.Transport{}
	}
	if tlsConfig != nil {
		transport.TLSClientConfig = tlsConfig.Clone()
	}
	return &http.Client{
		Transport: transport,
		CheckRedirect: func(_ *http.Request, _ []*http.Request) error {
			return http.ErrUseLastResponse
		},
	}
}

func gatewayContext(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func gatewayMediaType(response *http.Response, expected string) bool {
	mediaType, _, err := mime.ParseMediaType(response.Header.Get("Content-Type"))
	return err == nil && mediaType == expected
}

func gatewayRetryAfter(response *http.Response) time.Duration {
	seconds, err := strconv.ParseInt(strings.TrimSpace(response.Header.Get("Retry-After")), 10, 64)
	if err != nil || seconds < 0 {
		return 0
	}
	if seconds > int64((time.Duration(1<<63-1))/time.Second) {
		return 0
	}
	return time.Duration(seconds) * time.Second
}

func validateGatewayIdempotencyKey(key string) error {
	if len(key) < 16 || len(key) > 128 {
		return fmt.Errorf("idempotency key length is outside 16..128")
	}
	for _, character := range []byte(key) {
		if (character >= 'A' && character <= 'Z') ||
			(character >= 'a' && character <= 'z') ||
			(character >= '0' && character <= '9') ||
			character == '.' || character == '_' || character == ':' || character == '-' {
			continue
		}
		return fmt.Errorf("idempotency key contains a forbidden character")
	}
	return nil
}

func validateGatewayParameters(raw []byte) error {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("spec.parameters is not valid JSON")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("spec.parameters contains trailing JSON")
	}
	if _, ok := value.(map[string]any); !ok {
		return fmt.Errorf("spec.parameters must be a JSON object")
	}
	if gatewayJSONContainsNumber(value) {
		return fmt.Errorf("spec.parameters numeric values must be decimal strings")
	}
	return nil
}

func gatewayJSONContainsNumber(value any) bool {
	switch current := value.(type) {
	case json.Number:
		return true
	case []any:
		for _, item := range current {
			if gatewayJSONContainsNumber(item) {
				return true
			}
		}
	case map[string]any:
		for _, item := range current {
			if gatewayJSONContainsNumber(item) {
				return true
			}
		}
	}
	return false
}

func gatewayJSONNull(raw []byte) bool { return bytes.Equal(bytes.TrimSpace(raw), []byte("null")) }

func validateGatewaySnapshot(snapshot *GatewayInvocationSnapshot, requestID string) error {
	// version 是服务端单调递增的乐观锁版本，从 1 起；<=0 表示字段缺失或被伪造。
	if snapshot == nil || snapshot.ID == "" || snapshot.Version <= 0 || snapshot.SKU == "" || snapshot.TaskRef == "" {
		return fmt.Errorf("snapshot identity fields are incomplete")
	}
	if !isGatewaySnapshotState(snapshot.State) {
		return fmt.Errorf("snapshot state is not registered")
	}
	if snapshot.Terminal != isGatewayTerminalState(snapshot.State) {
		return fmt.Errorf("snapshot terminal flag does not match state")
	}
	if snapshot.ReservedUnits != nil && !isGatewayDecimalString(*snapshot.ReservedUnits) {
		return fmt.Errorf("snapshot reserved_units is not a decimal string")
	}
	if snapshot.SettledUnits != nil && !isGatewayDecimalString(*snapshot.SettledUnits) {
		return fmt.Errorf("snapshot settled_units is not a decimal string")
	}
	if snapshot.State != GatewayStateCompleted && gatewayJSONValueNonEmpty(snapshot.Result) {
		return fmt.Errorf("snapshot result is only allowed for completed invocations")
	}
	if snapshot.State != GatewayStateCompleted && gatewayJSONValueNonEmpty(snapshot.LotDeductions) {
		return fmt.Errorf("snapshot lot_deductions is only allowed for completed invocations")
	}
	if err := validateGatewayLotDeductions(snapshot.LotDeductions); err != nil {
		return err
	}
	if snapshot.Error != nil {
		if !isGatewayInvocationErrorCode(snapshot.Error.Code) {
			snapshot.Error.Code = GatewayInternalError
			snapshot.Error.Message = "unrecognized invocation error code"
		}
		snapshot.Error.HTTPStatus = 0
		snapshot.Error.RequestID = requestID
		if snapshot.Error.InvocationID == "" {
			snapshot.Error.InvocationID = snapshot.ID
		}
	}
	return nil
}

func validateGatewayLotDeductions(raw json.RawMessage) error {
	if !gatewayJSONValueNonEmpty(raw) {
		return nil
	}
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return fmt.Errorf("snapshot lot_deductions is not valid JSON")
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return fmt.Errorf("snapshot lot_deductions contains trailing JSON")
	}
	if err := validateGatewayUnitFields(value); err != nil {
		return err
	}
	return nil
}

func validateGatewayUnitFields(value any) error {
	switch current := value.(type) {
	case []any:
		for _, item := range current {
			if err := validateGatewayUnitFields(item); err != nil {
				return err
			}
		}
	case map[string]any:
		for key, item := range current {
			if key == "units" || strings.HasSuffix(key, "_units") {
				switch unit := item.(type) {
				case nil:
				case string:
					if !isGatewayDecimalString(unit) {
						return fmt.Errorf("snapshot unit field is not a decimal string")
					}
				default:
					return fmt.Errorf("snapshot unit field must be a decimal string or null")
				}
			}
			if err := validateGatewayUnitFields(item); err != nil {
				return err
			}
		}
	case json.Number:
		return nil
	}
	return nil
}

func isGatewaySnapshotState(state GatewayInvocationState) bool {
	switch state {
	case GatewayStateAccepted, GatewayStateRunning, GatewayStateCancelPending,
		GatewayStateReconciling, GatewayStateSettlementShortfall,
		GatewayStateCompleted, GatewayStateFailed, GatewayStateCancelled:
		return true
	default:
		return false
	}
}

func isGatewayTerminalState(state GatewayInvocationState) bool {
	switch state {
	case GatewayStateCompleted, GatewayStateFailed, GatewayStateCancelled:
		return true
	default:
		return false
	}
}

func isGatewayDecimalString(value string) bool {
	if value == "" {
		return false
	}
	for _, character := range []byte(value) {
		if character < '0' || character > '9' {
			return false
		}
	}
	return true
}

func gatewayJSONValueNonEmpty(raw json.RawMessage) bool {
	return len(bytes.TrimSpace(raw)) > 0 && !gatewayJSONNull(raw)
}

func decodeGatewaySnapshotResponse(response *http.Response) (string, *GatewayInvocationSnapshot, error) {
	body, err := readGatewayResponseBody(response, 4<<20)
	if err != nil {
		return "", nil, newGatewayProtocolError(response.StatusCode)
	}
	return decodeGatewaySnapshotBytes(body, response.StatusCode)
}

func decodeGatewaySnapshotBytes(body []byte, status int) (string, *GatewayInvocationSnapshot, error) {
	var envelope struct {
		RequestID  string                     `json:"request_id"`
		Invocation *GatewayInvocationSnapshot `json:"invocation"`
	}
	if err := json.Unmarshal(body, &envelope); err != nil || envelope.RequestID == "" || envelope.Invocation == nil {
		return "", nil, newGatewayProtocolError(status)
	}
	if err := validateGatewaySnapshot(envelope.Invocation, envelope.RequestID); err != nil {
		return "", nil, newGatewayProtocolError(status)
	}
	return envelope.RequestID, envelope.Invocation, nil
}

func readGatewayResponseBody(response *http.Response, maxBytes int64) ([]byte, error) {
	if response == nil || response.Body == nil {
		return nil, fmt.Errorf("gateway response body is unavailable")
	}
	defer response.Body.Close()
	reader := io.Reader(response.Body)
	if maxBytes > 0 {
		reader = io.LimitReader(response.Body, maxBytes+1)
	}
	body, err := io.ReadAll(reader)
	if err != nil {
		return nil, err
	}
	if maxBytes > 0 && int64(len(body)) > maxBytes {
		return nil, fmt.Errorf("gateway response body is too large")
	}
	return body, nil
}

func gatewayErrorFromResponse(response *http.Response, allowed func(string) bool) *GatewayError {
	if response == nil {
		return newGatewayError(GatewayInternalError, 0, "", "gateway response is unavailable")
	}
	body, err := readGatewayResponseBody(response, 4<<20)
	if err != nil {
		return newGatewayError(GatewayInternalError, response.StatusCode, "", "gateway error response is unreadable")
	}
	return gatewayErrorFromBytes(body, response.StatusCode, allowed)
}

func verifyGatewayContentDigest(header string, body []byte) error {
	header = strings.TrimSpace(header)
	if !strings.HasPrefix(header, "sha-256=:") || !strings.HasSuffix(header, ":") {
		return newGatewayProtocolError(http.StatusOK)
	}
	encoded := strings.TrimSuffix(strings.TrimPrefix(header, "sha-256=:"), ":")
	want, err := base64.StdEncoding.DecodeString(encoded)
	if err != nil || len(want) != sha256.Size {
		return newGatewayProtocolError(http.StatusOK)
	}
	digest := sha256.Sum256(body)
	if !bytes.Equal(want, digest[:]) {
		return newGatewayProtocolError(http.StatusOK)
	}
	return nil
}
