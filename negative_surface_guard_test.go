package musereelsdk

import (
	"fmt"
	"go/ast"
	"go/parser"
	"go/token"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strconv"
	"strings"
	"testing"
)

// negativeSurfaceCategory 是冻结的“绝不封装”负面清单分类。
type negativeSurfaceCategory string

const (
	negativeLedgerMutation negativeSurfaceCategory = "reserve/settle/grant/balance mutation"
	negativeCostPlane      negativeSurfaceCategory = "vendor/supplier/cost/contract/margin"
	negativeFreeFlag       negativeSurfaceCategory = "free/complimentary request flag"
	negativeScopeOverride  negativeSurfaceCategory = "tenant/instance/deployment scope override"
	negativePayment        negativeSurfaceCategory = "payment determination"
	negativeUpstreamAuth   negativeSurfaceCategory = "upstream key/credential configuration"
)

// negativeSurfaceHit 记录一个不允许出现在 SDK 表面或调用面的命中。
type negativeSurfaceHit struct {
	path     string
	line     int
	name     string
	owner    string
	kind     string
	category negativeSurfaceCategory
}

// negativeSurfaceObservation 是扫描器内部的统一观察形状。
type negativeSurfaceObservation struct {
	path  string
	pos   token.Pos
	name  string
	owner string
	kind  string
}

