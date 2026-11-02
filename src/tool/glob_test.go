package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runGlob(t *testing.T, args map[string]any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("序列化参数失败: %v", err)
	}
	return GlobTool{}.Execute(context.Background(), raw)
}

// 不写 pattern 就是列目录：只列一层，目录带尾随 /
func TestGlobListsOneLevel(t *testing.T) {
	root := newTempTree(t, map[string]string{
		"a.go":          "package a",
		"b.md":          "readme",
		"sub/c.go":      "package sub",
		"sub/deep/d.go": "package deep",
	})

	out, err := runGlob(t, map[string]any{"path": root})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	for _, want := range []string{"a.go", "b.md", "sub/"} {
		if !strings.Contains(out, want) {
			t.Fatalf("列目录应该有 %q: %s", want, out)
		}
	}
	// 不递归：子目录里的东西不该出现
	for _, unwanted := range []string{"c.go", "d.go"} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("pattern 没写 ** 时不该递归，却列出了 %q: %s", unwanted, out)
		}
	}
	if !strings.Contains(out, "2 个文件，1 个目录") {
		t.Fatalf("统计不对: %s", out)
	}
}

// 不含 / 的 pattern 只比名字，* 不会跨目录
func TestGlobPatternWithoutSlashIsNotRecursive(t *testing.T) {
	root := newTempTree(t, map[string]string{
		"a.go":     "package a",
		"sub/b.go": "package b",
	})

	out, err := runGlob(t, map[string]any{"path": root, "pattern": "*.go"})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "a.go") {
		t.Fatalf("应该列出 a.go: %s", out)
	}
	if strings.Contains(out, "b.go") {
		t.Fatalf("*.go 不该匹配子目录里的文件: %s", out)
	}
}

// ** 匹配零到任意层，输出的是相对起点的路径
func TestGlobRecursive(t *testing.T) {
	root := newTempTree(t, map[string]string{
		"a.go":           "package a",
		"b.md":           "readme",
		"sub/c.go":       "package sub",
		"sub/deep/d.go":  "package deep",
		"sub/deep/e.txt": "text",
	})

	out, err := runGlob(t, map[string]any{"path": root, "pattern": "**/*.go"})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	for _, want := range []string{"a.go", "sub/c.go", "sub/deep/d.go"} {
		if !strings.Contains(out, want) {
			t.Fatalf("**/*.go 应该匹配 %q: %s", want, out)
		}
	}
	if strings.Contains(out, "e.txt") || strings.Contains(out, "b.md") {
		t.Fatalf("只该列出 .go: %s", out)
	}
	if !strings.Contains(out, "3 个文件") {
		t.Fatalf("统计不对: %s", out)
	}

	// 限定子目录时只看那一支
	out, err = runGlob(t, map[string]any{"path": root, "pattern": "sub/**/*.go"})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if strings.Contains(out, "a.go\n") || !strings.Contains(out, "sub/c.go") {
		t.Fatalf("sub/**/*.go 只该列 sub 下的文件: %s", out)
	}
}

// 依赖、构建产物、版本库这些目录列出来就够了，不往里钻
func TestGlobSkipsNoisyDirs(t *testing.T) {
	root := newTempTree(t, map[string]string{
		"a.go":                      "package a",
		"node_modules/pkg/index.js": "module.exports = 1",
		".git/config":               "[core]",
		"vendor/github.com/x/x.go":  "package x",
	})

	out, err := runGlob(t, map[string]any{"path": root, "pattern": "**"})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	for _, want := range []string{"a.go", "node_modules/", ".git/", "vendor/"} {
		if !strings.Contains(out, want) {
			t.Fatalf("%q 应该被列出来: %s", want, out)
		}
	}
	for _, unwanted := range []string{"index.js", "config", "github.com"} {
		if strings.Contains(out, unwanted) {
			t.Fatalf("这些目录的内容不该被遍历: %q\n%s", unwanted, out)
		}
	}
}

