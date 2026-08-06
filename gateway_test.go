package musereelsdk

import (
	"bytes"
	"context"
	"crypto/ed25519"
	"crypto/sha256"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"
)

type gatewayTestTokenSource struct {
	token       Token
	invalidates atomic.Int32
	requests    atomic.Int32
}

func (source *gatewayTestTokenSource) Token(context.Context) (Token, error) {
	source.requests.Add(1)
	return source.token, nil
}

func (source *gatewayTestTokenSource) Invalidate() { source.invalidates.Add(1) }

func newGatewayTestClient(t *testing.T, handler http.Handler) (*GatewayClient, *httptest.Server, *gatewayTestTokenSource, ed25519.PublicKey) {
	t.Helper()
	server := httptest.NewTLSServer(handler)
	t.Cleanup(server.Close)
	token, err := NewToken("access-token-secret", "Bearer", time.Now().Add(time.Hour))
	if err != nil {
		t.Fatalf("NewToken: %v", err)
	}
	tokens := &gatewayTestTokenSource{token: token}
	privateKey := ed25519.NewKeyFromSeed(bytes.Repeat([]byte{7}, ed25519.SeedSize))
	signer, err := NewEd25519Signer("kid-gateway-test", privateKey)
	if err != nil {
		t.Fatalf("NewEd25519Signer: %v", err)
	}
	client, err := NewGatewayClient(server.URL, server.Client().Transport.(*http.Transport).TLSClientConfig, tokens, signer, GatewayIdentity{
		InstanceID: "instance-01",
		TenantID:   "tenant-01",
		SessionID:  "session-01",
		Actor:      "user-01",
	})
	if err != nil {
		t.Fatalf("NewGatewayClient: %v", err)
	}
	return client, server, tokens, privateKey.Public().(ed25519.PublicKey)
}

func validGatewayCreateRequest() GatewayCreateRequest {
	return GatewayCreateRequest{
		SKU:     "sku-video",
		TaskRef: "task-01",
		Spec: GatewayInvocationSpec{
			SchemaVersion: "v1",
			Input:         map[string]any{"prompt": "hello", "frames": 5},
			Parameters:    map[string]string{"duration": "5", "seed": "0007"},
		},
		ModerationReceipt: "opaque-receipt",
	}
}

func validGatewaySnapshotBody(id, version, state string, terminal bool) []byte {
	return []byte(fmt.Sprintf(`{"request_id":"req-01","invocation":{"id":%q,"version":%q,"state":%q,"terminal":%t,"sku_id":"sku-video","task_ref":"task-01","created_at_ms":1800000000000,"updated_at_ms":1800000001000,"reserved_units":"5","settled_units":null,"result":null,"error":null,"lot_deductions":null}}`, id, version, state, terminal))
}

func writeGatewayJSON(w http.ResponseWriter, status int, body []byte) {
	w.Header().Set("Content-Type", "application/json")
	w.WriteHeader(status)
	_, _ = w.Write(body)
}

