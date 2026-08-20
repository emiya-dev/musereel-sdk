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
	"sort"
	"strconv"
	"strings"
	"sync"
	"time"

	"github.com/emiya-dev/musereel-sdk/jcs"
)

// GatewayDeliveryMode is determined by the Gateway-side SKU catalog. The SDK
// exposes separate methods for the delivery modes; delivery_mode is not put in
// the request body and is never silently overridden by the SDK.
type GatewayDeliveryMode string

const (
	GatewayDeliveryStream GatewayDeliveryMode = "stream"
	GatewayDeliveryAsync  GatewayDeliveryMode = "async"
)

// GatewayInvocationState is the closed set of states used by invocation
// snapshots.
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

// GatewayActorFunc supplies the actor at request time. It is resolved once per
// logical request, and a token-refresh retry reuses the same value.
type GatewayActorFunc func(context.Context) (string, error)

// GatewayIdentity is the token-bound identity context required by
// SignAssertion for Gateway requests.
type GatewayIdentity struct {
	InstanceID string
	TenantID   string
	SessionID  string
	Actor      string
	ActorFunc  GatewayActorFunc
}

// GatewayInvocationSpec is the frozen spec object. Input and Parameters use
// interface values, so callers may provide json.RawMessage or ordinary Go JSON
// values. Validate rejects JSON numbers that cannot be canonicalized
// (fractions, exponent notation, or values outside int64); integer forms are
// accepted.
type GatewayInvocationSpec struct {
	SchemaVersion string `json:"schema_version"`
	Input         any    `json:"input"`
	Parameters    any    `json:"parameters"`
}

// GatewayCreateRequest contains exactly the four create-body fields and does
// not contain delivery_mode; the delivery mode is a SKU catalog attribute.
type GatewayCreateRequest struct {
	SKU               string                `json:"sku_id"`
	TaskRef           string                `json:"task_ref"`
	Spec              GatewayInvocationSpec `json:"spec"`
	ModerationReceipt string                `json:"moderation_receipt"`
}

// Validate checks the request shape before the SDK creates a token or
// assertion for the request.
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
	// input 与 parameters 走同一关：assertion digest 规范化的是**整个请求体**
	// （assertion.go 的 jcs.CanonicalizeJSON(body)），只查 parameters 会让 input
	// 里的同类数值躲到 digest 那一步才炸，而那一步的报错不带字段路径。
	if err := validateGatewayInput(input); err != nil {
		return err
	}
	if err := validateGatewayParameters(parameters); err != nil {
		return err
	}
	return nil
}

