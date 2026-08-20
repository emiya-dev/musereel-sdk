package musereelsdk

import (
	"os"
	"path/filepath"
	"regexp"
	"sort"
	"strings"
	"testing"
)

// 这道闸看住 README 里那张 conformance 环境变量表。
//
// 存在理由是它已经烂过一次：2026-08-20 查出 README 的可选表里列着
// MUSEREEL_CONFORMANCE_MODERATION_RECEIPT，而全仓没有一行代码读它——审核收据是
// harness 先跑一次 moderation.generate.v1 调用、再从终态结果里读出来的，从外面
// 根本塞不进去。照着表设了这个变量的人不会得到任何反馈，只会以为自己配好了。
//
// 这和本仓对 gateway anchor 值"坚决不重述"是同一条教训：一份被抄进散文的清单，
// 只要没有机器判据跟事实对拍，就一定会分叉。区别只在于 anchor 那条已经吃过亏并
// 立了规矩，而环境变量表这一维当时没人管——它是本仓唯一没有闸的那张表，
// 也确实就是唯一烂掉的那张。
//
// 🔴 判据必须是**双向**的：README 多列（幽灵变量）和代码多读（漏记文档）都要红。
// 只查"README 里的都存在"这一个方向，把表整个删空也能过闸。
//
// ⚠ 这道闸对"提及"和"列表项"不作区分——README 里**任何地方**写出完整变量名都算数。
// 这是故意的保守设计，第一次装它的时候就撞上了：当时想在正文里写一句"早先错列过
// MUSEREEL_CONFORMANCE_MODERATION_RECEIPT"，立刻被自己抓红。
// 分开判"哪些提及不算"需要一份豁免名单，而豁免名单本身没人看住，最后就是这道闸
// 被绕过去的那个口子。⇒ 结论反过来用：README 只讲当前事实，
// "曾经写错过什么"属于提交信息，不属于给读者看的文档。
var conformanceEnvPattern = regexp.MustCompile(`MUSEREEL_CONFORMANCE_[A-Z0-9_]+`)

// conformanceEnvNamesIn 提取一个文件里出现的全部 conformance 环境变量名。
// skipComments 只对 Go 源码开启：注释里提到某个变量名（例如说明它已废弃）
// 不等于代码读它，把注释算进来会让这道闸假红。
func conformanceEnvNamesIn(t *testing.T, path string, skipComments bool) map[string]struct{} {
	t.Helper()
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取 %s 失败：%v", path, err)
	}
	names := map[string]struct{}{}
	for _, line := range strings.Split(string(raw), "\n") {
		if skipComments {
			if index := strings.Index(line, "//"); index >= 0 {
				line = line[:index]
			}
		}
		for _, name := range conformanceEnvPattern.FindAllString(line, -1) {
			names[name] = struct{}{}
		}
	}
	return names
}

func sortedNames(set map[string]struct{}) []string {
	out := make([]string, 0, len(set))
	for name := range set {
		out = append(out, name)
	}
	sort.Strings(out)
	return out
}

func missingFrom(want, have map[string]struct{}) []string {
	var out []string
	for name := range want {
		if _, ok := have[name]; !ok {
			out = append(out, name)
		}
	}
	sort.Strings(out)
	return out
}

func TestREADMEConformanceEnvTableMatchesCode(t *testing.T) {
	codeNames := map[string]struct{}{}
	entries, err := os.ReadDir("conformance")
	if err != nil {
		t.Fatalf("读取 conformance 目录失败：%v", err)
	}
	var scanned []string
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") || strings.HasSuffix(name, "_test.go") {
			continue
		}
		scanned = append(scanned, name)
		for envName := range conformanceEnvNamesIn(t, filepath.Join("conformance", name), true) {
			codeNames[envName] = struct{}{}
		}
	}
	if len(scanned) == 0 {
		t.Fatal("conformance/ 下没有扫到任何非测试 Go 文件——扫描面塌了，这道闸已失去意义")
	}
	if len(codeNames) == 0 {
		t.Fatalf("在 %v 里一个 MUSEREEL_CONFORMANCE_* 都没提取到——正则或扫描面坏了", scanned)
	}

	for _, readme := range []string{"README.md", "README.zh-CN.md"} {
		docNames := conformanceEnvNamesIn(t, readme, false)
		if ghosts := missingFrom(docNames, codeNames); len(ghosts) != 0 {
			t.Errorf("%s 列了代码从不读取的环境变量 %v。\n"+
				"设置它们不会有任何效果。要么在 conformance 里真的读，要么把它们从表里删掉——"+
				"别留一个看起来能配、实际无效的旋钮。\n扫描面：conformance/%v",
				readme, ghosts, scanned)
		}
		if undocumented := missingFrom(codeNames, docNames); len(undocumented) != 0 {
			t.Errorf("%s 漏记了代码实际读取的环境变量 %v。\n"+
				"跑这条腿的人照着 README 配就会缺项。\n扫描面：conformance/%v",
				readme, undocumented, scanned)
		}
	}

	// 中英两份必须列同一张表：漂移了就说明有人只改了一边。
	englishNames := conformanceEnvNamesIn(t, "README.md", false)
	chineseNames := conformanceEnvNamesIn(t, "README.zh-CN.md", false)
	if onlyEnglish := missingFrom(englishNames, chineseNames); len(onlyEnglish) != 0 {
		t.Errorf("README.md 有而 README.zh-CN.md 没有的环境变量：%v（两份必须对等）", onlyEnglish)
	}
	if onlyChinese := missingFrom(chineseNames, englishNames); len(onlyChinese) != 0 {
		t.Errorf("README.zh-CN.md 有而 README.md 没有的环境变量：%v（两份必须对等）", onlyChinese)
	}
	t.Logf("对拍通过：%d 个环境变量，扫描面 conformance/%v，表 %v",
		len(codeNames), scanned, sortedNames(codeNames))
}
