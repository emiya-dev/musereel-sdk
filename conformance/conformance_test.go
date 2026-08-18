//go:build conformance

package conformance

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	sdk "github.com/emiya-dev/musereel-sdk"
	runtimepb "github.com/emiya-dev/musereel-sdk/runtime"
)

// TestSluiceComposeConformance 只接受真实 compose 环境；环境不全时显式失败。
func TestSluiceComposeConformance(t *testing.T) {
	if testing.Short() {
		t.Skip("真实 sluice compose conformance 在 -short 下跳过")
	}
	cfg, err := loadConfig()
	if err != nil {
		t.Fatalf("conformance 未配置：%v", err)
	}
	ctx, cancel := context.WithTimeout(context.Background(), 15*time.Minute)
	defer cancel()
	if err := run(ctx, cfg); err != nil {
		t.Fatalf("conformance 失败：%v", err)
	}
}

func TestArtifactRefsFromResultBySKU(t *testing.T) {
	const invocationID = "74fa0dff-0000-0000-0000-000000000001"
	videoID := "11111111-1111-1111-1111-111111111111"
	imageIDOne := "22222222-2222-2222-2222-222222222222"
	imageIDTwo := "33333333-3333-3333-3333-333333333333"
	musicID := "44444444-4444-4444-4444-444444444444"
	speechID := "55555555-5555-5555-5555-555555555555"
	artifactPath := func(artifactID string) string {
		return "/runtime/v1/invocations/" + invocationID + "/artifacts/" + artifactID
	}

	tests := []struct {
		name     string
		skuID    string
		result   json.RawMessage
		wantRefs []conformanceArtifactRef
		wantErr  string
	}{
		{
			name:     "text has no artifact keys",
			skuID:    textGenerateSKU,
			result:   json.RawMessage(`{"text":"ok"}`),
			wantRefs: nil,
		},
		{
			name:     "moderation receipt is not an artifact",
			skuID:    moderationGenerateSKU,
			result:   json.RawMessage(`{"moderation_receipt":"e14-conformance"}`),
			wantRefs: nil,
		},
		{
			name:     "lyrics has no artifact keys",
			skuID:    lyricsGenerateSKU,
			result:   json.RawMessage(`{"lyrics":"a line"}`),
			wantRefs: nil,
		},
		{
			name:  "video uses singular artifact",
			skuID: videoGenerateSKU,
			result: json.RawMessage(fmt.Sprintf(
				`{"artifact":{"artifact_id":%q,"download_path":%q,"media_type":"video/mp4"}}`,
				videoID,
				artifactPath(videoID),
			)),
			wantRefs: []conformanceArtifactRef{{artifactID: videoID, downloadPath: artifactPath(videoID)}},
		},
		{
			name:  "image uses every artifact",
			skuID: imageGenerateSKU,
			result: json.RawMessage(fmt.Sprintf(
				`{"artifacts":[{"artifact_id":%q,"download_path":%q},{"artifact_id":%q,"download_path":%q}],"delivered_image_count":"2","requested_image_count":"2"}`,
				imageIDOne,
				artifactPath(imageIDOne),
				imageIDTwo,
				artifactPath(imageIDTwo),
			)),
			wantRefs: []conformanceArtifactRef{
				{artifactID: imageIDOne, downloadPath: artifactPath(imageIDOne)},
				{artifactID: imageIDTwo, downloadPath: artifactPath(imageIDTwo)},
			},
		},
		{
			name:  "music uses singular artifact",
			skuID: musicGenerateSKU,
			result: json.RawMessage(fmt.Sprintf(
				`{"artifact":{"artifact_id":%q,"download_path":%q}}`,
				musicID,
				artifactPath(musicID),
			)),
			wantRefs: []conformanceArtifactRef{{artifactID: musicID, downloadPath: artifactPath(musicID)}},
		},
		{
			name:  "speech uses singular artifact",
			skuID: speechGenerateSKU,
			result: json.RawMessage(fmt.Sprintf(
				`{"artifact":{"artifact_id":%q,"download_path":%q}}`,
				speechID,
				artifactPath(speechID),
			)),
			wantRefs: []conformanceArtifactRef{{artifactID: speechID, downloadPath: artifactPath(speechID)}},
		},
		{
			name:    "artifact SKU with empty result fails",
			skuID:   videoGenerateSKU,
			result:  nil,
			wantErr: "终态 snapshot.result 为空",
		},
		{
			name:  "artifact SKU without artifact id fails",
			skuID: musicGenerateSKU,
			result: json.RawMessage(fmt.Sprintf(
				`{"artifact":{"download_path":%q}}`,
				artifactPath(musicID),
			)),
			wantErr: "缺少 artifact_id",
		},
		{
			name:    "non artifact SKU with artifacts fails",
			skuID:   textGenerateSKU,
			result:  json.RawMessage(`{"artifacts":[],"delivered_image_count":"0"}`),
			wantErr: "不得包含 artifact 或 artifacts",
		},
		{
			name:  "image count must match artifacts",
			skuID: imageGenerateSKU,
			result: json.RawMessage(fmt.Sprintf(
				`{"artifacts":[{"artifact_id":%q,"download_path":%q}],"delivered_image_count":"2"}`,
				imageIDOne,
				artifactPath(imageIDOne),
			)),
			wantErr: "长度=1，不等于 delivered_image_count=2",
		},
		{
			name:  "artifact download path must bind invocation and id",
			skuID: speechGenerateSKU,
			result: json.RawMessage(fmt.Sprintf(
				`{"artifact":{"artifact_id":%q,"download_path":"/runtime/v1/invocations/wrong/artifacts/%s"}}`,
				speechID,
				speechID,
			)),
			wantErr: "不等于服务端 artifact 路径",
		},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			got, err := artifactRefsFromResult(testCase.skuID, invocationID, testCase.result)
			if testCase.wantErr != "" {
				if err == nil {
					t.Fatalf("sku=%q result unexpectedly accepted; want error containing %q", testCase.skuID, testCase.wantErr)
				}
				if !strings.Contains(err.Error(), testCase.wantErr) {
					t.Fatalf("sku=%q error=%q, want substring %q", testCase.skuID, err, testCase.wantErr)
				}
				return
			}
			if err != nil {
				t.Fatalf("sku=%q result rejected: %v", testCase.skuID, err)
			}
			if len(got) != len(testCase.wantRefs) {
				t.Fatalf("sku=%q artifact ref count=%d, want %d", testCase.skuID, len(got), len(testCase.wantRefs))
			}
			for index, want := range testCase.wantRefs {
				if got[index].artifactID != want.artifactID {
					t.Fatalf("sku=%q ref[%d].artifactID=%q, want %q", testCase.skuID, index, got[index].artifactID, want.artifactID)
				}
				if got[index].downloadPath != want.downloadPath {
					t.Fatalf("sku=%q ref[%d].downloadPath=%q, want %q", testCase.skuID, index, got[index].downloadPath, want.downloadPath)
				}
			}
		})
	}
}