// GatewayInvocationSnapshot is shared by async create, GET, and cancel
// responses. Result and LotDeductions remain raw JSON; the SDK does not convert
// units or similar amount-like values to floating-point numbers.
type GatewayInvocationSnapshot struct {
	ID string `json:"id"`
	// Version is int64 because the server's respond.go and the 06 contract
	// example ("version": 1) both encode it as a JSON number. Declaring it a string
	// made every real Gateway snapshot fail to decode (async create 202, GET 200,
	// and cancel all became protocol errors); the SDK fixtures once sent a quoted
	// value and therefore incorrectly reinforced that assumption.
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

// GatewayCreateResponse is shared by both create modes. A 303 response is
// represented by AlreadyExists=true and a non-empty InvocationID with a nil
// error; for stream mode, the caller owns and must close Stream.
type GatewayCreateResponse struct {
	StatusCode    int
	RequestID     string
	InvocationID  string
	Location      string
	AlreadyExists bool
	Snapshot      *GatewayInvocationSnapshot
	Stream        *GatewaySSEStream
}

// GatewayGetResponse contains either a new snapshot or a 304 result. ETag and
// RetryAfter are metadata used by GatewayPoller.
type GatewayGetResponse struct {
	StatusCode  int
	RequestID   string
	Snapshot    *GatewayInvocationSnapshot
	NotModified bool
	ETag        string
	RetryAfter  time.Duration
}

// GatewayCancelResponse represents either a newly accepted cancellation intent
// (202) or the current snapshot (200).
type GatewayCancelResponse struct {
	StatusCode   int
	RequestID    string
	InvocationID string
	Accepted     bool
	Snapshot     *GatewayInvocationSnapshot
}

type gatewayClientConfig struct {
	now Clock
}

// GatewayClientOption customizes SDK-local behavior only.
type GatewayClientOption func(*gatewayClientConfig)

// WithGatewayClock injects the clock used for assertion issuance and polling
// metadata tests. It does not change the maximum 60-second wire TTL.
func WithGatewayClock(now Clock) GatewayClientOption {
	return func(config *gatewayClientConfig) {
		if now != nil {
			config.now = now
		}
	}
}

// GatewayClient wraps the four authenticated Gateway invocation routes.
type GatewayClient struct {
	baseURL    *url.URL
	tokens     TokenSource
	signer     Signer
	identity   GatewayIdentity
	httpClient *http.Client
	now        Clock
}

// NewGatewayClient constructs an authenticated Gateway HTTP client. Callers
// provide TLS configuration from NewTLSConfig and the token and signer
// abstractions shared by the rest of the SDK.
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

// CreateStream sends a stream-mode create request and returns its SSE stream.
func (client *GatewayClient) CreateStream(ctx context.Context, request GatewayCreateRequest, idempotencyKey string) (GatewayCreateResponse, error) {
	return client.create(ctx, request, idempotencyKey, GatewayDeliveryStream)
}

// CreateAsync sends an async-mode create request and returns its snapshot.
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

	if response.StatusCode != http.StatusAccepted {
		response.Body.Close()
		return GatewayCreateResponse{}, newGatewayProtocolError(response.StatusCode)
	}
	location := response.Header.Get("Location")
	locationID, locationErr := client.invocationIDFromLocation(location)
	if !gatewayMediaType(response, "application/json") {
		response.Body.Close()
		if locationErr == nil {
			return GatewayCreateResponse{}, newGatewayProtocolErrorWithInvocationID(response.StatusCode, locationID)
		}
		return GatewayCreateResponse{}, newGatewayProtocolError(response.StatusCode)
	}
	requestID, snapshot, err := decodeGatewaySnapshotResponse(response)
	if err != nil {
		if gatewayErr, ok := err.(*GatewayError); ok && locationErr == nil {
			gatewayErr.InvocationID = locationID
		}
		return GatewayCreateResponse{}, err
	}
	if locationErr != nil || locationID != snapshot.ID {
		// 两边都可能带得出 ID，优先信 Location（它是契约点名的那个出口，
		// 06:542-543）；Location 解不出来时回落到 body——此时 snapshot 已经过
		// validateGatewaySnapshot，snapshot.ID 是校验过的合法 ID，扔掉它等于
		// 让调用方在「invocation 明明已落库」的情况下只能盲目重提。
		recoveredID := snapshot.ID
		if locationErr == nil {
			recoveredID = locationID
		}
		return GatewayCreateResponse{}, newGatewayProtocolErrorWithInvocationID(response.StatusCode, recoveredID)
	}
	return GatewayCreateResponse{
		StatusCode:   response.StatusCode,
		RequestID:    requestID,
		InvocationID: snapshot.ID,
		Location:     location,
		Snapshot:     snapshot,
	}, nil
}

// Get retrieves the current snapshot without an idempotency key.
//
// This method is intentionally not dead code. An SDK-013 reconciliation
// review once misclassified it as unused: Sluice's integration rehearsal
// module (test/rehearsal, a separate Go module that replaces this repository
// through a sibling directory) has real call sites in golden/golden_test.go and
// golden/completed_test.go, and Sluice's ci.sh full harness compiles and runs
// that module. Removing Get would not fail this repository's own gates, but it
// would make another repository's gate fail to compile. It therefore remains
// public because it has a consumer.
func (client *GatewayClient) Get(ctx context.Context, invocationID string) (GatewayGetResponse, error) {
	return client.GetWithETag(ctx, invocationID, "")
}

// GetWithETag sends If-None-Match when etag is non-empty. A 304 response is
// returned with NotModified=true rather than as an error.
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

// GatewayPoller maintains the ETag and waits for the server's Retry-After
// before the next GET. An SSE disconnect does not cause it to POST or DELETE.
type GatewayPoller struct {
	client       *GatewayClient
	invocationID string

	mu       sync.Mutex
	etag     string
	nextPoll time.Time
}