// negativeSurfaceAllow 是唯一的豁免入口；注释不会改变扫描结果。
// key 使用“相对文件::所有者::名称”，没有 owner 的 literal 使用
// “相对文件::literal:<值>”。每一项都必须说明它为何只是协议事实或传输
// 细节，而不是 SDK 提供的被禁止能力。
var negativeSurfaceAllow = map[string]string{
	"assertion.go::AssertionInput::InstanceID":                                               "实例 token 绑定的 assertion 输入，不是调用方请求 scope",
	"assertion.go::AssertionInput::TenantID":                                                 "实例 token 绑定的 assertion 输入，不是调用方请求 scope",
	"assertion.go::AssertionClaims::TenantID":                                                "服务端签发 assertion 的返回声明，不是调用方请求 scope",
	"gateway.go::GatewayIdentity::InstanceID":                                                "实例 token 绑定的 Gateway assertion 上下文",
	"gateway.go::GatewayIdentity::TenantID":                                                  "实例 token 绑定的 Gateway assertion 上下文",
	"gateway.go::GatewayInvocationSnapshot::ReservedUnits":                                   "服务器返回的 invocation snapshot 事实，不是 reserve 操作",
	"gateway.go::GatewayInvocationSnapshot::SettledUnits":                                    "服务器返回的 invocation snapshot 事实，不是 settle 操作",
	"gateway.go::GatewayStateSettlementShortfall":                                            "服务器返回的封闭状态事实，不是 settle 操作",
	"gateway_errors.go::GatewayUpstreamUnavailable":                                          "冻结的 gateway 稳定错误码，不是上游 key 配置面",
	"mtls.go::NewMTLSCredentials":                                                            "mTLS transport credentials，不是上游 credential 配置",
	"runtime_client.go::RuntimeAssertionConfig::InstanceID":                                  "实例 token 绑定的 runtime assertion 上下文",
	"runtime_client.go::RuntimeAssertionConfig::TenantID":                                    "实例 token 绑定的 runtime assertion 上下文",
	"runtime_client.go::WithRuntimeAssertion::instanceID":                                    "实例 token 绑定的 runtime assertion 上下文",
	"runtime_client.go::WithRuntimeAssertion::tenantID":                                      "实例 token 绑定的 runtime assertion 上下文",
	"runtime_client.go::VerifyAndConfirmPayment":                                             "仅原样转交冻结的支付原始证明 RPC",
	"runtime_client.go::GetBalance":                                                          "只读余额查询 RPC，不是余额 mutation",
	"runtime_client.go::ListLedger":                                                          "只读额度流水查询 RPC，不是余额 mutation",
	"path.go::literal:VerifyAndConfirmPayment":                                               "冻结的 gRPC RPC 名称，SDK 只透传该 RPC",
	"path.go::literal:GetBalance":                                                            "冻结的只读 gRPC RPC 名称",
	"path.go::literal:balance:get":                                                           "冻结的只读 assertion operation",
	"assertion.go::literal:tenant_id":                                                        "冻结 assertion claims 字段名",
	"runtime/runtime.pb.go::RegistrationGrantUnits":                                          "冻结 protobuf 返回事实字段",
	"runtime/runtime.pb.go::RegistrationGrant":                                               "冻结 protobuf 返回事实字段",
	"runtime/runtime.pb.go::GrantId":                                                         "冻结 protobuf 返回事实字段",
	"runtime/runtime.pb.go::GrantedUnits":                                                    "冻结 protobuf 返回事实字段",
	"runtime/runtime.pb.go::GetGrantId":                                                      "冻结 protobuf 返回事实 getter",
	"runtime/runtime.pb.go::GetGrantedUnits":                                                 "冻结 protobuf 返回事实 getter",
	"runtime/runtime.pb.go::GetRegistrationGrantUnits":                                       "冻结 protobuf 返回事实 getter",
	"runtime/runtime.pb.go::GetRegistrationGrant":                                            "冻结 protobuf 返回事实 getter",
	"runtime/runtime.pb.go::VerifyAndConfirmPaymentRequest":                                  "冻结 protobuf 支付证明输入，仅由公开 RPC 原样转交",
	"runtime/runtime.pb.go::VerifyAndConfirmPaymentReply":                                    "冻结 protobuf RPC 返回事实",
	"runtime/runtime.pb.go::GetIdempotencyKey":                                               "冻结 protobuf getter，名称来自支付证明协议",
	"runtime/runtime.pb.go::RetailGrant":                                                     "冻结 protobuf 返回事实类型",
	"runtime/runtime.pb.go::GetGrant":                                                        "冻结 protobuf 返回事实 getter",
	"runtime/runtime.pb.go::Grant":                                                           "冻结 protobuf 返回事实字段",
	"runtime/runtime.pb.go::GrantPlan":                                                       "冻结 protobuf 返回事实类型",
	"runtime/runtime.pb.go::GetGrantPlan":                                                    "冻结 protobuf 返回事实 getter",
	"runtime/runtime.pb.go::PaidAmount":                                                      "冻结订单返回事实，不是支付判定方法",
	"runtime/runtime.pb.go::PaidAtMs":                                                        "冻结订单返回事实，不是支付判定方法",
	"runtime/runtime.pb.go::PaymentChannel":                                                  "冻结订单返回事实，不是支付判定方法",
	"runtime/runtime.pb.go::PaymentTransactionId":                                            "冻结订单返回事实，不是支付判定方法",
	"runtime/runtime.pb.go::BalanceReply::PaidAvailableUnits":                                "冻结余额查询返回拆分，不是支付判定方法",
	"runtime/runtime.pb.go::PaidAvailableUnits":                                              "冻结余额查询返回拆分，不是支付判定方法",
	"runtime/runtime.pb.go::GetPaidAmount":                                                   "冻结订单返回事实 getter",
	"runtime/runtime.pb.go::GetPaidAtMs":                                                     "冻结订单返回事实 getter",
	"runtime/runtime.pb.go::GetPaidAvailableUnits":                                           "冻结余额查询返回拆分 getter",
	"runtime/runtime.pb.go::GetPaymentChannel":                                               "冻结订单返回事实 getter",
	"runtime/runtime.pb.go::GetPaymentTransactionId":                                         "冻结订单返回事实 getter",
	"runtime/runtime.pb.go::FreeAvailableUnits":                                              "冻结余额查询返回拆分，不是请求侧免费标志",
	"runtime/runtime.pb.go::GetFreeAvailableUnits":                                           "冻结余额查询返回拆分 getter",
	"runtime/runtime.pb.go::BalanceReply":                                                    "冻结只读余额查询返回类型",
	"runtime/runtime.pb.go::GetBalance":                                                      "冻结只读余额查询 getter",
	"runtime/runtime.pb.go::BalanceClass":                                                    "冻结流水返回分类事实",
	"runtime/runtime.pb.go::GetBalanceClass":                                                 "冻结流水返回分类 getter",
	"runtime/runtime.pb.go::CredentialRef":                                                   "实名证明引用，不是上游 credential 配置",
	"runtime/runtime.pb.go::GetCredentialRef":                                                "实名证明引用 getter，不是上游 credential 配置",
	"runtime/runtime.pb.go::QualityTier::VendorModel":                                        "冻结 protobuf 质量档位事实字段",
	"runtime/runtime.pb.go::GetVendorModel":                                                  "冻结 protobuf 质量档位事实 getter",
	"runtime/runtime_grpc.pb.go::RuntimeService_VerifyAndConfirmPayment_FullMethodName":      "冻结 protobuf RPC 路径常量",
	"runtime/runtime_grpc.pb.go::RuntimeService_GetBalance_FullMethodName":                   "冻结只读 protobuf RPC 路径常量",
	"runtime/runtime_grpc.pb.go::RuntimeService_VerifyAndConfirmPayment":                     "冻结 protobuf RPC 方法，仅由 SDK 透传",
	"runtime/runtime_grpc.pb.go::RuntimeService_GetBalance":                                  "冻结只读 protobuf RPC 方法",
	"runtime/runtime_grpc.pb.go::_RuntimeService_VerifyAndConfirmPayment_Handler":            "生成的 protobuf RPC handler",
	"runtime/runtime_grpc.pb.go::_RuntimeService_GetBalance_Handler":                         "生成的只读 protobuf RPC handler",
	"runtime/runtime_grpc.pb.go::VerifyAndConfirmPayment":                                    "生成的 protobuf RPC 方法",
	"runtime/runtime_grpc.pb.go::GetBalance":                                                 "生成的只读 protobuf RPC 方法",
	"runtime/runtime_grpc.pb.go::literal:/runtime.v1.RuntimeService/VerifyAndConfirmPayment": "冻结 protobuf RPC 路径 literal",
	"runtime/runtime_grpc.pb.go::literal:/runtime.v1.RuntimeService/GetBalance":              "冻结只读 protobuf RPC 路径 literal",
	"runtime/runtime.pb.go::literal:/runtime.v1.RuntimeService/VerifyAndConfirmPayment":      "冻结 protobuf RPC 路径 literal",
	"runtime/runtime.pb.go::literal:/runtime.v1.RuntimeService/GetBalance":                   "冻结只读 protobuf RPC 路径 literal",
}