// TestArtifactLegMarker 钉住这条腿的机器可读标记，以及两处 SKU 分类必须互相钉住这件事。
// 标记是外部矩阵判「artifact 传输腿跑没跑过」的唯一锚，所以它的**措辞**和
// 它**什么时候必须拒绝出具**同样重要：count=0 的 downloaded、以及分类漂移导致的
// 「非产物 SKU 却解出了引用」，都会让一条没跑的腿拿到跑过的凭证。
func TestArtifactLegMarker(t *testing.T) {
	tests := []struct {
		name       string
		skuID      string
		refCount   int
		wantMarker string
		wantErr    string
	}{
		{
			name:       "non artifact sku is skipped",
			skuID:      textGenerateSKU,
			refCount:   0,
			wantMarker: "ARTIFACT_LEG=skipped sku=text.generate.v1",
		},
		{
			name:       "moderation is skipped",
			skuID:      moderationGenerateSKU,
			refCount:   0,
			wantMarker: "ARTIFACT_LEG=skipped sku=moderation.generate.v1",
		},
		{
			name:       "image reports every downloaded artifact",
			skuID:      imageGenerateSKU,
			refCount:   2,
			wantMarker: "ARTIFACT_LEG=downloaded sku=image.generate.v1 count=2",
		},
		{
			name:       "music reports its single artifact",
			skuID:      musicGenerateSKU,
			refCount:   1,
			wantMarker: "ARTIFACT_LEG=downloaded sku=music.generate.v1 count=1",
		},
		{
			name:     "artifact sku with zero refs must not claim downloaded",
			skuID:    imageGenerateSKU,
			refCount: 0,
			wantErr:  "artifact 分类自相矛盾",
		},
		{
			name:     "non artifact sku with refs must not claim skipped",
			skuID:    textGenerateSKU,
			refCount: 1,
			wantErr:  "artifact 分类自相矛盾",
		},
		{
			name:     "unknown sku with refs is a contradiction",
			skuID:    "unknown.generate.v1",
			refCount: 1,
			wantErr:  "artifact 分类自相矛盾",
		},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			marker, err := artifactLegMarker(testCase.skuID, testCase.refCount)
			if testCase.wantErr != "" {
				if err == nil {
					t.Fatalf("sku=%q refCount=%d 竟然出具了标记 %q，期望报错含 %q",
						testCase.skuID, testCase.refCount, marker, testCase.wantErr)
				}
				if !strings.Contains(err.Error(), testCase.wantErr) {
					t.Fatalf("sku=%q error=%q，期望含子串 %q", testCase.skuID, err, testCase.wantErr)
				}
				if marker != "" {
					t.Fatalf("sku=%q 报错时仍返回了标记 %q，矩阵会拿它当跑过的凭证", testCase.skuID, marker)
				}
				return
			}
			if err != nil {
				t.Fatalf("sku=%q refCount=%d 被拒: %v", testCase.skuID, testCase.refCount, err)
			}
			if marker != testCase.wantMarker {
				t.Fatalf("sku=%q marker=%q, want %q", testCase.skuID, marker, testCase.wantMarker)
			}
		})
	}
}

