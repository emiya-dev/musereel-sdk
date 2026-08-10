//go:build conformance

package conformance

import (
	"context"
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