// negativeSurfaceLedgerWords 是账本相关名词的第一段词表。
var negativeSurfaceLedgerWords = []string{
	"balance", "ledger", "unit", "quota", "credit", "debit",
}

// negativeSurfaceMutationWords 是账本 mutation 的第二段词表。
var negativeSurfaceMutationWords = []string{
	"set", "write", "update", "adjust", "mutat", "credit", "debit",
	"increment", "decrement", "apply", "consume", "deduct", "add", "remove",
}

// scanSDKNonTestGo 扫描仓库中所有非测试 Go 文件。
func scanSDKNonTestGo(root string) ([]negativeSurfaceHit, error) {
	var paths []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return filepath.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		paths = append(paths, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(paths)

	var hits []negativeSurfaceHit
	for _, path := range paths {
		fileHits, err := scanSDKGoFile(path, root)
		if err != nil {
			return nil, err
		}
		hits = append(hits, fileHits...)
	}
	return hits, nil
}

// scanSDKGoFile 扫描导出面、公开方法参数和 HTTP/gRPC 调用面。
func scanSDKGoFile(path, root string) ([]negativeSurfaceHit, error) {
	source, err := os.ReadFile(path)
	if err != nil {
		return nil, err
	}
	fset := token.NewFileSet()
	file, err := parser.ParseFile(fset, path, source, parser.ParseComments)
	if err != nil {
		return nil, err
	}
	relativePath, err := filepath.Rel(root, path)
	if err != nil {
		return nil, err
	}
	relativePath = filepath.ToSlash(relativePath)

	var hits []negativeSurfaceHit
	observe := func(observation negativeSurfaceObservation) {
		if observation.name == "" || observation.name == "_" {
			return
		}
		if negativeSurfaceAllowed(relativePath, observation) {
			return
		}
		category, ok := classifyNegativeSurface(observation.name, observation.kind)
		if !ok {
			return
		}
		position := fset.Position(observation.pos)
		hits = append(hits, negativeSurfaceHit{
			path:     relativePath,
			line:     position.Line,
			name:     observation.name,
			owner:    observation.owner,
			kind:     observation.kind,
			category: category,
		})
	}

	for _, declaration := range file.Decls {
		switch declaration := declaration.(type) {
		case *ast.GenDecl:
			for _, specification := range declaration.Specs {
				switch specification := specification.(type) {
				case *ast.TypeSpec:
					if ast.IsExported(specification.Name.Name) {
						observe(negativeSurfaceObservation{path: path, pos: specification.Name.Pos(), name: specification.Name.Name, kind: "exported type"})
					}
					scanExportedFields(specification.Name.Name, specification.Type, observe)
				case *ast.ValueSpec:
					for index, name := range specification.Names {
						if ast.IsExported(name.Name) {
							observe(negativeSurfaceObservation{path: path, pos: name.Pos(), name: name.Name, kind: "exported value"})
						}
						if index < len(specification.Values) && looksLikeCallSurfaceName(name.Name) {
							observeStringLiteral(specification.Values[index], path, observe, "registered call-surface value")
						}
					}
				}
			}
		case *ast.FuncDecl:
			if !ast.IsExported(declaration.Name.Name) {
				continue
			}
			observe(negativeSurfaceObservation{path: path, pos: declaration.Name.Pos(), name: declaration.Name.Name, kind: "exported function or method"})
			scanPublicParameters(declaration.Name.Name, declaration.Type, observe)
			scanPublicBalanceMutations(declaration.Body, observe)
		}
	}

	// 这里不以注释作豁免；只识别调用表达式和形似 HTTP/gRPC 路径的字符串。
	ast.Inspect(file, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.CallExpr:
			selector, ok := node.Fun.(*ast.SelectorExpr)
			if !ok {
				return true
			}
			switch selector.Sel.Name {
			case "Invoke":
				if len(node.Args) > 1 {
					observeStringLiteral(node.Args[1], path, observe, "gRPC method call")
				}
			case "NewRequest":
				if len(node.Args) > 1 {
					observeStringLiteral(node.Args[1], path, observe, "HTTP route call")
				}
			case "NewRequestWithContext":
				if len(node.Args) > 2 {
					observeStringLiteral(node.Args[2], path, observe, "HTTP route call")
				}
			}
		case *ast.BasicLit:
			if node.Kind != token.STRING {
				return true
			}
			value, unquoteErr := strconv.Unquote(node.Value)
			if unquoteErr == nil && (strings.Contains(value, "/runtime") || strings.Contains(value, "/RuntimeService/")) {
				observe(negativeSurfaceObservation{path: path, pos: node.Pos(), name: value, kind: "HTTP/gRPC route literal"})
			}
		case *ast.KeyValueExpr:
			// map 的字符串 key 也是调用面注册表的一部分，例如 runtimeMethods。
			observeStringLiteral(node.Key, path, observe, "registered call-surface key")
			observeStringLiteral(node.Value, path, observe, "registered call-surface value")
		}
		return true
	})

	return hits, nil
}

