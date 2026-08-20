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
// 🔴 **这道闸锁的是「名字集合」，不是那张表的正确性。** 想清楚它不管什么，
// 比想清楚它管什么更要紧——否则下一个人会把"闸是绿的"读成"表是对的"：
//
//	· **不锁必填/可选的分类。** 把 GATEWAY_URL 挪进可选表、或把 EVENT_ID
//	  写进必填表，名字集合没变，闸照绿。（前者空值 fail-fast，后者空值会
//	  自动生成 sdk005-conformance-<ns>，两种错分类给读者的后果完全不同。）
//	· **不锁允许值与默认值。** DELIVERY_MODE 写成接受 sync、video 的 schema
//	  默认从 3 改成 1，闸都看不见——它不读那些句子。
//	· **"名字出现过" ≠ "代码真的读它"。** SKU_ID 与 DELIVERY_MODE 同时出现在
//	  必填 slice 和 fmt.Errorf 的消息里；把它从必填 slice 删掉、只留错误消息，
//	  闸仍认为代码在读，于是幽灵变量能原样复活。要真正堵住这一条得做 AST，
//	  那个代价这里不值得——它锁住的是后果最重的那一维（配了完全没反应、
//	  且没有任何信号），分类和默认值配错至少还会从报错里得到线索。
//
// 判据必须是**双向**的：README 多列（幽灵变量）和代码多读（漏记文档）都要红。
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
			if index := commentStart(line); index >= 0 {
				line = line[:index]
			}
		}
		for _, name := range conformanceEnvPattern.FindAllString(line, -1) {
			names[name] = struct{}{}
		}
	}
	return names
}

// commentStart 找出行注释的起点，**跳过 scheme://** 里那对斜杠。
//
// 直接用 strings.Index(line, "//") 是不行的：一行里只要出现 https://，
// 这行就会从协议分隔符处被截断，后面真实的 os.Getenv 一起被当成注释丢掉，
// 于是那个变量不进 codeNames——README 没列它也不会红（漏记那一侧假绿），
// 而 README 列了它反倒会被判成幽灵变量（另一侧假红）。两个方向都会坏。
// 当前 conformance/*.go 里零 URL，所以这从来没发作过；它是个迟早会踩的口子，
// 不是现存缺陷。
func commentStart(line string) int {
	for index := 0; index+1 < len(line); index++ {
		if line[index] != '/' || line[index+1] != '/' {
			continue
		}
		if index > 0 && line[index-1] == ':' {
			continue
		}
		return index
	}
	return -1
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

// 上面那道闸靠**字面量**认变量名，因此有两个它看不见的入口。两个都不会让它变红，
// 只会让它不再覆盖新加的变量——而"绿"和"覆盖完整"在外面看完全同形，
// 这正是守卫最坏的失效方式：它还在跑，只是不再说话了。
//
// ⇒ 与其等哪天有人踩中，不如把这两个入口本身钉住：出现就红，并直接说清
// 该去扩大哪一侧的扫描面。当前两者都是零命中，所以这条断言不会无故假红。
func TestConformanceEnvReadsStayInsideGuardedSurface(t *testing.T) {
	entries, err := os.ReadDir("conformance")
	if err != nil {
		t.Fatalf("读取 conformance 目录失败：%v", err)
	}

	// 入口①：把 os.Getenv 挪进 _test.go。上面那道闸刻意跳过测试文件
	// （测试里出现的变量名不代表产品代码真的读它），于是挪过去就等于挪出扫描面。
	// 入口②：拼接出变量名，例如 os.Getenv("MUSEREEL_CONFORMANCE_" + suffix)。
	// 拼出来的名字在源码里根本不作为一个完整字面量存在，正则永远抓不到。
	concatenated := regexp.MustCompile(`os\.Getenv\([^)]*(\+|Sprintf)`)
	for _, entry := range entries {
		name := entry.Name()
		if entry.IsDir() || !strings.HasSuffix(name, ".go") {
			continue
		}
		raw, err := os.ReadFile(filepath.Join("conformance", name))
		if err != nil {
			t.Fatalf("读取 conformance/%s 失败：%v", name, err)
		}
		body := string(raw)

		if strings.HasSuffix(name, "_test.go") && strings.Contains(body, "os.Getenv") {
			t.Errorf("conformance/%s 是测试文件却读了环境变量。"+
				"TestREADMEConformanceEnvTableMatchesCode 的扫描面刻意排除 _test.go，"+
				"所以这里读的变量不会被拿去和 README 对拍——表可以缺它而闸依然绿。"+
				"要么把读取移回非测试文件，要么把扫描面扩到这个文件，"+
				"并想清楚测试里的负控假变量名会不会因此假红。", name)
		}
		if location := concatenated.FindString(body); location != "" {
			t.Errorf("conformance/%s 用拼接的方式读环境变量：%q。"+
				"拼出来的名字在源码里不是一个完整字面量，对拍闸看不见它，"+
				"README 缺了它也不会红。"+
				"请改成完整字面量常量，或把这道闸换成 AST 分析。", name, location)
		}
	}
}
