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

	"github.com/emiya-dev/musereel-sdk/jcs"
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

// validGatewaySnapshotBody 必须逐字段复刻真 gateway 的 respond.go 实际发出的形状。
// 这个 helper 已经栽过两次，都是同一类：**夹具锚在想象上而不是实物上**，
// SDK 的实现与夹具的虚构互相吻合，测试于是永远绿，对真服务端却一次也过不去。
//
//  1. version：此前这里用 %q 发带引号的字符串，SDK 的类型也是 string，两个错误互相吻合。
//     真 gateway 是 int64、以 JSON 数字发出。
//  2. lot_deductions：此前这里发 null，而真 gateway 发的是**空数组 []**
//     （respond.go 用 make([]lotDeduction, len(...)) 构造，零长度也序列化成 []；
//     契约 06 §4.2 的 snapshot 示例同样是 []）。SDK 的 gatewayJSONValueNonEmpty
//     把 [] 判成「非空」，于是每一次 async create 都被判「非 completed 不得有
//     lot_deductions」而拒绝——由接入彩排的 golden 腿在真服务端上抓出。
//
// 改这个 helper 前先去 respond.go 对一遍实际字段与类型，不要凭印象写。
func validGatewaySnapshotBody(id string, version int64, state string, terminal bool) []byte {
	return []byte(fmt.Sprintf(`{"request_id":"req-01","invocation":{"id":%q,"version":%d,"state":%q,"terminal":%t,"sku_id":"sku-video","task_ref":"task-01","created_at_ms":1800000000000,"updated_at_ms":1800000001000,"reserved_units":"5","settled_units":null,"result":null,"error":null,"lot_deductions":[]}}`, id, version, state, terminal))
}

// TestGatewayJSONValueEmptinessMatchesWireShapes 钉死「空」的判据：
// 空数组与空对象是「字段在、没内容」，必须算空；有元素才算非空。
// 负对照：把 gatewayJSONValueNonEmpty 改回只看字节长度与 null，前三行当场变红。
func TestGatewayJSONValueEmptinessMatchesWireShapes(t *testing.T) {
	for _, testCase := range []struct {
		raw  string
		want bool
	}{
		{`[]`, false},
		{`{}`, false},
		{` [ ] `, false},
		{`null`, false},
		{``, false},
		{`[{"lot_id":"lot_01","units":"33"}]`, true},
		{`{"artifacts":[]}`, true},
		{`"text"`, true},
		{`0`, true},
	} {
		if got := gatewayJSONValueNonEmpty([]byte(testCase.raw)); got != testCase.want {
			t.Errorf("gatewayJSONValueNonEmpty(%q) = %t, want %t", testCase.raw, got, testCase.want)
		}
	}
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
		writeGatewayJSON(w, http.StatusAccepted, validGatewaySnapshotBody("inv-01", 1, string(GatewayStateAccepted), false))
	}))

	response, err := client.CreateAsync(context.Background(), validGatewayCreateRequest(), key)
	if err != nil {
		t.Fatalf("CreateAsync: %v", err)
	}
	if response.StatusCode != http.StatusAccepted || response.InvocationID != "inv-01" || response.Snapshot == nil {
		t.Fatalf("response = %#v", response)
	}
}