func TestConformanceSchemaVersionDefaults(t *testing.T) {
	oneVersionSKUs := []string{
		textGenerateSKU,
		moderationGenerateSKU,
		imageGenerateSKU,
		lyricsGenerateSKU,
		musicGenerateSKU,
		speechGenerateSKU,
	}
	for _, skuID := range oneVersionSKUs {
		skuID := skuID
		t.Run(skuID, func(t *testing.T) {
			got, err := defaultSchemaVersionForSKU(skuID)
			if err != nil {
				t.Fatalf("schema_version lookup failed for sku=%q: %v", skuID, err)
			}
			if got != conformanceSchemaVersionOne {
				t.Fatalf("sku=%q schema_version=%q, want %q", skuID, got, conformanceSchemaVersionOne)
			}
		})
	}

	t.Run(videoGenerateSKU, func(t *testing.T) {
		got, err := defaultSchemaVersionForSKU(videoGenerateSKU)
		if err != nil {
			t.Fatalf("schema_version lookup failed for sku=%q: %v", videoGenerateSKU, err)
		}
		if got != conformanceVideoSchemaVersion {
			t.Fatalf("sku=%q schema_version=%q, want %q", videoGenerateSKU, got, conformanceVideoSchemaVersion)
		}
	})
}

func TestConformanceSchemaVersionOverrideAndUnknownSKU(t *testing.T) {
	got, err := resolveConformanceSchemaVersion(videoGenerateSKU, " explicit ")
	if err != nil {
		t.Fatalf("schema_version override failed: %v", err)
	}
	if got != "explicit" {
		t.Fatalf("schema_version override=%q, want %q", got, "explicit")
	}

	if _, err := resolveConformanceSchemaVersion("unknown.generate.v1", "explicit"); err == nil {
		t.Fatal("unknown SKU with an override unexpectedly succeeded")
	}
}

func TestModerationTargetSchemaVersionUsesTargetSKU(t *testing.T) {
	input, _, err := buildConformanceSpec(moderationGenerateSKU, conformanceVideoSchemaVersion)
	if err != nil {
		t.Fatalf("build moderation spec failed: %v", err)
	}

	var moderationSpec struct {
		TargetSKUID string `json:"target_sku_id"`
		TargetSpec  struct {
			SchemaVersion string `json:"schema_version"`
		} `json:"target_spec"`
	}
	if err := json.Unmarshal(input, &moderationSpec); err != nil {
		t.Fatalf("decode moderation spec failed: %v", err)
	}
	if moderationSpec.TargetSKUID != textGenerateSKU {
		t.Fatalf("moderation target_sku_id=%q, want %q", moderationSpec.TargetSKUID, textGenerateSKU)
	}
	want, err := defaultSchemaVersionForSKU(textGenerateSKU)
	if err != nil {
		t.Fatalf("target schema_version lookup failed: %v", err)
	}
	if moderationSpec.TargetSpec.SchemaVersion != want {
		t.Fatalf("moderation target schema_version=%q, want target SKU %q default %q", moderationSpec.TargetSpec.SchemaVersion, textGenerateSKU, want)
	}
}