func TestGatewayCreateAsyncUsesFrozenHeadersAndFingerprint(t *testing.T) {
	const key = "create-key-123456"
	var client *GatewayClient
	var publicKey ed25519.PublicKey
	client, _, _, publicKey = newGatewayTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Method != http.MethodPost || request.URL.Path != "/runtime/v1/invocations" {
			t.Fatalf("request = %s %s", request.Method, request.URL.Path)
		}
		if got := request.Header.Get("Accept"); got != "application/json" {
			t.Errorf("Accept = %q", got)
		}
		if got := request.Header.Get("Idempotency-Key"); got != key {
			t.Errorf("Idempotency-Key = %q", got)
		}
		if got := request.Header.Get("X-Sluice-Actor"); got != "user-01" {
			t.Errorf("X-Sluice-Actor = %q", got)
		}
		if request.Header.Get("Authorization") != "Bearer access-token-secret" {
			t.Errorf("Authorization header was not set")
		}
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read request body: %v", err)
		}
		var decoded map[string]any
		if err := json.Unmarshal(body, &decoded); err != nil {
			t.Fatalf("decode request body: %v", err)
		}
		if _, ok := decoded["delivery_mode"]; ok {
			t.Fatal("request body unexpectedly exposed delivery_mode")
		}
		spec := decoded["spec"].(map[string]any)
		if spec["parameters"].(map[string]any)["duration"] != "5" {
			t.Fatalf("numeric parameter was not preserved as a string: %#v", spec["parameters"])
		}
		assertion, err := VerifyCompactJWS(request.Header.Get("X-Sluice-Actor-Assertion"), publicKey)
		if err != nil {
			t.Fatalf("VerifyCompactJWS: %v", err)
		}
		fingerprint, err := RequestFingerprint(request.Method, request.URL.Path, "user-01", key, body)
		if err != nil {
			t.Fatalf("RequestFingerprint: %v", err)
		}
		if assertion.RequestFingerprint != fingerprint || assertion.Operation != string(GatewayInvocationCreate) {
			t.Fatalf("assertion claims = %#v, fingerprint = %q", assertion, fingerprint)
		}
		// sub 必须等于 X-Sluice-Actor 携带的 actor 值（06 契约 claim 表；服务端
		// instanceauth/assertion.go 比对 claims.Subject != request.Actor）。
		// 这条断言此前缺失——本测试同时看得见 header 与 claims 却从不交叉核对，
		// 于是把「sub 恒等于 header 名字」这个缺陷认证成了正确行为。
		if assertion.Subject != request.Header.Get("X-Sluice-Actor") {
			t.Fatalf("assertion sub = %q, want the actor header value %q",
				assertion.Subject, request.Header.Get("X-Sluice-Actor"))
		}
		w.Header().Set("Location", "/runtime/v1/invocations/inv-01")
		writeGatewayJSON(w, http.StatusAccepted, validGatewaySnapshotBody("inv-01", "1", string(GatewayStateAccepted), false))
	}))

	response, err := client.CreateAsync(context.Background(), validGatewayCreateRequest(), key)
	if err != nil {
		t.Fatalf("CreateAsync: %v", err)
	}
	if response.StatusCode != http.StatusAccepted || response.InvocationID != "inv-01" || response.Snapshot == nil {
		t.Fatalf("response = %#v", response)
	}
}

func TestGateway303IsExposedWithoutFollowingGET(t *testing.T) {
	var requests atomic.Int32
	client, _, _, _ := newGatewayTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		requests.Add(1)
		if request.Method != http.MethodPost {
			t.Fatalf("303 was followed with %s", request.Method)
		}
		w.Header().Set("Location", "/runtime/v1/invocations/inv-existing")
		w.WriteHeader(http.StatusSeeOther)
	}))

	response, err := client.CreateAsync(context.Background(), validGatewayCreateRequest(), "create-key-123456")
	if err != nil {
		t.Fatalf("CreateAsync(303): %v", err)
	}
	if !response.AlreadyExists || response.InvocationID != "inv-existing" || response.StatusCode != http.StatusSeeOther {
		t.Fatalf("303 response = %#v", response)
	}
	if requests.Load() != 1 {
		t.Fatalf("request count = %d, want 1", requests.Load())
	}
}

func TestGateway401RuntimeUnauthenticatedRefreshesOnceWithFreshAssertion(t *testing.T) {
	var requests atomic.Int32
	var firstAssertion, secondAssertion string
	client, _, tokens, _ := newGatewayTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Idempotency-Key") != "create-key-123456" {
			t.Fatalf("idempotency key changed during token refresh")
		}
		switch requests.Add(1) {
		case 1:
			firstAssertion = request.Header.Get("X-Sluice-Actor-Assertion")
			writeGatewayJSON(w, http.StatusUnauthorized, []byte(`{"request_id":"req-auth","error":{"code":"runtime_unauthenticated","message":"expired","retryable":false,"retry_after_ms":null,"details":{}}}`))
		case 2:
			secondAssertion = request.Header.Get("X-Sluice-Actor-Assertion")
			w.Header().Set("Location", "/runtime/v1/invocations/inv-01")
			writeGatewayJSON(w, http.StatusAccepted, validGatewaySnapshotBody("inv-01", "1", string(GatewayStateAccepted), false))
		default:
			t.Fatal("gateway request retried more than once")
		}
	}))
	if _, err := client.CreateAsync(context.Background(), validGatewayCreateRequest(), "create-key-123456"); err != nil {
		t.Fatalf("CreateAsync after token refresh: %v", err)
	}
	if tokens.invalidates.Load() != 1 || firstAssertion == "" || secondAssertion == "" || firstAssertion == secondAssertion {
		t.Fatalf("refresh behavior: invalidates=%d first=%q second=%q", tokens.invalidates.Load(), firstAssertion, secondAssertion)
	}
}