// NewPoller creates an ETag-aware poller for an invocation.
func (client *GatewayClient) NewPoller(invocationID string) (*GatewayPoller, error) {
	if client == nil {
		return nil, fmt.Errorf("gateway client is not configured")
	}
	if _, err := CanonicalGatewayPath(GatewayInvocationGet, invocationID); err != nil {
		return nil, newGatewayError(GatewayInvalidInvocationRequest, 0, "", "invalid invocation id")
	}
	return &GatewayPoller{client: client, invocationID: invocationID}, nil
}

// Poll performs one GET, observes the previous Retry-After, and updates the
// ETag.
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

// ETag returns the poller's current validator.
func (poller *GatewayPoller) ETag() string {
	if poller == nil {
		return ""
	}
	poller.mu.Lock()
	defer poller.mu.Unlock()
	return poller.etag
}

// DownloadArtifact verifies Content-Digest before writing any bytes to dst.
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

// Cancel sends a bodyless DELETE using the idempotency key held by the caller.
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
	value, err := decodeGatewayJSONValue(raw, "spec.parameters")
	if err != nil {
		return err
	}
	if _, ok := value.(map[string]any); !ok {
		return fmt.Errorf("spec.parameters must be a JSON object")
	}
	return validateGatewayCanonicalizable(raw, value, "spec.parameters")
}

func validateGatewayInput(raw []byte) error {
	value, err := decodeGatewayJSONValue(raw, "spec.input")
	if err != nil {
		return err
	}
	return validateGatewayCanonicalizable(raw, value, "spec.input")
}

// validateGatewayCanonicalizable 的判据**就是 jcs 本身**，不是它的一份复刻。
//
// 早先这里自己重新实现了「小数 / 指数 / 超 int64 一律拒」的规则，声称与 jcs 子集同源。
// 对 JSON 数字那一维确实等价，但 jcs 的拒绝面比数字大：`{"a":1,"a":2}` 这种重复键
// jcs 硬拒（jcs.go 走 token 流），而 encoding/json 是**末键静默覆盖**，于是复刻版放行、
// 真发请求时在算 digest 那步才炸。复刻一份判据就会有这种缝，所以改成直接问 jcs。
//
// 遍历的职责因此收缩成一件事：**在 jcs 说"不行"之后，把它定位到具体字段**。
// jcs 的原始报错（"json number must be integer decimal string form"）不带任何字段名，
// 而中枢那边同一条错误会被 create.go 改写成 fields=["request"]，更没有定位价值。
// ⚠ 已知代价：`CanonicalizeJSON` 会**完整重解析并重建** canonical 串，而 Validate 对
// input 与 parameters 各跑一次，加上签名路径对整包再跑一次，一次 create 共三次全量 JCS。
// 二次审查两路都点了这条，grok 路实测：8MiB 的 input 单次 canonicalize 约 81ms，
// 线性外推到中枢允许的 64MB body 约 0.6s + 一棵额外的树 + 一份等长的串。
//
// 本轮**接受**这个代价：相对一次 video 生成的时长可忽略，而换到的是「过不了规范化的
// 请求在本地就被按字段拦下」。⇒ 如果将来大 body 真成为问题，正确的修法是**给 jcs 包加
// 一个只校验不重建的入口**，让这里改调它；不要在这里按 body 大小切换判据——那会让
// 「这一关查不查重复键」变成 body 大小的函数，是一个新的隐含前提。
func validateGatewayCanonicalizable(raw []byte, value any, path string) error {
	_, canonicalErr := jcs.CanonicalizeJSON(raw)
	if canonicalErr == nil {
		return nil
	}
	// 🔴 只有当 jcs 拒的**确实是数字**时才去定位字段。
	//
	// 否则会报错错位：`{"a":2,"a":1.5}` 的真实拒因是重复键（jcs 在解码第二个 value
	// 之前就拒了），但 decodeGatewayJSONValue 走 encoding/json 是末键覆盖，树里只剩
	// `a: 1.5`，于是定位器兴高采烈地报「a 是小数」——接入方改完小数，重复键还在，
	// 再失败一次。二次审查（composer 路第二轮）实测到这条。
	//
	// 判错误类型用字符串匹配不优雅，但这是**中枢自己的做法**
	// （sluice 侧 gateway/fingerprint.go:699 与 invocation/spec.go:170 都是
	// `strings.Contains(err.Error(), "json number must be integer decimal string form")`），
	// 跟着同一份 jcs 实现走比自己另发明一套判别更不容易漂。
	if isJCSNumberRejection(canonicalErr) {
		if located := locateUncanonicalizableNumber(value, path); located != nil {
			return located
		}
	}
	// 拒因不是数字（重复键、坏 UTF-8、未配对代理项），或是数字但已被末键覆盖而定位不到：
	// 透传 jcs 的原因，至少说清是 input 还是 parameters 那一段。
	return fmt.Errorf("%s cannot be canonicalized by the gateway: %w", path, canonicalErr)
}