func TestGatewayTerminalSuccessAssertion(t *testing.T) {
	tests := []struct {
		name      string
		state     sdk.GatewayInvocationState
		terminal  bool
		errorCode string
		wantError bool
	}{
		{name: "completed", state: sdk.GatewayStateCompleted, terminal: true},
		{name: "completed_without_terminal_flag", state: sdk.GatewayStateCompleted, terminal: false, wantError: true},
		{name: "failed_with_server_reason", state: sdk.GatewayStateFailed, terminal: true, errorCode: sdk.GatewayInternalError, wantError: true},
		{name: "cancelled_without_server_reason", state: sdk.GatewayStateCancelled, terminal: true, wantError: true},
		{name: "reconciling", state: sdk.GatewayStateReconciling, terminal: false, wantError: true},
		{name: "settlement_shortfall", state: sdk.GatewayStateSettlementShortfall, terminal: false, wantError: true},
		{name: "accepted", state: sdk.GatewayStateAccepted, terminal: false, wantError: true},
		{name: "running", state: sdk.GatewayStateRunning, terminal: false, wantError: true},
		{name: "cancel_pending", state: sdk.GatewayStateCancelPending, terminal: false, wantError: true},
		{name: "unknown", state: sdk.GatewayInvocationState("future_state"), terminal: true, wantError: true},
	}

	for _, testCase := range tests {
		t.Run(testCase.name, func(t *testing.T) {
			snapshot := &sdk.GatewayInvocationSnapshot{
				State:    testCase.state,
				Terminal: testCase.terminal,
			}
			if testCase.errorCode != "" {
				snapshot.Error = &sdk.GatewayError{Code: testCase.errorCode, Message: "synthetic server reason"}
			}

			err := assertSuccessfulGatewayTerminal(snapshot)
			if testCase.wantError {
				if err == nil {
					t.Fatalf("state=%q unexpectedly accepted", testCase.state)
				}
				if !strings.Contains(err.Error(), string(testCase.state)) {
					t.Fatalf("error=%q does not identify state=%q", err, testCase.state)
				}
				if testCase.errorCode != "" && !strings.Contains(err.Error(), testCase.errorCode) {
					t.Fatalf("error=%q does not include server reason code=%q", err, testCase.errorCode)
				}
				return
			}
			if err != nil {
				t.Fatalf("state=%q rejected: %v", testCase.state, err)
			}
		})
	}
}

func TestIdentityReplayComparesBusinessFieldsAndRequiresNewRequestID(t *testing.T) {
	first, second := identityReplayFixture()
	if err := assertIdentityReplay(first, second, "event-019"); err != nil {
		t.Fatalf("same business facts with distinct request_id rejected: %v", err)
	}

	tests := []struct {
		name   string
		mutate func(*runtimepb.IdentityReply)
	}{
		{name: "event_id", mutate: func(reply *runtimepb.IdentityReply) { reply.EventId = "event-other" }},
		{name: "identity_id", mutate: func(reply *runtimepb.IdentityReply) { reply.IdentityId = "identity-other" }},
		{name: "actor", mutate: func(reply *runtimepb.IdentityReply) { reply.Actor = "actor-other" }},
		{name: "identity_status", mutate: func(reply *runtimepb.IdentityReply) { reply.IdentityStatus = "disabled" }},
		{name: "verification_status", mutate: func(reply *runtimepb.IdentityReply) { reply.VerificationStatus = "verified" }},
		{name: "state_changed_at_ms", mutate: func(reply *runtimepb.IdentityReply) { reply.StateChangedAtMs++ }},
		{name: "verification_changed_at_ms", mutate: func(reply *runtimepb.IdentityReply) { reply.VerificationChangedAtMs = nil }},
		{name: "event_applied", mutate: func(reply *runtimepb.IdentityReply) { reply.EventApplied = false }},
	}
	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			first, second := identityReplayFixture()
			testCase.mutate(second)
			if err := assertIdentityReplay(first, second, "event-019"); err == nil {
				t.Fatalf("business field %s mismatch was accepted", testCase.name)
			}
		})
	}

	t.Run("same request_id is rejected", func(t *testing.T) {
		first, second := identityReplayFixture()
		second.RequestId = first.RequestId
		if err := assertIdentityReplay(first, second, "event-019"); err == nil || !strings.Contains(err.Error(), "request_id") {
			t.Fatalf("same request_id result = %v, want request_id error", err)
		}
	})
}

func identityReplayFixture() (*runtimepb.IdentityReply, *runtimepb.IdentityReply) {
	firstVerificationChangedAtMs := int64(1800000000123)
	secondVerificationChangedAtMs := int64(1800000000123)
	return &runtimepb.IdentityReply{
			RequestId:               "request-019-first",
			EventId:                 "event-019",
			IdentityId:              "identity-019",
			Actor:                   "actor-019",
			IdentityStatus:          "active",
			VerificationStatus:      "pending",
			StateChangedAtMs:        1800000000000,
			VerificationChangedAtMs: &firstVerificationChangedAtMs,
			EventApplied:            true,
		}, &runtimepb.IdentityReply{
			RequestId:               "request-019-second",
			EventId:                 "event-019",
			IdentityId:              "identity-019",
			Actor:                   "actor-019",
			IdentityStatus:          "active",
			VerificationStatus:      "pending",
			StateChangedAtMs:        1800000000000,
			VerificationChangedAtMs: &secondVerificationChangedAtMs,
			EventApplied:            true,
		}
}