func TestGatewayCreateAsyncProtocolErrorsRetainLocationInvocationID(t *testing.T) {
	for _, testCase := range []struct {
		name     string
		location string
		body     []byte
		wantID   string
	}{
		{
			name:     "bad snapshot body with Location",
			location: "/runtime/v1/invocations/inv-from-location",
			body:     []byte("not-json"),
			wantID:   "inv-from-location",
		},
		{
			name:     "snapshot ID mismatch with Location",
			location: "/runtime/v1/invocations/inv-from-location",
			body:     validGatewaySnapshotBody("inv-from-body", 1, string(GatewayStateAccepted), false),
			wantID:   "inv-from-location",
		},
		{
			name:   "bad snapshot body without Location",
			body:   []byte("not-json"),
			wantID: "",
		},
		{
			// Location 解不出来、但 body 校验得过：ID 从 body 回落。
			// 二次审查（grok 面 1）点名的同构漏网——mismatch 时信 Location，
			// Location 坏掉时却把已经校验过的 snapshot.ID 一起扔了。
			name:     "unparsable Location falls back to snapshot ID",
			location: "https://evil.example.com/somewhere/else?x=1",
			body:     validGatewaySnapshotBody("inv-from-body", 1, string(GatewayStateAccepted), false),
			wantID:   "inv-from-body",
		},
		{
			// 两边都解不出来时不许编造。
			name:     "unparsable Location and bad body yields no ID",
			location: "https://evil.example.com/somewhere/else?x=1",
			body:     []byte("not-json"),
			wantID:   "",
		},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			client, _, _, _ := newGatewayTestClient(t, http.HandlerFunc(func(w http.ResponseWriter, request *http.Request) {
				if testCase.location != "" {
					w.Header().Set("Location", testCase.location)
				}
				writeGatewayJSON(w, http.StatusAccepted, testCase.body)
			}))

			_, err := client.CreateAsync(context.Background(), validGatewayCreateRequest(), "create-key-123456")
			if err == nil {
				t.Fatal("CreateAsync unexpectedly succeeded")
			}
			var gatewayErr *GatewayError
			if !errors.As(err, &gatewayErr) {
				t.Fatalf("error = %T %v, want *GatewayError", err, err)
			}
			if gatewayErr.InvocationID != testCase.wantID {
				t.Fatalf("InvocationID = %q, want %q; error = %#v", gatewayErr.InvocationID, testCase.wantID, gatewayErr)
			}
		})
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
			writeGatewayJSON(w, http.StatusAccepted, validGatewaySnapshotBody("inv-01", 1, string(GatewayStateAccepted), false))
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
		writeGatewayJSON(w, http.StatusOK, validGatewaySnapshotBody("inv-01", 1, string(GatewayStateRunning), false))
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
	// ⚠ 这里刻意**不**再手抄一遍 19 个码：那样它就是同一集合在本包里的第四份副本，
	// 而它是「包含式」断言（漏抄一个码只会少测一条，不会红）。集合成员统一由
	// frozenGatewayErrorContract 提供，那张表自身与中枢导出产物双向对拍。
	for _, entry := range frozenGatewayErrorContract {
		if got := RetryableGatewayCode(entry.code); got != entry.retryable {
			t.Errorf("RetryableGatewayCode(%q) = %t, want %t", entry.code, got, entry.retryable)
		}
	}

	for _, testCase := range []struct {
		name          string
		code          string
		status        int
		wireRetryable bool
		fromWire      bool
		wantRetryable bool
	}{
		{name: "wire internal_error false", code: GatewayInternalError, status: http.StatusInternalServerError, wireRetryable: false, fromWire: true, wantRetryable: false},
		{name: "wire internal_error true", code: GatewayInternalError, status: http.StatusInternalServerError, wireRetryable: true, fromWire: true, wantRetryable: true},
		{name: "wire rate_limited false stays table true", code: GatewayRateLimited, status: http.StatusTooManyRequests, wireRetryable: false, fromWire: true, wantRetryable: true},
		{name: "SDK internal_error without wire stays conservative", code: GatewayInternalError, wantRetryable: true},
	} {
		t.Run(testCase.name, func(t *testing.T) {
			var err *GatewayError
			if testCase.fromWire {
				body := []byte(fmt.Sprintf(`{"request_id":"req-err","error":{"code":%q,"message":"diagnostic","retryable":%t,"retry_after_ms":1200,"details":{"invocation_id":"inv-01"}}}`,
					testCase.code, testCase.wireRetryable))
				err = gatewayErrorFromBytes(body, testCase.status, isGatewayInvocationErrorCode)
				if err.Code != testCase.code || err.Retryable != testCase.wireRetryable || err.RetryAfterMS == nil || *err.RetryAfterMS != 1200 || err.InvocationID != "inv-01" {
					t.Fatalf("wire error = %#v", err)
				}
				if got := err.Error(); strings.Contains(got, "diagnostic") || strings.Contains(got, "1200") {
					t.Fatalf("error string used unstable diagnostic data: %q", got)
				}
			} else {
				err = newGatewayError(GatewayInternalError, 0, "", "transport failure")
			}
			if got := err.RetryableByCode(); got != testCase.wantRetryable {
				t.Fatalf("RetryableByCode() = %t, want %t; error = %#v", got, testCase.wantRetryable, err)
			}
			if got := err.IsRetryable(); got != testCase.wantRetryable {
				t.Fatalf("IsRetryable() = %t, want %t; error = %#v", got, testCase.wantRetryable, err)
			}
		})
	}
}