func TestGatewayCreateStreamReturnsValidatedSSE(t *testing.T) {
	client, _, _, _ := newGatewayTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Accept") != "text/event-stream" {
			t.Fatalf("Accept = %q", request.Header.Get("Accept"))
		}
		w.Header().Set("Content-Type", "text/event-stream; charset=utf-8")
		w.WriteHeader(http.StatusOK)
		_, _ = io.WriteString(w, strings.Join([]string{
			"id: 1",
			"event: invocation.accepted",
			`data: {"request_id":"req-stream","invocation_id":"inv-stream","sequence":1,"occurred_at_ms":1,"payload":{}}`,
			"",
			"id: 2",
			"event: invocation.failed",
			`data: {"request_id":"req-stream","invocation_id":"inv-stream","sequence":2,"occurred_at_ms":2,"payload":{"code":"upstream_unavailable"}}`,
			"",
		}, "\n"))
	}))
	response, err := client.CreateStream(context.Background(), validGatewayCreateRequest(), "stream-key-123456")
	if err != nil || response.Stream == nil {
		t.Fatalf("CreateStream = %#v, %v", response, err)
	}
	defer response.Stream.Close()
	if event, err := response.Stream.Next(); err != nil || event.Event != "invocation.accepted" {
		t.Fatalf("accepted event = %#v, %v", event, err)
	}
	if event, err := response.Stream.Next(); err != nil || event.Event != "invocation.failed" {
		t.Fatalf("failed event = %#v, %v", event, err)
	}
}

func TestGatewayGetMaintainsETagAndForbidsIdempotencyKey(t *testing.T) {
	var sawIfNoneMatch atomic.Bool
	client, _, _, _ := newGatewayTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		if request.Header.Get("Idempotency-Key") != "" {
			t.Fatal("GET carried an Idempotency-Key")
		}
		if request.Header.Get("If-None-Match") == `"1"` {
			sawIfNoneMatch.Store(true)
			w.Header().Set("ETag", `"1"`)
			w.Header().Set("Retry-After", "0")
			w.WriteHeader(http.StatusNotModified)
			return
		}
		w.Header().Set("ETag", `"1"`)
		w.Header().Set("Retry-After", "1")
		writeGatewayJSON(w, http.StatusOK, validGatewaySnapshotBody("inv-01", "1", string(GatewayStateRunning), false))
	}))

	poller, err := client.NewPoller("inv-01")
	if err != nil {
		t.Fatalf("NewPoller: %v", err)
	}
	first, err := poller.Poll(context.Background())
	if err != nil || first.Snapshot == nil || first.ETag != `"1"` || first.RetryAfter != time.Second {
		t.Fatalf("first poll = %#v, %v", first, err)
	}
	// 测试不等待真实的一秒；直接 GET 验证 If-None-Match/304，前面的轮询元数据已验证 Retry-After。
	second, err := client.GetWithETag(context.Background(), "inv-01", poller.ETag())
	if err != nil || !second.NotModified || !sawIfNoneMatch.Load() {
		t.Fatalf("second poll = %#v, %v", second, err)
	}
}