func TestCancelLegMarkerKeepsBothLegalOutcomesObservable(t *testing.T) {
	const skuID = musicGenerateSKU
	tests := []struct {
		name        string
		response    sdk.GatewayCancelResponse
		cancelErr   error
		wantMarker  string
		wantFailure bool
	}{
		{
			name:       "accepted",
			response:   sdk.GatewayCancelResponse{StatusCode: 202, Accepted: true},
			wantMarker: "CANCEL_LEG=accepted sku=" + skuID,
		},
		{
			name:       "already terminal",
			cancelErr:  &sdk.GatewayError{Code: sdk.GatewayInvocationTransitionConflict, HTTPStatus: 409},
			wantMarker: "CANCEL_LEG=already_terminal sku=" + skuID,
		},
		{
			name:        "other gateway error still fails",
			cancelErr:   &sdk.GatewayError{Code: sdk.GatewayInternalError, HTTPStatus: 409},
			wantFailure: true,
		},
		{
			name:        "transition conflict with wrong status still fails",
			cancelErr:   &sdk.GatewayError{Code: sdk.GatewayInvocationTransitionConflict, HTTPStatus: 500},
			wantFailure: true,
		},
		{
			name:        "unexpected success status fails",
			response:    sdk.GatewayCancelResponse{StatusCode: 201},
			wantFailure: true,
		},
		{
			name:        "accepted response must expose accepted",
			response:    sdk.GatewayCancelResponse{StatusCode: 202},
			wantFailure: true,
		},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			marker, err := cancelLegMarker(skuID, testCase.response, testCase.cancelErr)
			if testCase.wantFailure {
				if err == nil {
					t.Fatalf("cancel result unexpectedly accepted with marker %q", marker)
				}
				if marker != "" {
					t.Fatalf("failed cancel result emitted marker %q", marker)
				}
				return
			}
			if err != nil {
				t.Fatalf("cancel result rejected: %v", err)
			}
			if marker != testCase.wantMarker {
				t.Fatalf("marker=%q, want %q", marker, testCase.wantMarker)
			}
		})
	}
}

// TestModerationSKUCarriesNoModerationReceipt 钉的是「moderation 自己就是产出收据的那一次调用」。
//
// 这不是风格问题：中枢对 moderation SKU 携带收据是显式拒绝，而该拒绝的内部码
// compliance_invalid_request 不在 gateway 冻结公共码集合里，会被折叠成
// internal_error / HTTP 500。⇒ 一旦这里回退，suite 收到的是「内部错误」，
// 既看不出是自己多发了字段，也和真正的中枢故障完全同形。实测过一次：
// 七 SKU 扫描里只有 moderation 报 500，gateway 侧零条错误日志、零 compliance 行。
//
// 断言必须逐 SKU 遍历而不是只测 moderation 一个：只测 moderation 的话，
// 把实现改成「所有 SKU 都不带收据」照样绿，而那会让另外六个 SKU 的收据闸失去覆盖。
func TestModerationSKUCarriesNoModerationReceipt(t *testing.T) {
	const receipt = "e14-conformance"

	t.Run(moderationGenerateSKU, func(t *testing.T) {
		if got := moderationReceiptForSKU(moderationGenerateSKU, receipt); got != "" {
			t.Fatalf("moderation SKU 必须不携带审核收据，实际 %q", got)
		}
	})

	for _, skuID := range []string{
		textGenerateSKU,
		videoGenerateSKU,
		imageGenerateSKU,
		lyricsGenerateSKU,
		musicGenerateSKU,
		speechGenerateSKU,
	} {
		skuID := skuID
		t.Run(skuID, func(t *testing.T) {
			if got := moderationReceiptForSKU(skuID, receipt); got != receipt {
				t.Fatalf("sku=%q 必须原样携带审核收据 %q，实际 %q", skuID, receipt, got)
			}
		})
	}
}

