package musereelsdk

import (
	"io/fs"
	"os"
	"os/exec"
	"path/filepath"
	"sort"
	"strings"
	"testing"
)

// repoGoFiles 列出**属于这个仓**的非测试 Go 文件，供类守卫当扫描面用。
//
// 为什么不直接 filepath.WalkDir 整棵树：那样扫的是「这个目录下碰巧有什么」，
// 不是「这个仓有什么」。`go mod vendor` 是个完全正常的动作，它会往树里放
// 六百多个第三方 .go；WalkDir 全都会扫到，于是 negative-surface 的词表当场命中
// grpc 的 cost/vendor 字样、authorization 守卫当场命中 grpc 自己的
// AppendToOutgoingContext——**两条闸一起假红，而仓里一行代码都没错。**
//
// 判据交给 git，不在这里写死一张目录名单（vendor/、testdata/、别人放进来的
// worktree……）：名单永远漏，而「在不在仓里」是仓自己回答的。这与 pin 门禁跳过
// git 已忽略文件是同一个办法。
//
// 用 --cached --others --exclude-standard 而不是只用 --cached：已跟踪的 +
// 未跟踪但没被 .gitignore 管住的。后半截是必须的，否则新写还没 git add 的文件
// 会漏出守卫，变成「提交前绿、提交后红」——守卫在最该说话的时候闭嘴。
//
// root 不是 git 检出时（负对照喂的合成临时目录就是这种）退化成 WalkDir，
// 与本改动前的行为一致。
func repoGoFiles(root string) ([]string, error) {
	output, err := exec.Command("git", "-C", root,
		"ls-files", "--cached", "--others", "--exclude-standard", "-z", "--", "*.go").Output()
	if err != nil {
		return walkGoFiles(root)
	}
	var files []string
	for _, relative := range strings.Split(string(output), "\x00") {
		if relative == "" || strings.HasSuffix(relative, "_test.go") {
			continue
		}
		files = append(files, filepath.Join(root, relative))
	}
	sort.Strings(files)
	return files, nil
}

// walkGoFiles 是 repoGoFiles 在非 git 目录上的退化路径。
func walkGoFiles(root string) ([]string, error) {
	var files []string
	err := filepath.WalkDir(root, func(path string, entry fs.DirEntry, walkErr error) error {
		if walkErr != nil {
			return walkErr
		}
		if entry.IsDir() {
			if entry.Name() == ".git" {
				return fs.SkipDir
			}
			return nil
		}
		if filepath.Ext(path) != ".go" || strings.HasSuffix(path, "_test.go") {
			return nil
		}
		files = append(files, path)
		return nil
	})
	if err != nil {
		return nil, err
	}
	sort.Strings(files)
	return files, nil
}

// TestRepoGoFilesExcludesVendorButKeepsUntracked 钉住扫描面的两条边。
//
// 它们必须**同时**成立，少一条这个 helper 就没有意义：
//
//	· vendor/ 那六百多个第三方 .go 不能进扫描面 —— 否则两条类守卫在一台
//	  只是跑过 `go mod vendor` 的机器上无故变红；
//	· 未跟踪但没被忽略的 .go **必须**进扫描面 —— 否则守卫会漏掉刚写好还没
//	  git add 的代码，变成「提交前绿、提交后红」，在最该说话的时候闭嘴。
//
// 只测第一条会退化成「把扫描面缩到越小越好」，那样守卫迟早瞎掉。
func TestRepoGoFilesExcludesVendorButKeepsUntracked(t *testing.T) {
	vendorFile := filepath.Join("vendor", "example.com", "dep", "dep.go")
	untrackedFile := "zz_untracked_probe_DELETE_ME.go"

	if err := os.MkdirAll(filepath.Dir(vendorFile), 0o755); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(vendorFile, []byte("package dep\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	if err := os.WriteFile(untrackedFile, []byte("package musereelsdk\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	t.Cleanup(func() {
		os.RemoveAll("vendor")
		os.Remove(untrackedFile)
	})

	// 前提断言：vendor/ 之所以被排除，是因为 .gitignore 管住了它，不是因为
	// helper 里写死了这个名字。这条要是没了，下面那条会退化成「碰巧没扫到」。
	if err := exec.Command("git", "check-ignore", "-q", "--", vendorFile).Run(); err != nil {
		t.Fatalf(".gitignore 没有管住 %s —— vendor 排除依赖的是 git 判据，不是硬编码名单", vendorFile)
	}

	files, err := repoGoFiles(".")
	if err != nil {
		t.Fatal(err)
	}
	var sawVendor, sawUntracked bool
	for _, file := range files {
		if strings.HasPrefix(filepath.ToSlash(filepath.Clean(file)), "vendor/") {
			sawVendor = true
		}
		if filepath.Base(file) == untrackedFile {
			sawUntracked = true
		}
	}
	if sawVendor {
		t.Error("vendor/ 下的第三方代码进了扫描面 —— 两条类守卫会在跑过 go mod vendor 的机器上假红")
	}
	if !sawUntracked {
		t.Error("未跟踪但未被忽略的 .go 没进扫描面 —— 守卫会漏掉还没 git add 的代码")
	}
}

// TestWalkGoFilesFallbackOutsideGit 守退化路径：合成负对照喂的是临时目录，
// 不是 git 检出。退化没了的话，negative-surface 的六类合成样本会一起变成空集，
// 而空集上「零命中」恒真 —— 守卫看起来全绿，其实什么都没扫。
func TestWalkGoFilesFallbackOutsideGit(t *testing.T) {
	outside := t.TempDir()
	if err := os.WriteFile(filepath.Join(outside, "sample.go"), []byte("package sample\n"), 0o644); err != nil {
		t.Fatal(err)
	}
	files, err := repoGoFiles(outside)
	if err != nil {
		t.Fatal(err)
	}
	if len(files) != 1 {
		t.Fatalf("非 git 目录下应退化成 WalkDir 并找到 1 个文件，实际 %d 个：%v", len(files), files)
	}
}