// 第五象限：wire 给了 internal_error 但**根本没带** retryable 字段。
// 这一格不能落成 bool 零值 false —— 06:611-619 对未登记内部码的缺省是保守可重试，
// 判成不可重试会让调用方对一个瞬时内部错过早放弃。上面那张表覆盖不到它：
// 表里每一行都显式写了 retryable，缺字段与「显式 false」在解出来的结构体上同形。
func TestGatewayInternalErrorWithoutWireRetryableStaysConservative(t *testing.T) {
	body := []byte(`{"request_id":"req-err","error":{"code":"internal_error","message":"diagnostic","details":{}}}`)
	err := gatewayErrorFromBytes(body, http.StatusInternalServerError, isGatewayInvocationErrorCode)
	if err.Code != GatewayInternalError {
		t.Fatalf("code = %q, want %q", err.Code, GatewayInternalError)
	}
	if !err.RetryableByCode() {
		t.Fatalf("RetryableByCode() = false, want true when the wire omits retryable; error = %#v", err)
	}
	if !err.IsRetryable() {
		t.Fatalf("IsRetryable() = false, want true when the wire omits retryable; error = %#v", err)
	}
}

// 显式 null 与字段缺失走同一条路：都不算「服务端说了话」。
func TestGatewayInternalErrorWithNullRetryableStaysConservative(t *testing.T) {
	body := []byte(`{"request_id":"req-err","error":{"code":"internal_error","message":"diagnostic","retryable":null,"details":{}}}`)
	err := gatewayErrorFromBytes(body, http.StatusInternalServerError, isGatewayInvocationErrorCode)
	if !err.RetryableByCode() {
		t.Fatalf("RetryableByCode() = false, want true when the wire sends retryable:null; error = %#v", err)
	}
}

// speechDecimalRequest 造一个 speech 请求，parameters 由调用方给。
func speechRequestWithParameters(raw string) GatewayCreateRequest {
	request := validGatewayCreateRequest()
	request.SKU = "speech.generate.v1"
	request.TaskRef = "speech-task-01"
	request.Spec.Input = map[string]any{"text": "hello"}
	request.Spec.Parameters = json.RawMessage(raw)
	return request
}

// TestGatewayCreateAllowsIntegerNumericParameters 钉住「整数一律放行」。
//
// 中枢把 speech 的 pitch 声明为 integer、speed 与 vol 声明为 number
// （querysurface/public_schema.go），它们本来就该以 JSON 数字发送——早先那版
// 「拒绝 parameters 内任何 JSON 数字」把它们一起误杀了。
func TestGatewayCreateAllowsIntegerNumericParameters(t *testing.T) {
	request := speechRequestWithParameters(`{"voice_id":"x","pitch":0,"speed":1,"vol":2}`)
	if err := request.Validate(); err != nil {
		t.Fatalf("整数参数被拒了：%v", err)
	}
}