// isJCSNumberRejection 判断 jcs 的拒因是不是「这个 JSON 数字不合规范」。
// 两条串对应 jcs/jcs.go 对 json.Number 的两条限制；那个包是中枢冻结参考实现的镜像，
// 串一变这里就要跟着变——TestGatewayNumberRejectionStringsStillMatchJCS 钉住这一点。
func isJCSNumberRejection(err error) bool {
	if err == nil {
		return false
	}
	message := err.Error()
	return strings.Contains(message, "json number must be integer decimal string form") ||
		strings.Contains(message, "json integer out of int64 range")
}

func decodeGatewayJSONValue(raw []byte, path string) (any, error) {
	decoder := json.NewDecoder(bytes.NewReader(raw))
	decoder.UseNumber()
	var value any
	if err := decoder.Decode(&value); err != nil {
		return nil, fmt.Errorf("%s is not valid JSON", path)
	}
	if decoder.Decode(&struct{}{}) != io.EOF {
		return nil, fmt.Errorf("%s contains trailing JSON", path)
	}
	return value, nil
}

// locateUncanonicalizableNumber 在 jcs 判定这段 JSON 过不了规范化之后，找出**是哪个
// 数字**害的并给出字段路径；找不到（拒因不是数字）返回 nil，由调用方透传 jcs 的原因。
//
// 为什么这一关判「能不能规范化」而不是「这个字段是不是 decimal_string」：
// 后者需要 SDK 里维护一份逐 SKU 请求 schema 的手抄本，而中枢那份是**按运营配置
// 动态派生**的（querysurface/public_schema.go 里 publicParameterRule 会把任何配了
// min/max 的 param_schema 字段渲染成 decimal_string），既抄不全也追不上：
// 运营在 console 加一个参数，SDK 就静默漏掉一个字段。实测漏网的至少有 text 的
// temperature（顶层）与 music 的 audio_setting.sample_rate / bitrate（嵌套两层，
// 只认顶层的白名单结构上就够不到）。
//
// 「能不能规范化」这个判据则与 jcs 子集**同源**——见 jcs/jcs.go 对 json.Number 的
// 两条限制，那个包镜像的是中枢 backend/pkg/app/core/jcs.go 的现行实现，跟着它走就
// 没有跨仓漂移。⚠ 这里原先引的是 contract-input/reference/jcs-server-reference.go.txt，
// 那份副本已删除：它在 key 排序上已经 stale（还写着 sort.Strings 的 UTF-8 序，而两边
// 现行实现都是 UTF-16 序），而 pin 门禁根本没哈希它——照它走恰恰会漂移。
// 中枢对同一条错误自己也是这么判的
// （backend/pkg/app/core/jcs.go 拒小数，gateway 侧把它翻译成
// 「schema 中的 JSON 数字不能使用小数或指数，请改用整数十进制」）。
//
// ⚠ 这一关**不冒充 schema 校验**：整数形态的 JSON 数字一律放行，哪怕该字段声明为
// decimal_string（例：image_count: 4）。那种错由中枢拒——它知道 const / min / max /
// enum，报错比 SDK 猜的准。SDK 只负责拦住「连规范化都过不去、否则要到算 digest
// 时才炸且不带路径」的那一类。
func locateUncanonicalizableNumber(value any, path string) error {
	switch current := value.(type) {
	case json.Number:
		text := current.String()
		if strings.ContainsAny(text, ".eE") {
			// ⚠ 不要在这里笼统建议「改传字符串」，两种情形的正确做法不同：
			//
			// ① response_format 子树里的数字是 **JSON Schema 关键字**（minimum、maxItems…）。
			//    把它改成字符串就不是合法的 JSON Schema 了——那条建议会把人带沟里。
			//    中枢对这条有专用文案「schema 中的 JSON 数字不能使用小数或指数，请改用整数十进制」。
			// ② 其余字段接不接受 JSON 字符串取决于它在目录里的声明：decimal_string 要字符串
			//    （text 的 temperature 就是这么发的），而声明为 number / integer 的字段拿到
			//    字符串会被中枢的手写校验当场拒（首字节不是数字或负号即拒）。
			if strings.Contains(path, ".response_format") {
				return fmt.Errorf(
					"%s is the JSON number %s, which the gateway cannot canonicalize: request fingerprints use an "+
						"integer-only JCS subset. This value is a JSON Schema keyword inside response_format, so it "+
						"must be written in integer decimal notation (quoting it would not be valid JSON Schema)",
					path, text)
			}
			// ⚠ 不要把被拒的 token 原文 %q 进建议里当"照抄这个"——中枢的 decimal_string
			// 要的是**规范十进制形式**，判据是 `strconv.FormatInt(parsed, 10) == raw` 逐字相等
			// （video.go:591-593、image_spec.go:106-108 等）。把 1e3 建议成 "1e3"、
			// 把 44100.0 建议成 "44100.0"，接入方照做会被中枢换一种方式再拒一次。
			// 所以这里给**形式示范**，不回填原文。
			return fmt.Errorf(
				"%s is the JSON number %s, which the gateway cannot canonicalize: request fingerprints use an "+
					"integer-only JCS subset. Check this SKU's request schema: decimal_string fields take a JSON "+
					"string in canonical decimal form (no fractional part, no exponent); integer/number "+
					"fields take an integer",
				path, text)
		}
		if _, err := strconv.ParseInt(text, 10, 64); err != nil {
			return fmt.Errorf(
				"%s is the JSON number %s, which is outside the int64 range the gateway can canonicalize",
				path, text)
		}
	case []any:
		for index, item := range current {
			if err := locateUncanonicalizableNumber(item, fmt.Sprintf("%s[%d]", path, index)); err != nil {
				return err
			}
		}
	case map[string]any:
		// 按键排序遍历：同一个坏请求必须每次都报同一个字段。Go 的 map 迭代顺序是
		// 随机的，不排序的话「含两个坏字段的请求报哪一个」每次都可能不同——
		// 对接方会看到飘忽的报错，测试也会间歇性红。
		keys := make([]string, 0, len(current))
		for key := range current {
			keys = append(keys, key)
		}
		sort.Strings(keys)
		for _, key := range keys {
			if err := locateUncanonicalizableNumber(current[key], path+"."+key); err != nil {
				return err
			}
		}
	}
	return nil
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

// gatewayJSONValueNonEmpty 判断一个 JSON 值是否携带**实质内容**。
//
// ⚠ 空数组 `[]` 与空对象 `{}` 一律算**空**：它们表示「字段在、但没有内容」，
// 不是「有内容」。原实现只看字节长度与 null，于是 `[]`（长度 2）被判成非空——
// 而网关对未完成的 invocation 恒发 `"lot_deductions": []`
// （契约 06 §4.2 的 snapshot 示例即如此）。后果是 validateGatewaySnapshot 命中
// 「非 completed 不得有 lot_deductions」而拒绝，**SDK 的每一次 async create 都失败在 202 上**，
// 且错误被折叠成 internal_error，从错误信息里完全看不出真正原因。
func gatewayJSONValueNonEmpty(raw json.RawMessage) bool {
	trimmed := bytes.TrimSpace(raw)
	if len(trimmed) == 0 || gatewayJSONNull(raw) {
		return false
	}
	var value any
	if err := json.Unmarshal(trimmed, &value); err != nil {
		// 解析不了就当它有内容，交给下游的严格校验去拒绝；不在这里静默放行畸形载荷。
		return true
	}
	switch typed := value.(type) {
	case nil:
		return false
	case []any:
		return len(typed) > 0
	case map[string]any:
		return len(typed) > 0
	default:
		return true
	}
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