// looksLikeCallSurfaceName 限定常量扫描范围，避免把任意业务文案当作调用面。
func looksLikeCallSurfaceName(name string) bool {
	lower := strings.ToLower(name)
	return strings.Contains(lower, "path") || strings.Contains(lower, "route") ||
		strings.Contains(lower, "method") || strings.Contains(lower, "operation") ||
		strings.Contains(lower, "endpoint") || strings.Contains(lower, "url") ||
		strings.Contains(lower, "rpc")
}

// scanExportedFields 扫描导出 struct/interface 的字段和方法名。
func scanExportedFields(owner string, expression ast.Expr, observe func(negativeSurfaceObservation)) {
	switch expression := expression.(type) {
	case *ast.StructType:
		for _, field := range expression.Fields.List {
			for _, name := range field.Names {
				if ast.IsExported(name.Name) {
					observe(negativeSurfaceObservation{pos: name.Pos(), name: name.Name, owner: owner, kind: "exported field"})
				}
			}
		}
	case *ast.InterfaceType:
		for _, field := range expression.Methods.List {
			for _, name := range field.Names {
				if ast.IsExported(name.Name) {
					observe(negativeSurfaceObservation{pos: name.Pos(), name: name.Name, owner: owner, kind: "exported interface method"})
				}
			}
		}
	}
}