// TestGatewayCreateRejectsUncanonicalizableNumbersEverywhere 是本关的主表。
//
// 🔴 每一行都必须同时满足两件事：**被拒**，且**报错点名字段路径**。后者不是文风要求——
// 这一关不拦的话，同样的输入在中枢那边的下场是（2026-08-19 在中枢仓逐行核过）：
// 整份请求 body 进 `computeFingerprint`（gateway create.go:62）→ `instanceauth/jcs.go:31`
// 的 CanonicalizeJSON 拒掉 → **create.go:62-65 丢掉 JCS 原文**，改写成
// `invalid_invocation_request` + `fields=["request"]`，接入方拿到的报错既不知道是哪个字段、
// 也不知道问题出在小数。中枢自己把这个退化形态写成过反例
// （gateway 的 response_format_test.go:102-107：「不能退化为 fields=[request]」）。
// ⇒ SDK 这一关是目前唯一能把它定位到字段的地方。
func TestGatewayCreateRejectsUncanonicalizableNumbersEverywhere(t *testing.T) {
	cases := []struct {
		name       string
		parameters string
		wantPath   string
		// wantDiagnosis 钉住**报错说对了原因**，不只是"拒了"。
		// 消融实测：把小数分支关掉后 strconv.ParseInt("1.2") 一样会失败，于是同一条
		// 输入落进"超出 int64 范围"那条错误路径——照样被拒、照样带字段路径，只有诊断
		// 是错的。只断言"拒了 + 有路径"的话，这种退化对测试完全隐形。
		wantDiagnosis string
		// notDiagnosis 是它的反面：小数不能被说成 int64 越界。
		notDiagnosis string
	}{
		{
			// 早先那版白名单把 speech 整族当作"数字字段"整体放行，于是 1.2 在
			// Validate 这关是绿的，真发请求时才在指纹那步炸——而那条测试只调
			// Validate()，没走发送路径，所以它的绿证明不了"这个请求发得出去"。
			name:          "speech speed 小数",
			parameters:    `{"voice_id":"x","pitch":0,"speed":1.2,"vol":1.5}`,
			wantPath:      "spec.parameters.speed",
			wantDiagnosis: "cannot canonicalize",
			notDiagnosis:  "int64 range",
		},
		{
			// temperature 是 text 的**顶层** decimal_string 字段，早先那版白名单漏了它。
			name:          "text temperature 小数",
			parameters:    `{"temperature":0.7}`,
			wantPath:      "spec.parameters.temperature",
			wantDiagnosis: "cannot canonicalize",
			notDiagnosis:  "int64 range",
		},
		{
			// music 的 audio_setting.sample_rate / bitrate 是 decimal_string，但嵌套在
			// audio_setting 里。早先那版的判定条件只认顶层键，**结构上**就够不到它们。
			name:          "music audio_setting 嵌套小数",
			parameters:    `{"audio_setting":{"sample_rate":44100.0,"format":"mp3"}}`,
			wantPath:      "spec.parameters.audio_setting.sample_rate",
			wantDiagnosis: "cannot canonicalize",
			notDiagnosis:  "int64 range",
		},
		{
			// 早先那版整个跳过 response_format，理由是"JSON Schema 的约束天然是数字"。
			// 但中枢**明确拒绝**它里面的小数，还专门给了自诊断文案
			// （gateway 侧：「schema 中的 JSON 数字不能使用小数或指数，请改用整数十进制」）。
			// 跳过它等于让 SDK 放行一个中枢一定会拒的请求。
			name:          "response_format 里的小数约束",
			parameters:    `{"response_format":{"type":"json_schema","json_schema":{"name":"a","schema":{"properties":{"score":{"minimum":0.5}}}}}}`,
			wantPath:      "spec.parameters.response_format.json_schema.schema.properties.score.minimum",
			wantDiagnosis: "cannot canonicalize",
			notDiagnosis:  "int64 range",
		},
		{
			name:          "指数记法",
			parameters:    `{"seconds":1e3}`,
			wantPath:      "spec.parameters.seconds",
			wantDiagnosis: "cannot canonicalize",
			notDiagnosis:  "int64 range",
		},
		{
			name:          "数组元素里的小数",
			parameters:    `{"weights":[1,2.5]}`,
			wantPath:      "spec.parameters.weights[1]",
			wantDiagnosis: "cannot canonicalize",
			notDiagnosis:  "int64 range",
		},
		{
			// jcs 子集的第二条限制：整数也必须落在 int64 内。
			name:          "超出 int64 的整数",
			parameters:    `{"n":99999999999999999999}`,
			wantPath:      "spec.parameters.n",
			wantDiagnosis: "int64 range",
		},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := speechRequestWithParameters(testCase.parameters).Validate()
			if err == nil {
				t.Fatalf("%s 必须被拒", testCase.parameters)
			}
			if !strings.Contains(err.Error(), testCase.wantPath) {
				t.Fatalf("报错必须点名 %s，接入方才能自助定位；实际：%v", testCase.wantPath, err)
			}
			if !strings.Contains(err.Error(), testCase.wantDiagnosis) {
				t.Fatalf("报错必须说对原因（含 %q）；实际：%v", testCase.wantDiagnosis, err)
			}
			if testCase.notDiagnosis != "" && strings.Contains(err.Error(), testCase.notDiagnosis) {
				t.Fatalf("小数不能被诊断成 %q——那是另一条错误路径的文案；实际：%v",
					testCase.notDiagnosis, err)
			}
		})
	}
}

