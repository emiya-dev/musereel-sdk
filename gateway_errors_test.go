package musereelsdk

import (
	"encoding/json"
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
//
// 四列各锚一跳，少一列就多一个「改错了也不红」的口子：
//   - constName —— 常量的**名**，与 AST 提取的两处 switch 的 case 标识符对拍；
//   - constant  —— 常量**本体**（编译期引用），与 code 对拍。没有这一列，
//     把两个同 retryable 组的常量值互换是**绿**的：标识符名没动、
//     两个码仍在白名单里、retryable 分组也没变，而调用方拿常量比 wire 会整个对调分支。
//   - code      —— wire 上的字面量；这一列同时与中枢导出产物做**集合**对拍。
//   - retryable —— 中枢产物今天不导出该列（口径见 SOURCE.txt 的 frozen_codes_*），
//     只能留在本仓；等中枢按登记加列后，这一维也可以换成产物驱动。
//
// 🔴 码名集合的事实源不是这张表，是 contract-input/frozen_public_error_codes.json
// （中枢 gateway 导出的产物副本，sha256 由 scripts/check-contract-pin.sh 钉住）。
// 这张表只负责「名 ↔ 值 ↔ 是否可重试」的配对，集合成员由产物裁定。
var frozenGatewayErrorContract = []struct {
	constName string
	constant  string
	code      string
	retryable bool
}{
	{constName: "GatewayInvalidInvocationRequest", constant: GatewayInvalidInvocationRequest, code: "invalid_invocation_request", retryable: false},
	{constName: "GatewayModerationInvalidRequest", constant: GatewayModerationInvalidRequest, code: "moderation_invalid_request", retryable: false},
	{constName: "GatewayRuntimeUnauthenticated", constant: GatewayRuntimeUnauthenticated, code: "runtime_unauthenticated", retryable: false},
	{constName: "GatewayActorAssertionInvalid", constant: GatewayActorAssertionInvalid, code: "actor_assertion_invalid", retryable: false},
	{constName: "GatewayActorAssertionReplayed", constant: GatewayActorAssertionReplayed, code: "actor_assertion_replayed", retryable: false},
	{constName: "GatewayRuntimeForbidden", constant: GatewayRuntimeForbidden, code: "runtime_forbidden", retryable: false},
	{constName: "GatewaySKUNotAllowed", constant: GatewaySKUNotAllowed, code: "sku_not_allowed", retryable: false},
	{constName: "GatewayComplianceRejected", constant: GatewayComplianceRejected, code: "compliance_rejected", retryable: false},
	{constName: "GatewayInvocationNotFound", constant: GatewayInvocationNotFound, code: "invocation_not_found", retryable: false},
	{constName: "GatewayInvocationArtifactNotFound", constant: GatewayInvocationArtifactNotFound, code: "invocation_artifact_not_found", retryable: false},
	{constName: "GatewayInvocationArtifactExpired", constant: GatewayInvocationArtifactExpired, code: "invocation_artifact_expired", retryable: false},
	{constName: "GatewayInvocationDeliveryModeMismatch", constant: GatewayInvocationDeliveryModeMismatch, code: "invocation_delivery_mode_mismatch", retryable: false},
	{constName: "GatewayInvocationIdempotencyConflict", constant: GatewayInvocationIdempotencyConflict, code: "invocation_idempotency_conflict", retryable: false},
	{constName: "GatewayInvocationTransitionConflict", constant: GatewayInvocationTransitionConflict, code: "invocation_transition_conflict", retryable: false},
	{constName: "GatewayInsufficientQuota", constant: GatewayInsufficientQuota, code: "insufficient_quota", retryable: false},
	{constName: "GatewayMemberLimitExceeded", constant: GatewayMemberLimitExceeded, code: "member_limit_exceeded", retryable: false},
	{constName: "GatewayRateLimited", constant: GatewayRateLimited, code: "rate_limited", retryable: true},
	{constName: "GatewayUpstreamUnavailable", constant: GatewayUpstreamUnavailable, code: "upstream_unavailable", retryable: true},
	{constName: "GatewayInternalError", constant: GatewayInternalError, code: "internal_error", retryable: true},
}

// frozenCodesArtifactRelativePath 指向中枢导出产物在本仓的副本。
// 中枢侧由 TestFrozenCodesExportMatchesArtifact 保证「改了源表就得重跑导出」，
// 本仓侧由 check-contract-pin.sh 的 sha256 保证「副本没被就地手改」——两边各守一半。
// ⚠ 两边都守不住的那一跳是「中枢导出了新产物，但没人把它同步进本仓」：
// 那要靠涟漪清单 + 新公共码上线用真实客户端读一次，不是这条测试能覆盖的。
const frozenCodesArtifactRelativePath = "contract-input/frozen_public_error_codes.json"

// frozenCodesArtifactSchemaVersion 是本仓当前读法所适用的产物版本。
// 版本变了必须停下来看导出口径改了什么，不能默默按旧读法解析。
const frozenCodesArtifactSchemaVersion = 1

type frozenCodesArtifact struct {
	SchemaVersion int      `json:"schema_version"`
	Codes         []string `json:"codes"`
}

func TestGatewayInvocationErrorCodesMatchFrozenContract(t *testing.T) {
	artifactCodes := loadFrozenCodesArtifact(t)

	contractNames := make(map[string]struct{}, len(frozenGatewayErrorContract))
	contractCodes := make(map[string]struct{}, len(frozenGatewayErrorContract))
	retryableNames := make(map[string]struct{})
	nonRetryableNames := make(map[string]struct{})
	for _, entry := range frozenGatewayErrorContract {
		if _, duplicate := contractNames[entry.constName]; duplicate {
			t.Fatalf("镜像表重复常量 %q", entry.constName)
		}
		if _, duplicate := contractCodes[entry.code]; duplicate {
			t.Fatalf("镜像表重复错误码 %q", entry.code)
		}
		contractNames[entry.constName] = struct{}{}
		contractCodes[entry.code] = struct{}{}
		if entry.retryable {
			retryableNames[entry.constName] = struct{}{}
		} else {
			nonRetryableNames[entry.constName] = struct{}{}
		}

		// 名 ↔ 值：把常量本体编译进来和字面量对拍。同 retryable 组互换常量值
		// 只有这一条能照出来。
		if entry.constant != entry.code {
			t.Errorf("常量 %s 的值 = %q，镜像表登记的是 %q", entry.constName, entry.constant, entry.code)
		}
		if !isGatewayInvocationErrorCode(entry.code) {
			t.Errorf("契约错误码 %q 未被 isGatewayInvocationErrorCode 接受", entry.code)
		}
		if got := RetryableGatewayCode(entry.code); got != entry.retryable {
			t.Errorf("RetryableGatewayCode(%q) = %t, want %t", entry.code, got, entry.retryable)
		}
	}

	// 集合成员以中枢产物为准，双向差集——只查一个方向的话，
	// 「产物收缩了而本仓没跟」会是绿的。
	if missing := gatewayErrorSetDifference(artifactCodes, contractCodes); len(missing) != 0 {
		t.Errorf("中枢产物里的公共错误码未出现在镜像表: %v；同步方式见 contract-input/SOURCE.txt 的 frozen_codes_*", missing)
	}
	if extra := gatewayErrorSetDifference(contractCodes, artifactCodes); len(extra) != 0 {
		t.Errorf("镜像表里的错误码不在中枢产物中: %v；本仓不得自造公共错误码", extra)
	}

	acceptNames, err := gatewayErrorSwitchClauses("isGatewayInvocationErrorCode")
	if err != nil {
		t.Fatal(err)
	}
	acceptedIdents := acceptNames.union()
	if missing := gatewayErrorSetDifference(contractNames, acceptedIdents); len(missing) != 0 {
		t.Errorf("契约镜像中的错误码未出现在 isGatewayInvocationErrorCode switch: %v", missing)
	}
	if extra := gatewayErrorSetDifference(acceptedIdents, contractNames); len(extra) != 0 {
		t.Errorf("isGatewayInvocationErrorCode switch 中的错误码未出现在契约镜像: %v", extra)
	}

	// RetryableGatewayCode 的 false 分支与 default 同义，删掉任一 case 都不改变行为
	// ⇒ 行为断言对它没有判别力，只能按 case 列表逐组对拍。
	retryClauses, err := gatewayErrorSwitchClauses("RetryableGatewayCode")
	if err != nil {
		t.Fatal(err)
	}
	if missing := gatewayErrorSetDifference(retryableNames, retryClauses.byResult("true")); len(missing) != 0 {
		t.Errorf("镜像表判定可重试的码未出现在 RetryableGatewayCode 的 true 分支: %v", missing)
	}
	if extra := gatewayErrorSetDifference(retryClauses.byResult("true"), retryableNames); len(extra) != 0 {
		t.Errorf("RetryableGatewayCode true 分支里的码在镜像表中不是可重试: %v", extra)
	}
	if missing := gatewayErrorSetDifference(nonRetryableNames, retryClauses.byResult("false")); len(missing) != 0 {
		t.Errorf("镜像表判定不可重试的码未显式列在 RetryableGatewayCode 的 false 分支: %v（靠 default 兜住等于没人守这一维）", missing)
	}
	if extra := gatewayErrorSetDifference(retryClauses.byResult("false"), nonRetryableNames); len(extra) != 0 {
		t.Errorf("RetryableGatewayCode false 分支里的码在镜像表中是可重试: %v", extra)
	}

	const futureGatewayCode = "future_gateway_error_code"
	if isGatewayInvocationErrorCode(futureGatewayCode) {
		t.Fatalf("未登记的 Gateway 错误码 %q 被接受", futureGatewayCode)
	}
	if RetryableGatewayCode(futureGatewayCode) {
		t.Fatalf("未登记的 Gateway 错误码 %q 被判为可重试", futureGatewayCode)
	}
}

// loadFrozenCodesArtifact 读中枢导出产物副本。产物自身可疑时一律 Fatal——
// 一份读不出码的产物会让上面的双向差集在**空集**上恒真，那比没有这条守卫更糟。
func loadFrozenCodesArtifact(t *testing.T) map[string]struct{} {
	t.Helper()
	_, testFile, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("无法定位 gateway error 守卫测试文件")
	}
	artifactPath := filepath.Join(filepath.Dir(testFile), frozenCodesArtifactRelativePath)
	raw, err := os.ReadFile(artifactPath)
	if err != nil {
		t.Fatalf("读取中枢冻结错误码产物失败：%v；该文件是中枢 backend/service/gateway/frozen_public_error_codes.json 的副本，缺了不能跳过", err)
	}
	var artifact frozenCodesArtifact
	if err := json.Unmarshal(raw, &artifact); err != nil {
		t.Fatalf("解析 %s 失败：%v", artifactPath, err)
	}
	if artifact.SchemaVersion != frozenCodesArtifactSchemaVersion {
		t.Fatalf("产物 schema_version=%d，本仓读法适用的是 %d；导出口径变了要先对齐读法，不能按旧读法解析", artifact.SchemaVersion, frozenCodesArtifactSchemaVersion)
	}
	if len(artifact.Codes) == 0 {
		t.Fatalf("产物 %s 的 codes 为空——空集会让下面的差集恒真", artifactPath)
	}

	codes := make(map[string]struct{}, len(artifact.Codes))
	for index, code := range artifact.Codes {
		if code == "" {
			t.Fatalf("产物 codes[%d] 为空串", index)
		}
		if index > 0 && artifact.Codes[index-1] >= code {
			t.Fatalf("产物 codes 必须严格升序且无重复，但 codes[%d]=%q 不大于 codes[%d]=%q", index, code, index-1, artifact.Codes[index-1])
		}
		codes[code] = struct{}{}
	}
	return codes
}