func TestBuildModerationRequestUsesSerializedTargetSpec(t *testing.T) {
	const taskRef = "task-receipt-017"
	targetSpecBytes := []byte(`{"schema_version":"3","input":{"prompt":"same target"},"parameters":{"seconds":"4"}}`)
	request, err := buildModerationRequest(config{skuID: videoGenerateSKU, taskRef: taskRef}, targetSpecBytes)
	if err != nil {
		t.Fatalf("build moderation request failed: %v", err)
	}
	if request.SKU != moderationGenerateSKU {
		t.Fatalf("moderation request sku=%q, want %q", request.SKU, moderationGenerateSKU)
	}
	if request.TaskRef != taskRef {
		t.Fatalf("moderation request task_ref=%q, want %q", request.TaskRef, taskRef)
	}
	if request.Spec.SchemaVersion != conformanceSchemaVersionOne {
		t.Fatalf("moderation request schema_version=%q, want %q", request.Spec.SchemaVersion, conformanceSchemaVersionOne)
	}
	if request.ModerationReceipt != "" {
		t.Fatalf("moderation request carried receipt %q", request.ModerationReceipt)
	}

	input, ok := request.Spec.Input.(json.RawMessage)
	if !ok {
		t.Fatalf("moderation request input type=%T, want json.RawMessage", request.Spec.Input)
	}
	var moderationInput struct {
		TargetSKUID string          `json:"target_sku_id"`
		TargetSpec  json.RawMessage `json:"target_spec"`
	}
	if err := json.Unmarshal(input, &moderationInput); err != nil {
		t.Fatalf("decode moderation input failed: %v", err)
	}
	if moderationInput.TargetSKUID != videoGenerateSKU {
		t.Fatalf("target_sku_id=%q, want %q", moderationInput.TargetSKUID, videoGenerateSKU)
	}
	if string(moderationInput.TargetSpec) != string(targetSpecBytes) {
		t.Fatalf("target_spec=%s, want exact serialized bytes %s", moderationInput.TargetSpec, targetSpecBytes)
	}
}

func TestModerationReceiptResultFailureReasons(t *testing.T) {
	tests := []struct {
		name       string
		result     moderationInvocationResult
		wantToken  string
		wantReason string
	}{
		{
			name:       "reject verdict",
			result:     moderationInvocationResult{Kind: "moderation", Verdict: "reject", ModerationReceipt: "ignored"},
			wantReason: "reason=moderation_verdict_not_pass",
		},
		{
			name:       "pass without token",
			result:     moderationInvocationResult{Kind: "moderation", Verdict: "pass"},
			wantReason: "reason=moderation_pass_receipt_empty",
		},
		{
			name:      "pass with token",
			result:    moderationInvocationResult{Kind: "moderation", Verdict: "pass", ModerationReceipt: "receipt-main"},
			wantToken: "receipt-main",
		},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			got, err := receiptFromModerationResult(videoGenerateSKU, testCase.result)
			if testCase.wantReason != "" {
				if err == nil {
					t.Fatalf("result unexpectedly accepted with token %q", got)
				}
				if !strings.Contains(err.Error(), "RECEIPT_CHAIN=mint_failed sku="+videoGenerateSKU) || !strings.Contains(err.Error(), testCase.wantReason) {
					t.Fatalf("error=%q, want sku and reason %q", err, testCase.wantReason)
				}
				return
			}
			if err != nil {
				t.Fatalf("pass result rejected: %v", err)
			}
			if got != testCase.wantToken {
				t.Fatalf("token=%q, want %q", got, testCase.wantToken)
			}
		})
	}
}

func TestReceiptMintFailureClassification(t *testing.T) {
	blocked := receiptMintInvocationFailure(imageGenerateSKU, &sdk.GatewayError{Code: sdk.GatewayInvalidInvocationRequest})
	if blocked == nil || !strings.Contains(blocked.Error(), "RECEIPT_CHAIN=blocked sku="+imageGenerateSKU+" reason=hub_target_shape_unsupported") {
		t.Fatalf("blocked error=%v, want hub target shape marker", blocked)
	}
	if !strings.Contains(blocked.Error(), "中枢当前只能审核 text 形与 video 形目标") {
		t.Fatalf("blocked error=%q lacks human explanation", blocked)
	}

	invocationFailure := receiptMintInvocationFailure(videoGenerateSKU, fmt.Errorf("transport down"))
	if invocationFailure == nil || !strings.Contains(invocationFailure.Error(), "reason=moderation_invocation_failed") {
		t.Fatalf("invocation failure=%v, want moderation invocation reason", invocationFailure)
	}
	if strings.Contains(invocationFailure.Error(), "hub_target_shape_unsupported") {
		t.Fatalf("generic invocation failure was classified as shape unsupported: %q", invocationFailure)
	}
}