func TestGatewayArtifactRedirectAndDigestMismatchAreRejected(t *testing.T) {
	tests := []struct {
		name    string
		handler http.HandlerFunc
	}{
		{
			name: "redirect",
			handler: func(w http.ResponseWriter, request *http.Request) {
				w.Header().Set("Location", "https://supplier.example/artifact")
				w.WriteHeader(http.StatusFound)
			},
		},
		{
			name: "digest mismatch",
			handler: func(w http.ResponseWriter, request *http.Request) {
				body := []byte("artifact-bytes")
				w.Header().Set("Content-Type", "application/octet-stream")
				w.Header().Set("Content-Digest", "sha-256=:"+base64.StdEncoding.EncodeToString(make([]byte, sha256.Size))+":")
				w.WriteHeader(http.StatusOK)
				_, _ = w.Write(body)
			},
		},
	}
	for _, test := range tests {
		t.Run(test.name, func(t *testing.T) {
			client, _, _, _ := newGatewayTestClient(t, test.handler)
			var output bytes.Buffer
			err := client.DownloadArtifact(context.Background(), "inv-01", "artifact-01", &output)
			if err == nil {
				t.Fatal("DownloadArtifact unexpectedly succeeded")
			}
			if output.Len() != 0 {
				t.Fatalf("writer received bytes after rejection: %q", output.String())
			}
			var gatewayErr *GatewayError
			if !errors.As(err, &gatewayErr) || gatewayErr.Code != GatewayInternalError {
				t.Fatalf("error = %T %v", err, err)
			}
		})
	}
}

func TestGatewaySSEOrderAndForwardCompatibleFields(t *testing.T) {
	streamBody := strings.Join([]string{
		": keep-alive",
		"",
		"id: 1",
		"event: invocation.accepted",
		"x-future-field: ignored",
		`sse-data: ignored`,
		`data: {"request_id":"req-01","invocation_id":"inv-01","sequence":1,"occurred_at_ms":1800000000000,"payload":{}}`,
		"",
		"id: 2",
		"event: usage.final",
		`data: {"request_id":"req-01","invocation_id":"inv-01","sequence":2,"occurred_at_ms":1800000000001,"payload":{"units":"5"}}`,
		"",
		"id: 3",
		"event: invocation.completed",
		`data: {"request_id":"req-01","invocation_id":"inv-01","sequence":3,"occurred_at_ms":1800000000002,"payload":{}}`,
		"",
	}, "\n")
	stream := newGatewaySSEStream(io.NopCloser(strings.NewReader(streamBody)))
	defer stream.Close()
	for _, want := range []string{"invocation.accepted", "usage.final", "invocation.completed"} {
		event, err := stream.Next()
		if err != nil || event.Event != want {
			t.Fatalf("Next = %#v, %v; want %s", event, err, want)
		}
	}
	if !stream.Terminal() || stream.Pending() {
		t.Fatalf("stream terminal state = terminal:%t pending:%t", stream.Terminal(), stream.Pending())
	}
	if _, err := stream.Next(); !errors.Is(err, io.EOF) {
		t.Fatalf("terminal EOF = %v", err)
	}

	invalid := newGatewaySSEStream(io.NopCloser(strings.NewReader(strings.Join([]string{
		"id: 1",
		"event: invocation.accepted",
		`data: {"request_id":"r","invocation_id":"i","sequence":1,"occurred_at_ms":1,"payload":{}}`,
		"",
		"id: 2",
		"event: invocation.accepted",
		`data: {"request_id":"r","invocation_id":"i","sequence":2,"occurred_at_ms":2,"payload":{}}`,
		"",
	}, "\n"))))
	defer invalid.Close()
	if _, err := invalid.Next(); err != nil {
		t.Fatalf("first accepted event: %v", err)
	}
	if _, err := invalid.Next(); err == nil {
		t.Fatal("duplicate accepted event was accepted")
	}
}