// gatewayErrorSwitchClauses 是 switch 的 case 分组：按该分支 `return` 的字面量归类。
type gatewayErrorSwitchGroups map[string]map[string]struct{}

func (clauses gatewayErrorSwitchGroups) byResult(result string) map[string]struct{} {
	if idents, ok := clauses[result]; ok {
		return idents
	}
	return map[string]struct{}{}
}

func (clauses gatewayErrorSwitchGroups) union() map[string]struct{} {
	all := make(map[string]struct{})
	for _, idents := range clauses {
		for ident := range idents {
			all[ident] = struct{}{}
		}
	}
	return all
}

func gatewayErrorSwitchClauses(functionName string) (gatewayErrorSwitchGroups, error) {
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
		if ok && function.Name.Name == functionName {
			target = function
			break
		}
	}
	if target == nil {
		return nil, fmt.Errorf("找不到 %s 函数", functionName)
	}

	var switchStatement *ast.SwitchStmt
	ast.Inspect(target.Body, func(node ast.Node) bool {
		if switchStatement != nil {
			return false
		}
		if statement, ok := node.(*ast.SwitchStmt); ok {
			switchStatement = statement
			return false
		}
		return true
	})
	if switchStatement == nil {
		return nil, fmt.Errorf("%s 不包含 switch", functionName)
	}

	clauses := gatewayErrorSwitchGroups{}
	for _, statement := range switchStatement.Body.List {
		clause, ok := statement.(*ast.CaseClause)
		if !ok || len(clause.List) == 0 {
			continue
		}
		result, err := gatewayErrorClauseResult(functionName, clause)
		if err != nil {
			return nil, err
		}
		if clauses[result] == nil {
			clauses[result] = map[string]struct{}{}
		}
		for _, expression := range clause.List {
			identifier, ok := expression.(*ast.Ident)
			if !ok {
				return nil, fmt.Errorf("%s switch 使用了非常量标识符 case %T", functionName, expression)
			}
			clauses[result][identifier.Name] = struct{}{}
		}
	}
	return clauses, nil
}

// gatewayErrorClauseResult 取该 case 分支 return 的字面量；分支不是「单条 return 字面量」
// 就直接报错——那说明函数形状变了，此时按旧读法归类会给出安静的错答案。
func gatewayErrorClauseResult(functionName string, clause *ast.CaseClause) (string, error) {
	if len(clause.Body) != 1 {
		return "", fmt.Errorf("%s 的 case 分支不是单条语句，读法需同步更新", functionName)
	}
	returnStatement, ok := clause.Body[0].(*ast.ReturnStmt)
	if !ok || len(returnStatement.Results) != 1 {
		return "", fmt.Errorf("%s 的 case 分支不是单值 return，读法需同步更新", functionName)
	}
	identifier, ok := returnStatement.Results[0].(*ast.Ident)
	if !ok {
		return "", fmt.Errorf("%s 的 case 分支 return 的不是标识符，读法需同步更新", functionName)
	}
	return identifier.Name, nil
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