func TestReceiptChainMarkersEnforceSKUClassification(t *testing.T) {
	const taskRef = "task-receipt-017"
	tests := []struct {
		name       string
		marker     string
		wantMarker string
		wantErr    string
	}{
		{
			name:       "moderation skipped",
			marker:     "skipped",
			wantMarker: "RECEIPT_CHAIN=skipped sku=" + moderationGenerateSKU,
		},
		{
			name:       "main minted",
			marker:     "minted-main",
			wantMarker: "RECEIPT_CHAIN=minted sku=" + videoGenerateSKU + " leg=main task_ref=" + taskRef,
		},
		{
			name:       "cancel minted",
			marker:     "minted-cancel",
			wantMarker: "RECEIPT_CHAIN=minted sku=" + videoGenerateSKU + " leg=cancel task_ref=" + taskRef,
		},
		{
			name:       "main presented",
			marker:     "presented-main",
			wantMarker: "RECEIPT_CHAIN=presented sku=" + videoGenerateSKU + " leg=main",
		},
		{
			name:       "cancel presented",
			marker:     "presented-cancel",
			wantMarker: "RECEIPT_CHAIN=presented sku=" + videoGenerateSKU + " leg=cancel",
		},
		{
			name:    "non moderation cannot be skipped",
			marker:  "bad-skip",
			wantErr: "不得打 skipped",
		},
		{
			name:    "moderation cannot be minted",
			marker:  "bad-mint",
			wantErr: "不得铸造收据",
		},
		{
			name:    "non moderation cannot mint empty receipt",
			marker:  "empty-mint",
			wantErr: "没有已铸造收据",
		},
		{
			name:    "moderation cannot skip while carrying receipt",
			marker:  "skip-with-receipt",
			wantErr: "不得携带收据",
		},
		{
			name:    "moderation cannot be presented",
			marker:  "bad-present",
			wantErr: "不得打 presented",
		},
		{
			name:    "non moderation cannot present empty receipt",
			marker:  "empty-present",
			wantErr: "没有可呈递收据",
		},
		{
			name:    "minted rejects unknown leg",
			marker:  "minted-bad-leg",
			wantErr: "收据腿无效",
		},
		{
			name:    "presented rejects unknown leg",
			marker:  "presented-bad-leg",
			wantErr: "收据腿无效",
		},
	}

	for _, testCase := range tests {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			var marker string
			var err error
			switch testCase.marker {
			case "skipped":
				marker, err = receiptChainSkippedMarker(moderationGenerateSKU, "")
			case "minted-main":
				marker, err = receiptChainMintedMarker(videoGenerateSKU, receiptLegMain, taskRef, "receipt-main")
			case "minted-cancel":
				marker, err = receiptChainMintedMarker(videoGenerateSKU, receiptLegCancel, taskRef, "receipt-cancel")
			case "presented-main":
				marker, err = receiptChainPresentedMarker(videoGenerateSKU, receiptLegMain, "receipt-main")
			case "presented-cancel":
				marker, err = receiptChainPresentedMarker(videoGenerateSKU, receiptLegCancel, "receipt-cancel")
			case "bad-skip":
				marker, err = receiptChainSkippedMarker(videoGenerateSKU, "")
			case "bad-mint":
				marker, err = receiptChainMintedMarker(moderationGenerateSKU, receiptLegMain, taskRef, "receipt")
			case "empty-mint":
				marker, err = receiptChainMintedMarker(videoGenerateSKU, receiptLegMain, taskRef, "")
			case "skip-with-receipt":
				marker, err = receiptChainSkippedMarker(moderationGenerateSKU, "receipt")
			case "bad-present":
				marker, err = receiptChainPresentedMarker(moderationGenerateSKU, receiptLegMain, "receipt")
			case "empty-present":
				marker, err = receiptChainPresentedMarker(videoGenerateSKU, receiptLegMain, "")
			case "minted-bad-leg":
				marker, err = receiptChainMintedMarker(videoGenerateSKU, "bogus", taskRef, "receipt")
			case "presented-bad-leg":
				marker, err = receiptChainPresentedMarker(videoGenerateSKU, "bogus", "receipt")
			default:
				t.Fatalf("unknown marker fixture %q", testCase.marker)
			}
			if testCase.wantErr != "" {
				if err == nil || !strings.Contains(err.Error(), testCase.wantErr) {
					t.Fatalf("marker=%q error=%v, want error containing %q", marker, err, testCase.wantErr)
				}
				if marker != "" {
					t.Fatalf("failed marker returned %q", marker)
				}
				return
			}
			if err != nil {
				t.Fatalf("marker rejected: %v", err)
			}
			if marker != testCase.wantMarker {
				t.Fatalf("marker=%q, want %q", marker, testCase.wantMarker)
			}
		})
	}
}

