package musereelsdk

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"testing"
)

// frozenGatewayErrorContract 是 §4.2.7 公共错误码表在 SDK 测试中的镜像。
// constName 同时把镜像项锚到 gateway_errors.go 中的具体常量，避免只对一组
// 字符串做宽松的行为测试。
var frozenGatewayErrorContract = []struct {
	constName string
	code      string
	retryable bool
}{
	{constName: "GatewayInvalidInvocationRequest", code: "invalid_invocation_request", retryable: false},
	{constName: "GatewayModerationInvalidRequest", code: "moderation_invalid_request", retryable: false},
	{constName: "GatewayRuntimeUnauthenticated", code: "runtime_unauthenticated", retryable: false},
	{constName: "GatewayActorAssertionInvalid", code: "actor_assertion_invalid", retryable: false},
	{constName: "GatewayActorAssertionReplayed", code: "actor_assertion_replayed", retryable: false},
	{constName: "GatewayRuntimeForbidden", code: "runtime_forbidden", retryable: false},
	{constName: "GatewaySKUNotAllowed", code: "sku_not_allowed", retryable: false},
	{constName: "GatewayComplianceRejected", code: "compliance_rejected", retryable: false},
	{constName: "GatewayInvocationNotFound", code: "invocation_not_found", retryable: false},
	{constName: "GatewayInvocationArtifactNotFound", code: "invocation_artifact_not_found", retryable: false},
	{constName: "GatewayInvocationArtifactExpired", code: "invocation_artifact_expired", retryable: false},
	{constName: "GatewayInvocationDeliveryModeMismatch", code: "invocation_delivery_mode_mismatch", retryable: false},
	{constName: "GatewayInvocationIdempotencyConflict", code: "invocation_idempotency_conflict", retryable: false},
	{constName: "GatewayInvocationTransitionConflict", code: "invocation_transition_conflict", retryable: false},
	{constName: "GatewayInsufficientQuota", code: "insufficient_quota", retryable: false},
	{constName: "GatewayMemberLimitExceeded", code: "member_limit_exceeded", retryable: false},
	{constName: "GatewayRateLimited", code: "rate_limited", retryable: true},
	{constName: "GatewayUpstreamUnavailable", code: "upstream_unavailable", retryable: true},
	{constName: "GatewayInternalError", code: "internal_error", retryable: true},
}

func TestGatewayInvocationErrorCodesMatchFrozenContract(t *testing.T) {
	contractNames := make(map[string]struct{}, len(frozenGatewayErrorContract))
	contractCodes := make(map[string]struct{}, len(frozenGatewayErrorContract))
	for _, entry := range frozenGatewayErrorContract {
		if _, duplicate := contractNames[entry.constName]; duplicate {
			t.Fatalf("镜像表重复常量 %q", entry.constName)
		}
		if _, duplicate := contractCodes[entry.code]; duplicate {
			t.Fatalf("镜像表重复错误码 %q", entry.code)
		}
		contractNames[entry.constName] = struct{}{}
		contractCodes[entry.code] = struct{}{}

		if !isGatewayInvocationErrorCode(entry.code) {
			t.Errorf("契约错误码 %q 未被 isGatewayInvocationErrorCode 接受", entry.code)
		}
		if got := RetryableGatewayCode(entry.code); got != entry.retryable {
			t.Errorf("RetryableGatewayCode(%q) = %t, want %t", entry.code, got, entry.retryable)
		}
	}

	switchNames, err := gatewayInvocationErrorSwitchNames()
	if err != nil {
		t.Fatal(err)
	}
	if missing := gatewayErrorSetDifference(contractNames, switchNames); len(missing) != 0 {
		t.Errorf("契约镜像中的错误码未出现在 isGatewayInvocationErrorCode switch: %v", missing)
	}
	if extra := gatewayErrorSetDifference(switchNames, contractNames); len(extra) != 0 {
		t.Errorf("isGatewayInvocationErrorCode switch 中的错误码未出现在契约镜像: %v", extra)
	}

	const futureGatewayCode = "future_gateway_error_code"
	if isGatewayInvocationErrorCode(futureGatewayCode) {
		t.Fatalf("未登记的 Gateway 错误码 %q 被接受", futureGatewayCode)
	}
}

func gatewayInvocationErrorSwitchNames() (map[string]struct{}, error) {
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		return nil, fmt.Errorf("无法定位 gateway error 守卫测试文件")
	}
	sourcePath := filepath.Join(filepath.Dir(testFile), "gateway_errors.go")
	source, err := os.ReadFile(sourcePath)
	if err != nil {
		return nil, fmt.Errorf("读取 %s 失败: %w", sourcePath, err)
	}
	file, err := parser.ParseFile(token.NewFileSet(), sourcePath, source, 0)
	if err != nil {
		return nil, fmt.Errorf("解析 %s 失败: %w", sourcePath, err)
	}

	var target *ast.FuncDecl
	for _, declaration := range file.Decls {
		function, ok := declaration.(*ast.FuncDecl)
		if ok && function.Name.Name == "isGatewayInvocationErrorCode" {
			target = function
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("找不到 isGatewayInvocationErrorCode 函数")
	}

	var switchStatement *ast.SwitchStmt
	ast.Inspect(target.Body, func(node ast.Node) bool {
		if statement, ok := node.(*ast.SwitchStmt); ok {
			switchStatement = statement
			return false
		}
		return true
	})
	if switchStatement == nil {
		return nil, fmt.Errorf("isGatewayInvocationErrorCode 不包含 switch")
	}

	names := make(map[string]struct{})
	for _, statement := range switchStatement.Body.List {
		clause, ok := statement.(*ast.CaseClause)
		if !ok || len(clause.List) == 0 {
			continue
		}
		for _, expression := range clause.List {
			identifier, ok := expression.(*ast.Ident)
			if !ok {
				return nil, fmt.Errorf("isGatewayInvocationErrorCode switch 使用了非常量标识符 case %T", expression)
			}
			names[identifier.Name] = struct{}{}
		}
	}
	return names, nil
}

func gatewayErrorSetDifference(left, right map[string]struct{}) []string {
	var difference []string
	for value := range left {
		if _, ok := right[value]; !ok {
			difference = append(difference, value)
		}
	}
	sort.Strings(difference)
	return difference
}
