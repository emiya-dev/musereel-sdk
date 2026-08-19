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
	body := []byte(`{"request_id":"req-err","error":{"code":"rate_limited","message":"diagnostic","retryable":false,"retry_after_ms":1200,"details":{"invocation_id":"inv-01"}}}`)
	err := gatewayErrorFromBytes(body, http.StatusTooManyRequests, isGatewayInvocationErrorCode)
	if err.Code != GatewayRateLimited || err.Retryable || !err.RetryableByCode() || err.RetryAfterMS == nil || *err.RetryAfterMS != 1200 || err.InvocationID != "inv-01" {
		t.Fatalf("wire mismatch error = %#v", err)
	}
	if got := err.Error(); strings.Contains(got, "diagnostic") || strings.Contains(got, "1200") {
		t.Fatalf("error string used unstable diagnostic data: %q", got)
	}
}

func TestGatewayCreateAllowsSpeechNumericParameters(t *testing.T) {
	request := validGatewayCreateRequest()
	request.SKU = "speech.generate.v1"
	request.TaskRef = "speech-task-01"
	request.Spec.Input = map[string]any{"text": "hello"}
	request.Spec.Parameters = json.RawMessage(`{"voice_id":"x","pitch":0,"speed":1.2,"vol":1.5}`)

	body, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal speech request: %v", err)
	}
	t.Logf("speech request body=%s", body)
	if err := request.Validate(); err != nil {
		t.Fatalf("speech numeric parameters were rejected: %v", err)
	}
}

func TestGatewayCreateAllowsNumbersInsideResponseFormatSchema(t *testing.T) {
	request := validGatewayCreateRequest()
	request.SKU = "text.generate.v1"
	request.TaskRef = "schema-task-01"
	request.Spec.Parameters = json.RawMessage(`{"response_format":{"type":"json_schema","json_schema":{"name":"answer","schema":{"type":"object","properties":{"title":{"type":"string","minLength":1},"score":{"type":"number","minimum":0,"maximum":1},"items":{"type":"array","minItems":1}}}}}}`)

	body, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal response format request: %v", err)
	}
	t.Logf("response_format request body=%s", body)
	if err := request.Validate(); err != nil {
		t.Fatalf("numeric JSON Schema constraints were rejected: %v", err)
	}
}

func TestGatewayCreateAllowsImageDecimalStringParameter(t *testing.T) {
	request := validGatewayCreateRequest()
	request.SKU = "image.generate.v1"
	request.TaskRef = "image-task-01"
	request.Spec.Parameters = json.RawMessage(`{"image_count":"4","resolution":"512","quality":"q2","aspect_ratio":"1:1"}`)

	body, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal image request: %v", err)
	}
	t.Logf("image request body=%s", body)
	if err := request.Validate(); err != nil {
		t.Fatalf("decimal-string image parameter was rejected: %v", err)
	}
}

func TestGatewayCreateRejectsDecimalStringNumericParameterAndShortKeys(t *testing.T) {
	request := validGatewayCreateRequest()
	request.SKU = "image.generate.v1"
	request.TaskRef = "image-task-invalid-01"
	request.Spec.Parameters = json.RawMessage(`{"image_count":4,"resolution":"512","quality":"q2","aspect_ratio":"1:1"}`)

	body, err := json.Marshal(request)
	if err != nil {
		t.Fatalf("marshal invalid image request: %v", err)
	}
	t.Logf("invalid image request body=%s", body)
	if err := request.Validate(); err == nil {
		t.Fatal("image_count JSON number was accepted")
	} else if !strings.Contains(err.Error(), "image_count") || !strings.Contains(err.Error(), "decimal string") {
		t.Fatalf("image_count error is not self-diagnosing: %v", err)
	} else {
		t.Logf("validation error=%v", err)
	}

	request.Spec.Parameters = map[string]string{"duration": "5"}
	if err := validateGatewayIdempotencyKey("short"); err == nil {
		t.Fatal("short idempotency key was accepted")
	}
}

// TestCreateRequestAlwaysSerializesModerationReceiptKey 钉的是「空收据必须仍然出现在线上」。
//
// gateway 的 parseCreateBody 显式要求 moderation_receipt 这个**键存在**，缺键当场 400
// （sluice backend/service/gateway/internal/httpapi/fingerprint.go:109-111）。而 moderation
// SKU 自己必须传空串——中枢 e2e 传的也是空串。两条合起来的结果是：这个字段既不能省、
// 又必须能为空，因此 json tag 绝不能加 omitempty。
//
// 加了 omitempty 不会有任何编译错误或类型错误，只会让 moderation 的请求悄悄少一个键，
// 而症状是七个 SKU 一起 400 —— 报错方向指向"请求结构错"，与真因"某人给一个字段加了
// 一个看起来无害的 tag"相隔很远。所以这条必须是机器判据，不能只写注释。
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

// TestGatewayParametersRejectEveryDecimalStringFieldAsNumber 钉住 decimal_string 白名单的**全集**。
//
// 为什么按字段逐个测：本轮改动把「拒绝一切 JSON number」收窄成「只拒绝 schema 声明为
// decimal_string 的字段」。收窄本身是对的，但白名单一旦漏一个字段，SDK 就悄悄不再拦它，
// 而中枢会在更靠后的地方用更难懂的信息拒掉——症状与「SDK 放行了本该放行的」完全同形。
// 实测：初版白名单只列了 image_count，漏掉了 video 的 seconds。
func TestGatewayParametersRejectEveryDecimalStringFieldAsNumber(t *testing.T) {
	for field := range gatewayDecimalStringParameters {
		raw := []byte(`{"` + field + `": 4}`)
		err := validateGatewayParameters(raw)
		if err == nil {
			t.Errorf("spec.parameters.%s 是 decimal_string 字段，写成 JSON number 必须被拒", field)
			continue
		}
		if !strings.Contains(err.Error(), field) {
			t.Errorf("拒绝 %s 时的报错必须指名该字段，接入方才能不看 SDK 源码自助定位：%v", field, err)
		}
		// 同一字段写成十进制字符串必须放行——否则上面的红只是"这个字段总是被拒"。
		if err := validateGatewayParameters([]byte(`{"` + field + `": "4"}`)); err != nil {
			t.Errorf("spec.parameters.%s 写成十进制字符串必须放行：%v", field, err)
		}
	}
	// 覆盖锚：白名单被清空时上面的循环一次都不执行而测试照样绿。
	if len(gatewayDecimalStringParameters) < 2 {
		t.Fatalf("decimal_string 白名单只剩 %d 项；中枢 golden 里至少有 image_count 与 seconds 两个",
			len(gatewayDecimalStringParameters))
	}
}