// TestGatewayCreateRejectsUncanonicalizableNumbersInInput 覆盖 spec.input 那一侧。
//
// assertion digest 规范化的是**整个请求体**，只查 parameters 会让 input 里的同类数值
// 躲到 digest 那一步才炸——同一条规则在一半字段上执行、在另一半上不执行，是新的不一致。
func TestGatewayCreateRejectsUncanonicalizableNumbersInInput(t *testing.T) {
	request := validGatewayCreateRequest()
	request.Spec.Input = json.RawMessage(`{"text":"hi","weight":0.8}`)
	err := request.Validate()
	if err == nil {
		t.Fatal("spec.input 里的小数必须被拒")
	}
	if !strings.Contains(err.Error(), "spec.input.weight") {
		t.Fatalf("报错必须点名 spec.input.weight：%v", err)
	}
}

// TestGatewayCreateDefersSchemaTypingToTheGateway 钉住这一关**不冒充 schema 校验**。
//
// image_count 在中枢声明为 decimal_string，写成整数 4 是错的——但拒它需要一份逐 SKU
// 请求 schema 的手抄本，而中枢那份按运营配置动态派生（publicParameterRule 会把任何配了
// min/max 的 param_schema 字段渲染成 decimal_string）。抄不全就会静默漏字段，所以这里
// 明确放手：整数形态交给中枢拒，它知道 const/min/max/enum，报错比 SDK 猜的准。
//
// ⚠ 这条测试红了不代表它坏了——它代表有人给 SDK 加回了字段级 schema 判断。
// 先回答「这份清单靠什么跟上中枢的动态派生」，再决定要不要改断言。
func TestGatewayCreateDefersSchemaTypingToTheGateway(t *testing.T) {
	request := validGatewayCreateRequest()
	request.SKU = "image.generate.v1"
	request.TaskRef = "image-task-01"
	request.Spec.Parameters = json.RawMessage(`{"image_count":4,"resolution":"512","quality":"q2"}`)
	if err := request.Validate(); err != nil {
		t.Fatalf("整数形态的 decimal_string 字段应当放行给中枢判：%v", err)
	}

	// 正确写法（十进制字符串）当然也放行——否则上面的绿只是"这个字段总是放行"。
	request.Spec.Parameters = json.RawMessage(`{"image_count":"4","resolution":"512","quality":"q2"}`)
	if err := request.Validate(); err != nil {
		t.Fatalf("十进制字符串必须放行：%v", err)
	}
}

