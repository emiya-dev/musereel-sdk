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
