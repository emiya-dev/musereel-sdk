package musereelsdk

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"strconv"
	"strings"
	"testing"
	"time"

	"google.golang.org/grpc"
	"google.golang.org/grpc/metadata"
)

// SDK-008：SDK 自己挂 Bearer 之前必须确认调用方没先挂一个。
//
// AppendToOutgoingContext 是**追加不是替换**，两个 authorization 会让中枢的
// 认证拦截器判成「没有 Bearer」（它要求恰好一个值），于是服务端回的是
// 「未认证」或「注册请求无效」——两种都不指向真正的原因。
// 实测代价见 SDK-007 落地那次：彩排 golden 六条腿全红，其中三条与改动无关。

type recordingConn struct {
	calls int
}

func (conn *recordingConn) Invoke(context.Context, string, any, any, ...grpc.CallOption) error {
	conn.calls++
	return nil
}

func (conn *recordingConn) NewStream(context.Context, *grpc.StreamDesc, string, ...grpc.CallOption) (grpc.ClientStream, error) {
	return nil, nil
}

func authorizationGuardClient() (*AuthenticatedClient, *recordingConn) {
	conn := &recordingConn{}
	tokens := NewCachedTokenSource(func(context.Context) (Token, error) {
		return NewToken("guard-token", "Bearer", time.Now().Add(5*time.Minute))
	})
	return NewAuthenticatedClient(conn, tokens), conn
}

func TestInvokeRejectsCallerSuppliedAuthorization(t *testing.T) {
	client, conn := authorizationGuardClient()
	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer caller-token")

	err := client.Invoke(ctx, "/runtime.v1.RuntimeService/GetBalance", nil, nil)
	if err == nil {
		t.Fatal("调用方已挂 authorization 时必须本地 fail-fast，不能发出一个带两个头的请求")
	}
	if !strings.Contains(err.Error(), "already set on the outgoing context") {
		t.Fatalf("报错必须指向真正的原因（头重复），实际：%v", err)
	}
	// 关键：**请求一次都不许发出去**。发出去了服务端只会给一个含糊的未认证错误。
	if conn.calls != 0 {
		t.Fatalf("守卫必须在发请求之前拦住，实际发了 %d 次", conn.calls)
	}
}

func TestInvokeWithAssertionRejectsCallerSuppliedAuthorization(t *testing.T) {
	client, conn := authorizationGuardClient()
	ctx := metadata.AppendToOutgoingContext(context.Background(), "authorization", "Bearer caller-token")

	signed := false
	err := client.InvokeWithAssertion(ctx, "/runtime.v1.RuntimeService/CreateOrder", AssertionCall{
		Args:           struct{}{},
		Sign:           func(Token) (JWS, error) { signed = true; return JWS{}, nil },
		ApplyAssertion: func(any, JWS) error { return nil },
	}, nil)
	if err == nil {
		t.Fatal("assertion 路径同样必须 fail-fast")
	}
	if !strings.Contains(err.Error(), "already set on the outgoing context") {
		t.Fatalf("报错必须指向真正的原因（头重复），实际：%v", err)
	}
	// 守卫要排在**签名之前**：assertion 带 nonce，白签一次会平白消耗一个 nonce。
	if signed {
		t.Fatal("守卫必须排在 Sign 之前，不得为一个注定失败的请求签名")
	}
	if conn.calls != 0 {
		t.Fatalf("守卫必须在发请求之前拦住，实际发了 %d 次", conn.calls)
	}
}

func TestCleanContextStillAttachesAuthorization(t *testing.T) {
	// 正向：没有人预先挂时一切照旧。只断拒绝方向的话，
	// 把守卫写成「永远返回错误」也能全绿。
	client, conn := authorizationGuardClient()
	if err := client.Invoke(context.Background(), "/runtime.v1.RuntimeService/GetBalance", nil, nil); err != nil {
		t.Fatalf("干净 context 必须照常发出：%v", err)
	}
	if conn.calls != 1 {
		t.Fatalf("干净 context 应发出恰好 1 次，实际 %d", conn.calls)
	}
}