// scanPublicParameters 扫描公开函数或方法的所有参数名，即使参数本身是小写。
func scanPublicParameters(owner string, functionType *ast.FuncType, observe func(negativeSurfaceObservation)) {
	if functionType == nil || functionType.Params == nil {
		return
	}
	for _, field := range functionType.Params.List {
		for _, name := range field.Names {
			observe(negativeSurfaceObservation{pos: name.Pos(), name: name.Name, owner: owner, kind: "public parameter"})
		}
	}
}

// scanPublicBalanceMutations 识别公开方法体中对 balance 目标的直接赋值或自增。
func scanPublicBalanceMutations(body *ast.BlockStmt, observe func(negativeSurfaceObservation)) {
	if body == nil {
		return
	}
	ast.Inspect(body, func(node ast.Node) bool {
		switch node := node.(type) {
		case *ast.AssignStmt:
			for _, expression := range node.Lhs {
				observeBalanceMutationTarget(expression, observe)
			}
		case *ast.IncDecStmt:
			observeBalanceMutationTarget(node.X, observe)
		}
		return true
	})
}

// observeBalanceMutationTarget 只把赋值左侧识别为 mutation，避免把局部只读变量误判。
func observeBalanceMutationTarget(expression ast.Expr, observe func(negativeSurfaceObservation)) {
	switch expression := expression.(type) {
	case *ast.Ident:
		if strings.Contains(strings.ToLower(expression.Name), "balance") {
			observe(negativeSurfaceObservation{pos: expression.Pos(), name: expression.Name, kind: "direct balance mutation"})
		}
	case *ast.SelectorExpr:
		if strings.Contains(strings.ToLower(expression.Sel.Name), "balance") {
			observe(negativeSurfaceObservation{pos: expression.Sel.Pos(), name: expression.Sel.Name, kind: "direct balance mutation"})
		}
	}
}

// observeStringLiteral 把调用面中的字符串常量交给同一套词表。
func observeStringLiteral(expression ast.Expr, path string, observe func(negativeSurfaceObservation), kind string) {
	literal, ok := expression.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return
	}
	value, err := strconv.Unquote(literal.Value)
	if err != nil {
		return
	}
	observe(negativeSurfaceObservation{path: path, pos: literal.Pos(), name: value, kind: kind})
}

// negativeSurfaceAllowed 只查显式 map；doc comment 和附近说明不参与判断。
func negativeSurfaceAllowed(relativePath string, observation negativeSurfaceObservation) bool {
	if observation.name == "" {
		return true
	}
	if observation.kind == "HTTP/gRPC route literal" || strings.Contains(observation.kind, "call") || observation.kind == "registered call-surface key" {
		if _, ok := negativeSurfaceAllow[relativePath+"::literal:"+observation.name]; ok {
			return true
		}
	}
	if observation.owner != "" {
		if _, ok := negativeSurfaceAllow[relativePath+"::"+observation.owner+"::"+observation.name]; ok {
			return true
		}
	}
	_, ok := negativeSurfaceAllow[relativePath+"::"+observation.name]
	return ok
}

// classifyNegativeSurface 使用大小写不敏感的冻结词表识别能力边界。
func classifyNegativeSurface(name, kind string) (negativeSurfaceCategory, bool) {
	lower := strings.ToLower(name)

	if strings.Contains(lower, "vendor") || strings.Contains(lower, "supplier") ||
		strings.Contains(lower, "cost") || strings.Contains(lower, "contract") || strings.Contains(lower, "margin") {
		return negativeCostPlane, true
	}
	if strings.Contains(lower, "reserve") || strings.Contains(lower, "settle") || strings.Contains(lower, "grant") ||
		(containsAny(lower, negativeSurfaceLedgerWords) && containsAny(lower, negativeSurfaceMutationWords)) {
		return negativeLedgerMutation, true
	}
	if strings.Contains(lower, "free") || strings.Contains(lower, "complimentary") {
		return negativeFreeFlag, true
	}
	if strings.Contains(lower, "tenant") || strings.Contains(lower, "instance") || strings.Contains(lower, "deployment") {
		return negativeScopeOverride, true
	}
	if strings.Contains(lower, "payment") || strings.Contains(lower, "paid") ||
		strings.Contains(lower, "markpaid") || strings.Contains(lower, "setpaid") ||
		strings.Contains(lower, "paymentstatus") || strings.Contains(lower, "paymentdecision") {
		return negativePayment, true
	}
	if strings.Contains(lower, "upstream") || strings.Contains(lower, "credential") {
		return negativeUpstreamAuth, true
	}
	_ = kind
	return "", false
}