// 结果太多时要截断并说明，别把上下文刷爆
func TestGlobTruncates(t *testing.T) {
	files := map[string]string{}
	for i := range 10 {
		files["f"+string(rune('0'+i))+".go"] = "package f"
	}
	root := newTempTree(t, files)

	out, err := runGlob(t, map[string]any{"path": root, "pattern": "*.go", "max_results": 3})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "找到 3 项") {
		t.Fatalf("应该只显示 3 项: %s", out)
	}
	if !strings.Contains(out, "已截断") {
		t.Fatalf("截断要说明: %s", out)
	}
}

func TestGlobNoMatch(t *testing.T) {
	root := newTempTree(t, map[string]string{"a.go": "package a"})

	out, err := runGlob(t, map[string]any{"path": root, "pattern": "*.rs"})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "没有匹配") {
		t.Fatalf("没匹配上要说清楚: %s", out)
	}
}

func TestGlobBadArgs(t *testing.T) {
	root := newTempTree(t, map[string]string{"a.go": "package a"})

	// 未闭合的 [ 是非法通配符
	if _, err := runGlob(t, map[string]any{"path": root, "pattern": "[abc"}); err == nil {
		t.Fatal("非法 pattern 应该报错")
	}
	if _, err := runGlob(t, map[string]any{"path": filepath.Join(root, "没有这个目录")}); err == nil {
		t.Fatal("路径不存在应该报错")
	}
	if _, err := runGlob(t, map[string]any{"path": filepath.Join(root, "a.go")}); err == nil {
		t.Fatal("path 传文件时应该提示改用 Read")
	}
	if _, err := runGlob(t, map[string]any{"pattern": strings.Repeat("a/", maxGlobPatternLen)}); err == nil {
		t.Fatal("过长的 pattern 应该报错")
	}
	// 带目录前缀的 pattern 匹配不到东西，不如当场说清楚该改用 path
	if _, err := runGlob(t, map[string]any{"pattern": root + "/*.go"}); err == nil {
		t.Fatal("pattern 带目录前缀时应该提示改用 path")
	}
}

// 用户按 Ctrl+C 之后不该再遍历
func TestGlobCanceled(t *testing.T) {
	root := newTempTree(t, map[string]string{"a.go": "package a"})
	raw, err := json.Marshal(map[string]any{"path": root, "pattern": "**"})
	if err != nil {
		t.Fatalf("序列化参数失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := (GlobTool{}).Execute(ctx, raw); err == nil {
		t.Fatal("已中断时应该直接返回错误")
	}
}

// globMatchSegments 的边界：** 匹配零层，单段通配符不跨层
func TestGlobMatchSegments(t *testing.T) {
	cases := []struct {
		pattern string
		name    string
		want    bool
	}{
		{"**/*.go", "a.go", true},
		{"**/*.go", "sub/deep/a.go", true},
		{"**/*.go", "a.md", false},
		{"*.go", "sub/a.go", false},
		{"src/**", "src/a/b.go", true},
		{"src/**/*.go", "src/a.go", true},
		{"src/**/*.go", "lib/a.go", false},
		{"*", "a", true},
		{"a?c", "abc", true},
		{"[ab].go", "b.go", true},
	}
	for _, c := range cases {
		got := globMatchSegments(strings.Split(c.pattern, "/"), strings.Split(c.name, "/"))
		if got != c.want {
			t.Fatalf("%q 匹配 %q 应该是 %v，实际 %v", c.pattern, c.name, c.want, got)
		}
	}
}

// 列目录出来的路径能直接喂给 Read：斜杠形式、不带 ./
func TestGlobPathUsable(t *testing.T) {
	root := newTempTree(t, map[string]string{"a.go": "package a\n"})
	out, err := runGlob(t, map[string]any{"path": root, "pattern": "*.go"})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	lines := strings.Split(out, "\n")
	last := lines[len(lines)-1]
	if last != "a.go" {
		t.Fatalf("最后一行应该是相对文件名: %q", last)
	}
	if _, err := os.Stat(filepath.Join(root, last)); err != nil {
		t.Fatalf("拼回起点应该能找到文件: %v", err)
	}
}
