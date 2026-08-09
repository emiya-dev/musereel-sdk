package musereelsdk

import (
	"context"
	"go/ast"
	"go/parser"
	"go/token"
	"os"
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
	entries, err := os.ReadDir(".")
	if err != nil {
		t.Fatal(err)
	}
	appendSites := 0
	var unguarded []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
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
	appends := 0
	guarded := false
	ast.Inspect(body, func(node ast.Node) bool {
		call, ok := node.(*ast.CallExpr)
		if !ok {
			return true
		}
		switch fn := call.Fun.(type) {
		case *ast.SelectorExpr:
			if fn.Sel.Name == "AppendToOutgoingContext" && callHasAuthorizationArg(call) {
				appends++
			}
		case *ast.Ident:
			if fn.Name == "assertNoOutgoingAuthorization" {
				guarded = true
			}
		}
		return true
	})
	return appends, guarded
}

func callHasAuthorizationArg(call *ast.CallExpr) bool {
	for _, arg := range call.Args {
		literal, ok := arg.(*ast.BasicLit)
		if ok && literal.Kind == token.STRING &&
			strings.EqualFold(strings.Trim(literal.Value, `"`), "authorization") {
			return true
		}
	}
	return false
}