// containsAny 返回 text 是否包含任一 mutation 词根。
func containsAny(text string, words []string) bool {
	for _, word := range words {
		if strings.Contains(text, word) {
			return true
		}
	}
	return false
}

func TestNegativeSurfaceGuardMatchesCurrentRepository(t *testing.T) {
	_, file, _, ok := runtime.Caller(0)
	if !ok {
		t.Fatal("无法定位守卫测试文件")
	}
	root := filepath.Dir(file)
	hits, err := scanSDKNonTestGo(root)
	if err != nil {
		t.Fatalf("扫描非测试 Go 文件失败: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("当前 SDK 表面命中绝不封装词表: %s", formatNegativeSurfaceHits(hits))
	}
}

func TestNegativeSurfaceGuardSixTemporaryNegativeControls(t *testing.T) {
	cases := []struct {
		name     string
		category negativeSurfaceCategory
		source   string
	}{
		{
			name:     "ledger mutation",
			category: negativeLedgerMutation,
			source:   "package sample\n// 这段注释不能成为豁免。\ntype Client struct{}\nfunc (Client) ReserveSettleGrantBalanceMutation() {}\nfunc (Client) ReadThenWrite(other *Client) { other.balance = 1 }\n",
		},
		{
			name:     "cost plane",
			category: negativeCostPlane,
			source:   "package sample\ntype VendorCostContractMargin struct{}\n",
		},
		{
			name:     "free flag",
			category: negativeFreeFlag,
			source:   "package sample\ntype CreateRequest struct { Free bool }\n",
		},
		{
			name:     "scope override",
			category: negativeScopeOverride,
			source:   "package sample\ntype Client struct{}\nfunc (Client) Invoke(tenantID string) {}\n",
		},
		{
			name:     "payment determination",
			category: negativePayment,
			source:   "package sample\ntype Client struct{}\nfunc (Client) MarkPaid() {}\n",
		},
		{
			name:     "upstream credential",
			category: negativeUpstreamAuth,
			source:   "package sample\ntype UpstreamCredentialConfig struct{}\n",
		},
	}

	for _, testCase := range cases {
		t.Run(testCase.name, func(t *testing.T) {
			directory := t.TempDir()
			path := filepath.Join(directory, "synthetic_violation.go")
			if err := os.WriteFile(path, []byte(testCase.source), 0o600); err != nil {
				t.Fatalf("写入临时违规样本失败: %v", err)
			}
			redHits, err := scanSDKNonTestGo(directory)
			if err != nil {
				t.Fatalf("扫描临时违规样本失败: %v", err)
			}
			if !hasNegativeSurfaceCategory(redHits, testCase.category) {
				t.Fatalf("守卫未将样本判红: %s", formatNegativeSurfaceHits(redHits))
			}
			t.Logf("RED_CONTROL category=%s hits=%s", testCase.category, formatNegativeSurfaceHits(redHits))

			if err := os.Remove(path); err != nil {
				t.Fatalf("移除临时违规样本失败: %v", err)
			}
			greenHits, err := scanSDKNonTestGo(directory)
			if err != nil {
				t.Fatalf("移除样本后扫描失败: %v", err)
			}
			if len(greenHits) != 0 {
				t.Fatalf("移除样本后守卫仍未恢复绿: %s", formatNegativeSurfaceHits(greenHits))
			}
			t.Logf("GREEN_AFTER_REMOVE category=%s hits=0", testCase.category)
		})
	}
}

func TestNegativeSurfaceGuardLedgerNounMutationControls(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "synthetic_ledger_surface.go")
	source := `package sample
type Client struct{}
func (Client) DeductUnits() {}
type LedgerWriter struct { UpdateQuota bool }
func (Client) AddHeader() {}
func (Client) SetTimeout() {}
`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatalf("写入临时账本样本失败: %v", err)
	}

	redHits, err := scanSDKNonTestGo(directory)
	if err != nil {
		t.Fatalf("扫描临时账本样本失败: %v", err)
	}
	for _, name := range []string{"DeductUnits", "LedgerWriter", "UpdateQuota"} {
		if !hasNegativeSurfaceName(redHits, name) {
			t.Fatalf("账本 mutation 样本 %s 未判红: %s", name, formatNegativeSurfaceHits(redHits))
		}
	}
	for _, name := range []string{"AddHeader", "SetTimeout"} {
		if hasNegativeSurfaceName(redHits, name) {
			t.Fatalf("非账本样本 %s 被误杀: %s", name, formatNegativeSurfaceHits(redHits))
		}
	}
	t.Logf("LEDGER_MUTATION_RED_CONTROL hits=%s", formatNegativeSurfaceHits(redHits))
	t.Log("NON_LEDGER_GREEN_CONTROL names=AddHeader,SetTimeout hits=0")

	if err := os.Remove(path); err != nil {
		t.Fatalf("移除临时账本样本失败: %v", err)
	}
	greenHits, err := scanSDKNonTestGo(directory)
	if err != nil {
		t.Fatalf("移除账本样本后扫描失败: %v", err)
	}
	if len(greenHits) != 0 {
		t.Fatalf("移除账本样本后守卫仍未恢复绿: %s", formatNegativeSurfaceHits(greenHits))
	}
	t.Log("LEDGER_MUTATION_GREEN_AFTER_REMOVE hits=0")
}