// TestEveryAuthorizationAppendIsGuarded 是类守卫：本仓每一个
// AppendToOutgoingContext("authorization", ...) 所在的方法，都必须先调用
// assertNoOutgoingAuthorization。上面三条只覆盖当前这两个站点，
// 将来新增一个追加点时它们不会变红——这一条会。
func TestEveryAuthorizationAppendIsGuarded(t *testing.T) {
	// 扫描面交给 git（见 repoGoFiles）。这里曾经是 filepath.WalkDir(".")，
	// 而那会把 `go mod vendor` 生成的六百多个第三方 .go 一起扫进来——
	// grpc 自己就有 AppendToOutgoingContext，当然不会调用本仓的守卫函数，
	// 于是这条闸在一台只是跑过 vendor 的机器上无故变红。
	files, err := repoGoFiles(".")
	if err != nil {
		t.Fatal(err)
	}
	appendSites := 0
	var unguarded []string
	for _, name := range files {
		fset := token.NewFileSet()
		file, parseErr := parser.ParseFile(fset, name, nil, 0)
		if parseErr != nil {
			t.Fatalf("解析 %s：%v", name, parseErr)
		}
		for _, decl := range file.Decls {
			fn, ok := decl.(*ast.FuncDecl)
			if !ok || fn.Body == nil {
				continue
			}
			appends, guarded := scanAuthorizationUse(fn.Body)
			if appends == 0 {
				continue
			}
			appendSites += appends
			if !guarded {
				unguarded = append(unguarded, fset.Position(fn.Pos()).String()+" "+fn.Name.Name)
			}
		}
	}
	if appendSites == 0 {
		t.Fatal("一个 authorization 追加点都没扫到——守卫已失效，先修守卫")
	}
	if len(unguarded) != 0 {
		t.Fatalf("以下方法挂 authorization 前没有调用 assertNoOutgoingAuthorization：\n  %s",
			strings.Join(unguarded, "\n  "))
	}
}

func scanAuthorizationUse(body *ast.BlockStmt) (int, bool) {
	var appendPositions []token.Pos
	var guardPositions []token.Pos
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		if isAuthorizationMutation(call) {
			appendPositions = append(appendPositions, call.Pos())
		}
		if fn, ok := call.Fun.(*ast.Ident); ok && fn.Name == "assertNoOutgoingAuthorization" {
			guardPositions = append(guardPositions, call.Pos())
		}
		return true
	})
	guarded := true
	for _, appendPosition := range appendPositions {
		guardFound := false
		for _, guardPosition := range guardPositions {
			if guardPosition < appendPosition {
				guardFound = true
				break
			}
		}
		if !guardFound {
			guarded = false
			break
		}
	}
	return len(appendPositions), guarded
}

func callHasAuthorizationArg(call *ast.CallExpr) bool {
	for _, arg := range call.Args {
		if isAuthorizationLiteral(arg) {
			return true
		}
	}
	return false
}

func isAuthorizationLiteral(expr ast.Expr) bool {
	literal, ok := expr.(*ast.BasicLit)
	if !ok || literal.Kind != token.STRING {
		return false
	}
	value, err := strconv.Unquote(literal.Value)
	return err == nil && strings.EqualFold(value, "authorization")
}

func isAuthorizationMutation(call *ast.CallExpr) bool {
	selector, ok := call.Fun.(*ast.SelectorExpr)
	if !ok {
		return false
	}
	switch selector.Sel.Name {
	case "AppendToOutgoingContext":
		return callHasAuthorizationArg(call)
	case "Set", "Append":
		return isMetadataReceiver(selector.X) && len(call.Args) > 0 && isAuthorizationLiteral(call.Args[0])
	case "NewOutgoingContext":
		// The metadata argument may be an identifier built elsewhere, so the AST
		// cannot prove that it contains no authorization. Treat every outgoing
		// metadata boundary as requiring the same guard.
		return true
	default:
		return false
	}
}

func isMetadataReceiver(expr ast.Expr) bool {
	ident, ok := expr.(*ast.Ident)
	if !ok {
		return false
	}
	name := strings.ToLower(ident.Name)
	return name == "md" || strings.Contains(name, "metadata")
}