func TestTargetCreateFailurePreservesModerationDetailsOnlyForReceiptErrors(t *testing.T) {
	details := map[string]any{
		"fields": []any{"messages"},
		"reason": "receipt consumed",
	}
	receiptFailure := targetCreateFailure(imageGenerateSKU, "target create", &sdk.GatewayError{
		Code:    sdk.GatewayModerationInvalidRequest,
		Details: details,
	})
	encodedDetails, err := json.Marshal(details)
	if err != nil {
		t.Fatalf("marshal details fixture failed: %v", err)
	}
	if !strings.Contains(receiptFailure.Error(), "收据链被中枢拒绝") || !strings.Contains(receiptFailure.Error(), "details="+string(encodedDetails)) {
		t.Fatalf("receipt failure=%q, want receipt classification and details %s", receiptFailure, encodedDetails)
	}

	targetFailure := targetCreateFailure(imageGenerateSKU, "target create", &sdk.GatewayError{Code: sdk.GatewayInvalidInvocationRequest})
	if strings.Contains(strings.ToLower(targetFailure.Error()), "receipt") || strings.Contains(targetFailure.Error(), "收据") {
		t.Fatalf("non-receipt target failure mentions receipt: %q", targetFailure)
	}
}

func TestReceiptReplayResultMarkerAcceptsBothObservedOutcomes(t *testing.T) {
	accepted, err := receiptReplayResultMarker(videoGenerateSKU, sdk.GatewayCreateResponse{StatusCode: 202}, nil)
	if err != nil || accepted != "RECEIPT_CHAIN=replay_accepted sku="+videoGenerateSKU {
		t.Fatalf("accepted marker=%q err=%v", accepted, err)
	}

	for _, code := range []string{sdk.GatewayModerationInvalidRequest, sdk.GatewayComplianceRejected} {
		code := code
		t.Run(code, func(t *testing.T) {
			rejected, rejectErr := receiptReplayResultMarker(videoGenerateSKU, sdk.GatewayCreateResponse{}, &sdk.GatewayError{Code: code})
			if rejectErr != nil || rejected != "RECEIPT_CHAIN=replay_rejected sku="+videoGenerateSKU {
				t.Fatalf("rejected marker=%q err=%v for code=%s", rejected, rejectErr, code)
			}
		})
	}

	if _, err := receiptReplayResultMarker(videoGenerateSKU, sdk.GatewayCreateResponse{}, fmt.Errorf("unrelated failure")); err == nil {
		t.Fatal("unrelated replay probe error was accepted")
	}
	if _, err := receiptReplayResultMarker(videoGenerateSKU, sdk.GatewayCreateResponse{StatusCode: 303, AlreadyExists: true}, nil); err == nil {
		t.Fatal("unexpected idempotent response was accepted as replay probe outcome")
	}
}

// 铸造前置的两道校验各自独立：目标 bytes 不合法就不该发出 moderation 请求，
// 而终态 result 不是 moderation 形状时不能被当成「没拿到收据」——那会把中枢换了
// 结果形状这件事误报成收据链失败，归因规则第 1 条就此失效。
func TestBuildModerationRequestRejectsUnusableTargetSpec(t *testing.T) {
	for _, testCase := range []struct {
		name       string
		targetSpec []byte
	}{
		{name: "empty", targetSpec: nil},
		{name: "blank", targetSpec: []byte("   ")},
		{name: "not json", targetSpec: []byte(`{"schema_version":`)},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := buildModerationRequest(config{skuID: videoGenerateSKU, taskRef: "task"}, testCase.targetSpec); err == nil {
				t.Fatal("unusable target spec bytes were accepted")
			}
		})
	}
}

func TestParseModerationInvocationResultRejectsForeignShapes(t *testing.T) {
	for _, testCase := range []struct {
		name   string
		result json.RawMessage
	}{
		{name: "empty", result: nil},
		{name: "blank", result: json.RawMessage("  ")},
		{name: "not json", result: json.RawMessage(`{"kind":`)},
		{name: "wrong kind", result: json.RawMessage(`{"kind":"image","verdict":"pass","moderation_receipt":"r"}`)},
	} {
		testCase := testCase
		t.Run(testCase.name, func(t *testing.T) {
			if _, err := parseModerationInvocationResult(testCase.result); err == nil {
				t.Fatal("foreign moderation result shape was accepted")
			}
		})
	}

	parsed, err := parseModerationInvocationResult(json.RawMessage(`{"kind":"moderation","verdict":"pass","moderation_receipt":"r","receipt_expires_at_ms":1}`))
	if err != nil {
		t.Fatalf("valid moderation result rejected: %v", err)
	}
	if parsed.ModerationReceipt != "r" {
		t.Fatalf("moderation_receipt=%q, want %q", parsed.ModerationReceipt, "r")
	}
}