func TestNegativeSurfaceGuardCallSurfaceNegativeControls(t *testing.T) {
	directory := t.TempDir()
	path := filepath.Join(directory, "synthetic_call_surface.go")
	source := `package sample
import "net/http"
func grpcCall(conn interface{ Invoke(any, string, any, any) }) {
	conn.Invoke(nil, "/runtime.v1.RuntimeService/MarkPaid", nil, nil)
}
func httpCall() {
	_, _ = http.NewRequest("POST", "/runtime/v1/settle", nil)
}
`
	if err := os.WriteFile(path, []byte(source), 0o600); err != nil {
		t.Fatalf("写入调用面违规样本失败: %v", err)
	}
	hits, err := scanSDKNonTestGo(directory)
	if err != nil {
		t.Fatalf("扫描调用面违规样本失败: %v", err)
	}
	if !hasNegativeSurfaceCategory(hits, negativePayment) || !hasNegativeSurfaceCategory(hits, negativeLedgerMutation) {
		t.Fatalf("HTTP/gRPC 调用面未判红: %s", formatNegativeSurfaceHits(hits))
	}
	t.Logf("CALL_SURFACE_RED_CONTROL hits=%s", formatNegativeSurfaceHits(hits))
	if err := os.Remove(path); err != nil {
		t.Fatalf("移除调用面违规样本失败: %v", err)
	}
	hits, err = scanSDKNonTestGo(directory)
	if err != nil {
		t.Fatalf("移除调用面样本后扫描失败: %v", err)
	}
	if len(hits) != 0 {
		t.Fatalf("移除调用面样本后未恢复绿: %s", formatNegativeSurfaceHits(hits))
	}
	t.Log("CALL_SURFACE_GREEN_AFTER_REMOVE hits=0")
}

func hasNegativeSurfaceCategory(hits []negativeSurfaceHit, category negativeSurfaceCategory) bool {
	for _, hit := range hits {
		if hit.category == category {
			return true
		}
	}
	return false
}

func hasNegativeSurfaceName(hits []negativeSurfaceHit, name string) bool {
	for _, hit := range hits {
		if hit.name == name {
			return true
		}
	}
	return false
}

func formatNegativeSurfaceHits(hits []negativeSurfaceHit) string {
	if len(hits) == 0 {
		return "none"
	}
	parts := make([]string, 0, len(hits))
	for _, hit := range hits {
		owner := hit.owner
		if owner != "" {
			owner += "."
		}
		parts = append(parts, fmt.Sprintf("%s:%d %s%s [%s]", hit.path, hit.line, owner, hit.name, hit.category))
	}
	sort.Strings(parts)
	return strings.Join(parts, "; ")
}
