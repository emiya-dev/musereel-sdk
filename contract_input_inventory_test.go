package musereelsdk

import (
	"os"
	"os/exec"
	"path/filepath"
	"strings"
	"testing"
)

// contract-input/ 的「只能有两类文件」这条规矩，本体是
// scripts/check-contract-pin.sh 里的目录清点。这条测试守的是**那个守卫本身**：
// 清点逻辑一旦退化（比如有人为了图省事改成只看顶层、或改成白名单后缀），
// 表现是「假镜像放进去照样绿」——与「仓里本来就没有假镜像」完全同形。
//
// ⚠ 用真实 contract-input/ 目录，不是临时拷贝：脚本按 dirname $0/.. 定位仓根，
// 给它加一个 root 覆盖开关等于给门禁开后门。代价是测试期间会在工作树里
// 短暂出现探针文件，由 t.Cleanup 删除，且文件名自带 DELETE_ME 以防万一。
func TestContractInputInventoryGateCatchesUnregisteredFile(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "scripts", "check-contract-pin.sh")
	if _, err := os.Stat(script); err != nil {
		t.Fatalf("找不到 pin 脚本：%v", err)
	}

	// 正控：先证明干净状态下它是绿的。少了这一步，下面的负控无法区分
	// 「守卫抓住了探针」与「这个仓本来就有别的东西让它红」。
	if out, err := exec.Command("sh", script).CombinedOutput(); err != nil {
		t.Fatalf("干净状态下 pin 门禁就是红的，负控失去意义：%v\n%s", err, out)
	}

	for _, probe := range []struct {
		name string
		path string
	}{
		{"顶层未登记文件", filepath.Join("contract-input", "zz_guard_probe_DELETE_ME.json")},
		// 子目录这一格是承重的：reference/jcs-server-reference.go.txt 当年就躺在子目录里。
		{"子目录未登记文件", filepath.Join("contract-input", "zz_probe_dir", "nested_DELETE_ME.txt")},
		// 这一格守的是 grep 的 -x（整行匹配）那个字母。"time.proto" 是已登记项
		// "runtime.proto" 的子串：把 -Fxq 退成 -Fq，它会被**静默放行**（实测 REAL_EXIT=0）。
		// 少了这一格，那个字母掉了没有任何东西会红。
		{"名字是已登记项子串的文件", filepath.Join("contract-input", "time.proto")},
	} {
		t.Run(probe.name, func(t *testing.T) {
			full := filepath.Join(root, probe.path)
			if err := os.MkdirAll(filepath.Dir(full), 0o755); err != nil {
				t.Fatal(err)
			}
			if err := os.WriteFile(full, []byte("not a registered mirror\n"), 0o644); err != nil {
				t.Fatal(err)
			}
			t.Cleanup(func() {
				os.Remove(full)
				os.Remove(filepath.Join(root, "contract-input", "zz_probe_dir"))
			})

			output, err := exec.Command("sh", script).CombinedOutput()
			if err == nil {
				t.Fatalf("放了一份未登记文件，pin 门禁却是绿的——清点逻辑已失效\n%s", output)
			}
			// 只断言"红了"不够：脚本前面还有一堆别的红法（缺文件、hash 不符、
			// 工具链不符），任何一条都能让 err != nil。必须确认红的是清点这一关，
			// 且**点出了是哪个文件**——那是 sluice-4d 的完成判据。
			text := string(output)
			if !strings.Contains(text, "既没被哈希、也没登记为 pin record") {
				t.Fatalf("门禁红了，但不是因为目录清点：\n%s", text)
			}
			base := filepath.Base(probe.path)
			if !strings.Contains(text, base) {
				t.Fatalf("清点报错里没有点名 %q，排查时无从下手：\n%s", base, text)
			}
		})
	}

	// 探针删干净之后必须回到绿——否则这条测试会给后面的用例留下污染。
	if out, err := exec.Command("sh", script).CombinedOutput(); err != nil {
		t.Fatalf("探针清理后 pin 门禁仍是红的，工作树可能被污染了：%v\n%s", err, out)
	}
}

// 与上面那张探针表相反的一维：**git 已经忽略的文件不该让门禁变红。**
//
// 这不是便利性让步。macOS 上在 Finder 里点一下 contract-input/ 就会生成 .DS_Store，
// 清点不跳过它的话门禁当场红——一条会稳定假红的闸，最后的下场是被人绕过去，
// 于是它本来要守的东西也一起没了。判据用 git 而不是写死一张杂物名单：
// 名单永远漏，而"在不在仓里"是仓自己回答的。
//
// 负对照：摘掉脚本里那三行 check-ignore 跳过 ⇒ 本用例变红（2026-08-20 实测 REAL_EXIT=1）。
func TestContractInputInventoryIgnoresGitIgnoredFiles(t *testing.T) {
	root, err := os.Getwd()
	if err != nil {
		t.Fatal(err)
	}
	script := filepath.Join(root, "scripts", "check-contract-pin.sh")

	// 正控：干净状态先得是绿的，否则下面绿不绿都说明不了问题。
	if out, err := exec.Command("sh", script).CombinedOutput(); err != nil {
		t.Fatalf("干净状态下 pin 门禁就是红的：%v\n%s", err, out)
	}

	probe := filepath.Join(root, "contract-input", ".DS_Store")

	// 先钉住前提：.gitignore 必须真的管住这个探针。少了这一步，哪天 .gitignore 被改，
	// 本用例会退化成「探针根本不是被忽略的」而仍然绿——那是另一种同形的假绿。
	if err := os.WriteFile(probe, []byte("finder junk\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() { os.Remove(probe) })
	if err := exec.Command("git", "-C", root, "check-ignore", "-q", "--", probe).Run(); err != nil {
		t.Fatalf(".gitignore 没有管住 %s —— 本用例的前提不成立，先修 .gitignore", probe)
	}

	if out, err := exec.Command("sh", script).CombinedOutput(); err != nil {
		t.Fatalf("git 已忽略的文件让门禁变红了；这条闸会在每台 macOS 开发机上假红：%v\n%s", err, out)
	}
}