// TestGatewayNumberRuleMatchesJCSSubset 是这一关的**跨仓漂移守卫**。
//
// 这一关的判据就是 jcs 本身（validateGatewayCanonicalizable 直接调 CanonicalizeJSON），
// 所以这里断言的是「两边判定逐样本一致」——包括**非数字**的拒因。
// 早先这里复刻了一份数字规则，样本也只覆盖数字形态，于是重复键这种 jcs 拒、复刻版放行的
// 缝隙落在样本之外：`{"a":1,"a":2}` 在 encoding/json 里是末键静默覆盖，不报错。
// 二次审查（composer 路）实测抓到，改判据后一并纳入。
func TestGatewayNumberRuleMatchesJCSSubset(t *testing.T) {
	samples := []string{
		`{"a":1}`,
		`{"a":0}`,
		`{"a":-7}`,
		`{"a":1.5}`,
		`{"a":1e3}`,
		`{"a":1E-3}`,
		`{"a":99999999999999999999}`,
		`{"a":{"b":[1,2]}}`,
		`{"a":{"b":[1,2.5]}}`,
		`{"a":"1.5"}`,
		`{"a":true,"b":null,"c":"x"}`,
		// 非数字拒因：判据换成 jcs 之后这些也必须一致。
		`{"a":1,"a":2}`,
		`{"a":{"b":1,"b":2}}`,
		`{"a":[{"c":1,"c":2}]}`,
		// 边界形态（二次审查 grok 路指出样本表疏漏，实现本来就等价，但没被钉住）。
		`{"a":-0}`,
		`{"a":1.0}`,
		`{"a":-0.0}`,
		`{"a":1E+2}`,
		`{"a":0e0}`,
		`{"a":9223372036854775807}`,
		`{"a":-9223372036854775808}`,
		`{"a":9223372036854775808}`,
	}
	for _, sample := range samples {
		validateRejected := validateGatewayParameters([]byte(sample)) != nil
		_, jcsErr := jcs.CanonicalizeJSON([]byte(sample))
		jcsRejected := jcsErr != nil
		if validateRejected != jcsRejected {
			t.Errorf("判定与 jcs 子集不一致 %s：Validate 拒=%v，CanonicalizeJSON 拒=%v（%v）",
				sample, validateRejected, jcsRejected, jcsErr)
		}
	}
}

// TestGatewayRejectsDuplicateKeys 钉住「判据是 jcs 本身，不是它的数字子集」。
//
// 重复键在 encoding/json 里是末键静默覆盖——任何基于 map 的复刻判据都看不见它，
// 而 jcs 走 token 流硬拒。这条红了通常意味着有人把判据改回了自己实现的那份。
func TestGatewayRejectsDuplicateKeys(t *testing.T) {
	cases := map[string]string{
		"spec.parameters": `{"a":1,"a":2}`,
		"嵌套对象":            `{"outer":{"a":1,"a":2}}`,
	}
	for name, parameters := range cases {
		t.Run(name, func(t *testing.T) {
			err := speechRequestWithParameters(parameters).Validate()
			if err == nil {
				t.Fatalf("重复键必须被拒：%s", parameters)
			}
			// 定位不到具体字段是可以接受的（拒因不是数字），但必须说清是哪一段，
			// 并透传 jcs 的原因——否则这条报错和中枢那个 fields=["request"] 一样没用。
			if !strings.Contains(err.Error(), "spec.parameters") {
				t.Fatalf("报错必须点明是 spec.parameters 那一段：%v", err)
			}
			if !strings.Contains(err.Error(), "duplicate key") {
				t.Fatalf("报错必须透传 jcs 的原因，接入方才知道是重复键：%v", err)
			}
		})
	}
}

// TestGatewayResponseFormatAdviceIsNotMisleading 钉住 response_format 子树的报错措辞。
//
// 那里面的数字是 **JSON Schema 关键字**（minimum、maxItems…），不是 SKU 的
// decimal_string 参数。通用文案建议「decimal_string 字段传 JSON 字符串」，照做把
// minimum 写成 "0.5" 会绕过这一关，但那已经不是合法的 JSON Schema——把人带沟里。
// 二次审查（composer 路）提出，实测成立后分叉文案。
func TestGatewayResponseFormatAdviceIsNotMisleading(t *testing.T) {
	const parameters = `{"response_format":{"type":"json_schema","json_schema":{"name":"a","schema":{"properties":{"score":{"minimum":0.5}}}}}}`
	err := speechRequestWithParameters(parameters).Validate()
	if err == nil {
		t.Fatal("response_format 里的小数必须被拒")
	}
	if !strings.Contains(err.Error(), "JSON Schema keyword") {
		t.Fatalf("必须说明这是 JSON Schema 关键字，不能套用 decimal_string 那套建议：%v", err)
	}
	if strings.Contains(err.Error(), "decimal_string fields take a JSON string") {
		t.Fatalf("不得对 JSON Schema 关键字建议加引号——那不是合法 JSON Schema：%v", err)
	}
}

