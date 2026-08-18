//go:build conformance

package conformance

import (
	"context"
	"encoding/json"
	"strings"
	"testing"
	"time"

	sdk "github.com/emiya-dev/musereel-sdk"
)

// TestSluiceComposeConformance 只接受真实 compose 环境；环境不全时显式失败。
func TestSluiceComposeConformance(t *testing.T) {
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