func TestGatewayRetryableTableAndWireMismatch(t *testing.T) {
	for _, code := range []string{GatewayRateLimited, GatewayUpstreamUnavailable, GatewayInternalError} {
		if !RetryableGatewayCode(code) {
			t.Errorf("RetryableGatewayCode(%q) = false", code)
		}
	}
	for _, code := range []string{
		GatewayInvalidInvocationRequest, GatewayRuntimeUnauthenticated, GatewayActorAssertionInvalid,
		GatewayActorAssertionReplayed, GatewayRuntimeForbidden, GatewaySKUNotAllowed,
		GatewayComplianceRejected, GatewayInvocationNotFound, GatewayInvocationArtifactNotFound,
		GatewayInvocationArtifactExpired, GatewayInvocationDeliveryModeMismatch,
		GatewayInvocationIdempotencyConflict, GatewayInsufficientQuota, GatewayMemberLimitExceeded,
	} {
		if RetryableGatewayCode(code) {
			t.Errorf("RetryableGatewayCode(%q) = true", code)
		}
	}
	body := []byte(`{"request_id":"req-err","error":{"code":"rate_limited","message":"diagnostic","retryable":false,"retry_after_ms":1200,"details":{"invocation_id":"inv-01"}}}`)
	err := gatewayErrorFromBytes(body, http.StatusTooManyRequests, isGatewayInvocationErrorCode)
	if err.Code != GatewayRateLimited || err.Retryable || !err.RetryableByCode() || err.RetryAfterMS == nil || *err.RetryAfterMS != 1200 || err.InvocationID != "inv-01" {
		t.Fatalf("wire mismatch error = %#v", err)
	}
	if got := err.Error(); strings.Contains(got, "diagnostic") || strings.Contains(got, "1200") {
		t.Fatalf("error string used unstable diagnostic data: %q", got)
	}
}

func TestGatewaySiteContextUsesEmptyUnauthenticatedRequest(t *testing.T) {
	var bodyLength atomic.Int32
	server := httptest.NewTLSServer(http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
		body, err := io.ReadAll(request.Body)
		if err != nil {
			t.Fatalf("read site-context body: %v", err)
		}
		bodyLength.Store(int32(len(body)))
		if len(body) != 0 || request.Header.Get("Authorization") != "" || request.Header.Get("X-Sluice-Actor") != "" || request.Header.Get("X-Sluice-Actor-Assertion") != "" {
			writeGatewayJSON(w, http.StatusBadRequest, []byte(`{"request_id":"req","error":{"code":"invalid_registration","message":"bad","retryable":false,"retry_after_ms":null,"details":{}}}`))
			return
		}
		writeGatewayJSON(w, http.StatusOK, []byte(`{"request_id":"req-site","site_context_token":"site-secret-token","expires_at_ms":1800000060000}`))
	}))
	defer server.Close()
	client, err := NewGatewaySiteContextClient(server.URL, server.Client().Transport.(*http.Transport).TLSClientConfig)
	if err != nil {
		t.Fatalf("NewGatewaySiteContextClient: %v", err)
	}
	response, err := client.Issue(context.Background())
	if err != nil {
		t.Fatalf("Issue: %v", err)
	}
	if bodyLength.Load() != 0 || response.SiteContextToken.Reveal() != "site-secret-token" {
		t.Fatalf("site-context response = %#v, body length = %d", response, bodyLength.Load())
	}
	if formatted := fmt.Sprintf("%v", response.SiteContextToken); strings.Contains(formatted, "site-secret-token") {
		t.Fatalf("site-context token leaked in formatting: %q", formatted)
	}
	// 负对照：服务端对非空 body 返回冻结的 invalid_registration。
	nonEmpty, err := server.Client().Post(server.URL+gatewaySiteContextPath, "application/json", strings.NewReader(`{}`))
	if err != nil {
		t.Fatalf("non-empty site-context request: %v", err)
	}
	defer nonEmpty.Body.Close()
	if nonEmpty.StatusCode != http.StatusBadRequest {
		t.Fatalf("non-empty body status = %d", nonEmpty.StatusCode)
	}
}

func TestGatewayCreateRejectsNumericParametersAndShortKeys(t *testing.T) {
	request := validGatewayCreateRequest()
	request.Spec.Parameters = map[string]any{"duration": 5}
	if err := request.Validate(); err == nil {
		t.Fatal("numeric parameter was accepted")
	}
	request.Spec.Parameters = map[string]string{"duration": "5"}
	if err := validateGatewayIdempotencyKey("short"); err == nil {
		t.Fatal("short idempotency key was accepted")
	}
}