// TestGatewayAdviceNeverEchoesTheRejectedToken 钉住「建议必须可执行」。
//
// 早先文案用 %q 把被拒的 token 原文塞进建议，于是 1e3 被建议成 "1e3"、
// 44100.0 被建议成 "44100.0"。中枢的 decimal_string 要的是**规范十进制形式**——
// 判据是 strconv.FormatInt(parsed, 10) 与原文逐字相等（sluice 侧 video.go:591-593、
// image_spec.go:106-108）——所以接入方照着改完会被换一种方式再拒一次。
// 二次审查（grok 路）提出，实测成立。
func TestGatewayAdviceNeverEchoesTheRejectedToken(t *testing.T) {
	cases := []struct {
		name       string
		parameters string
		token      string
	}{
		{"指数", `{"seconds":1e3}`, "1e3"},
		{"小数", `{"image_count":4.0}`, "4.0"},
		{"嵌套小数", `{"audio_setting":{"sample_rate":44100.0}}`, "44100.0"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := speechRequestWithParameters(testCase.parameters).Validate()
			if err == nil {
				t.Fatalf("必须被拒：%s", testCase.parameters)
			}
			// 报错可以（也应该）**陈述**被拒的是哪个值，但不能把它包在引号里
			// 当成"改成这个字符串"的建议。
			// ⚠ 只检查**本用例自己**那个 token：文案里若带固定示例串，会与某个用例的
			// token 撞车，那时红的是测试不是实现（初版就这么翻过一次，示例 "4.0"
			// 撞上了小数用例）。⇒ 文案现在不带任何示例串。
			if strings.Contains(err.Error(), `"`+testCase.token+`"`) {
				t.Fatalf("建议里回填了被拒的原文 %q——照做仍会被中枢拒：%v", testCase.token, err)
			}
			if !strings.Contains(err.Error(), "canonical decimal form") {
				t.Fatalf("建议必须点明要规范十进制形式：%v", err)
			}
		})
	}
}

// TestGatewayNonNumberRejectionIsNotBlamedOnANumber 钉住「不许把别人的锅扣给数字」。
//
// `{"a":2,"a":1.5}` 的真实拒因是**重复键**（jcs 在解码第二个 value 之前就拒了）。
// 而 decodeGatewayJSONValue 走 encoding/json 是末键覆盖，树里只剩 a:1.5——
// 定位器若无条件运行，就会报「a 是小数」，接入方改完小数重复键还在，再失败一次。
// 二次审查（composer 路第二轮）实测到，处置是只在 jcs 确实因数字而拒时才定位。
func TestGatewayNonNumberRejectionIsNotBlamedOnANumber(t *testing.T) {
	// ⚠ 拒因取决于 **jcs token 流的顺序**，不是"这段 JSON 里有什么"：
	// `{"a":2,"a":1.5}` 读到第二个键就发现重复，小数还没被解析 ⇒ 拒因是重复键；
	// `{"a":1.5,"a":2}` 小数先被读到 ⇒ 拒因是数字。两者预期不同，别写成同一条。
	// 初版把后者也预期成重复键，红的是测试不是实现。
	cases := []struct {
		name       string
		parameters string
		wantReason string
	}{
		{"重复键先于小数被读到", `{"a":2,"a":1.5}`, "duplicate key"},
		{"嵌套重复键", `{"outer":{"a":2,"a":1.5}}`, "duplicate key"},
		{"小数先于重复键被读到", `{"a":1.5,"a":2}`, "json number must be integer decimal string form"},
	}
	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			err := speechRequestWithParameters(testCase.parameters).Validate()
			if err == nil {
				t.Fatalf("必须被拒：%s", testCase.parameters)
			}
			if !strings.Contains(err.Error(), testCase.wantReason) {
				t.Fatalf("必须报 jcs 的真实拒因 %q：%v", testCase.wantReason, err)
			}
			// 关键断言：不得把非数字拒因说成「这个小数不能规范化」。
			// 末键覆盖会让树里只剩合法值或只剩非法值，定位器无条件运行就会张冠李戴。
			if testCase.wantReason == "duplicate key" &&
				strings.Contains(err.Error(), "cannot canonicalize: request fingerprints") {
				t.Fatalf("不得把重复键报成小数问题——改完小数还会再失败一次：%v", err)
			}
		})
	}
}

// TestGatewayNumberRejectionStringsStillMatchJCS 钉住 isJCSNumberRejection 认的两条串。
//
// 它靠字符串匹配识别 jcs 的数字拒因（中枢也是这么做的）。串一变，
// 「拒因是数字」就会被判成 false，于是所有小数错误退化成没有字段路径的兜底报错——
// 请求照样被拒，报错质量却悄悄掉一档。这条测试让那种退化当场可见。
func TestGatewayNumberRejectionStringsStillMatchJCS(t *testing.T) {
	if _, err := jcs.CanonicalizeJSON([]byte(`{"a":1.5}`)); err == nil {
		t.Fatal("jcs 必须拒小数")
	} else if !isJCSNumberRejection(err) {
		t.Fatalf("小数拒因没被识别成数字类，字符串对不上了：%v", err)
	}
	if _, err := jcs.CanonicalizeJSON([]byte(`{"a":99999999999999999999}`)); err == nil {
		t.Fatal("jcs 必须拒超出 int64 的整数")
	} else if !isJCSNumberRejection(err) {
		t.Fatalf("int64 越界拒因没被识别成数字类，字符串对不上了：%v", err)
	}
	// 负对照：非数字拒因不得被认成数字类，否则第一条守卫失去意义。
	if _, err := jcs.CanonicalizeJSON([]byte(`{"a":1,"a":2}`)); err == nil {
		t.Fatal("jcs 必须拒重复键")
	} else if isJCSNumberRejection(err) {
		t.Fatalf("重复键被误认成数字拒因：%v", err)
	}
}

// TestGatewayNumberErrorIsDeterministic 钉住报错的确定性。
//
// Go 的 map 迭代顺序是随机的。含两个坏字段的请求如果不排序遍历，报哪一个每次都可能不同——
// 对接方看到飘忽的报错，测试也会间歇性红。
func TestGatewayNumberErrorIsDeterministic(t *testing.T) {
	const parameters = `{"zulu":1.5,"alpha":2.5,"mike":3.5}`
	first := validateGatewayParameters([]byte(parameters))
	if first == nil {
		t.Fatal("多个坏字段必须被拒")
	}
	for attempt := 0; attempt < 50; attempt++ {
		again := validateGatewayParameters([]byte(parameters))
		if again == nil || again.Error() != first.Error() {
			t.Fatalf("第 %d 次报错与首次不同：%v vs %v", attempt, again, first)
		}
	}
	// 排序遍历 ⇒ 报的是字典序最小的那个字段。
	if !strings.Contains(first.Error(), "spec.parameters.alpha") {
		t.Fatalf("确定性报错应点名字典序最小的 alpha：%v", first)
	}
}

func TestGatewayCreateRejectsShortIdempotencyKey(t *testing.T) {
	if err := validateGatewayIdempotencyKey("short"); err == nil {
		t.Fatal("short idempotency key was accepted")
	}
	if err := validateGatewayIdempotencyKey("this-is-a-valid-idempotency-key"); err != nil {
		t.Fatalf("valid idempotency key rejected: %v", err)
	}
}

func TestCreateRequestAlwaysSerializesModerationReceiptKey(t *testing.T) {
	encoded, err := json.Marshal(GatewayCreateRequest{
		SKU:               "moderation.generate.v1",
		TaskRef:           "guard-task",
		ModerationReceipt: "",
	})
	if err != nil {
		t.Fatalf("marshal 失败: %v", err)
	}

	var probe map[string]json.RawMessage
	if err := json.Unmarshal(encoded, &probe); err != nil {
		t.Fatalf("unmarshal 失败: %v", err)
	}
	if _, ok := probe["moderation_receipt"]; !ok {
		t.Fatalf("moderation_receipt 键必须出现在线级请求里（禁止 omitempty），实际 body=%s", encoded)
	}
	if got := string(probe["moderation_receipt"]); got != `""` {
		t.Fatalf("空收据必须序列化成空串，实际 %s", got)
	}
}
