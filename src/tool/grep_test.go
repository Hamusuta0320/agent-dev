package tool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"unicode/utf8"
)

func runGrep(t *testing.T, args map[string]any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("序列化参数失败: %v", err)
	}
	return GrepTool{}.Execute(raw)
}

// newTempTree 按 map[相对路径]内容 建一棵临时目录树，返回根目录
func newTempTree(t *testing.T, files map[string]string) string {
	t.Helper()
	root := t.TempDir()
	for rel, content := range files {
		path := filepath.Join(root, filepath.FromSlash(rel))
		if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
			t.Fatalf("创建目录失败: %v", err)
		}
		if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
			t.Fatalf("写文件失败: %v", err)
		}
	}
	return root
}

func TestGrepBasic(t *testing.T) {
	root := newTempTree(t, map[string]string{
		"a.txt":     "first\nsecond TODO here\nthird\n",
		"sub/b.txt": "nothing\nTODO too\n",
	})
	out, err := runGrep(t, map[string]any{"pattern": "TODO", "path": root})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "找到 2 处匹配") {
		t.Fatalf("匹配数不符合预期: %s", out)
	}
	if !strings.Contains(out, "a.txt:2:second TODO here") {
		t.Fatalf("缺少 a.txt 的行号与内容: %s", out)
	}
	if !strings.Contains(out, "sub/b.txt:2:TODO too") {
		t.Fatalf("缺少 sub/b.txt 的匹配: %s", out)
	}
}

func TestGrepMetaCharsEscaped(t *testing.T) {
	root := newTempTree(t, map[string]string{
		"main.go": "fmt.Println(\"hi\")\nprintln!(\"hi\")\n",
	})
	out, err := runGrep(t, map[string]any{"pattern": `fmt\.Println`, "path": root})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "main.go:1:fmt.Println") {
		t.Fatalf("正则匹配结果不符合预期: %s", out)
	}
	if strings.Contains(out, "main.go:2:") {
		t.Fatalf("不该匹配到 println!: %s", out)
	}
}

func TestGrepCaseInsensitive(t *testing.T) {
	root := newTempTree(t, map[string]string{"a.txt": "Todo\nTODO\n"})
	out, err := runGrep(t, map[string]any{
		"pattern":          "todo",
		"path":             root,
		"case_insensitive": true,
	})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "找到 2 处匹配") {
		t.Fatalf("忽略大小写时应该匹配 2 处: %s", out)
	}
}

func TestGrepGlobFilter(t *testing.T) {
	root := newTempTree(t, map[string]string{
		"a.go":   "hit\n",
		"b.md":   "hit\n",
		"c.go":   "hit\n",
		"d.yaml": "hit\n",
	})
	out, err := runGrep(t, map[string]any{"pattern": "hit", "path": root, "glob": "*.go"})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "找到 2 处匹配") {
		t.Fatalf("glob 过滤后应该只剩 2 处: %s", out)
	}
	if strings.Contains(out, "b.md") || strings.Contains(out, "d.yaml") {
		t.Fatalf("不该搜到非 .go 文件: %s", out)
	}
}

func TestGrepSkipIgnoredDirsAndBinary(t *testing.T) {
	root := newTempTree(t, map[string]string{
		"keep.txt":            "needle\n",
		".git/objects/x.txt":  "needle\n",
		"node_modules/m/a.js": "needle\n",
	})
	// 二进制文件里也塞一个 needle
	bin := append([]byte("needle\x00\x01\x02"), []byte("more needle")...)
	if err := os.WriteFile(filepath.Join(root, "blob.bin"), bin, 0o644); err != nil {
		t.Fatalf("写二进制文件失败: %v", err)
	}

	out, err := runGrep(t, map[string]any{"pattern": "needle", "path": root})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "keep.txt:1:needle") {
		t.Fatalf("应该搜到 keep.txt: %s", out)
	}
	if strings.Contains(out, ".git") || strings.Contains(out, "node_modules") {
		t.Fatalf("不该进 .git / node_modules: %s", out)
	}
	if strings.Contains(out, "blob.bin") {
		t.Fatalf("不该搜二进制文件: %s", out)
	}
}

func TestGrepMaxResults(t *testing.T) {
	lines := strings.Repeat("hit\n", 10)
	root := newTempTree(t, map[string]string{"a.txt": lines})
	out, err := runGrep(t, map[string]any{"pattern": "hit", "path": root, "max_results": 3})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "找到 3 处匹配") {
		t.Fatalf("应该只返回 3 处: %s", out)
	}
	if !strings.Contains(out, "结果已截断") {
		t.Fatalf("超过 max_results 要提示截断: %s", out)
	}
}

func TestGrepNoMatch(t *testing.T) {
	root := newTempTree(t, map[string]string{"a.txt": "abc\n"})
	out, err := runGrep(t, map[string]any{"pattern": "zzz", "path": root})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "没有匹配") {
		t.Fatalf("没有匹配时应给出提示: %s", out)
	}
}

func TestGrepInvalidPattern(t *testing.T) {
	root := newTempTree(t, map[string]string{"a.txt": "abc\n"})
	if _, err := runGrep(t, map[string]any{"pattern": "a(b", "path": root}); err == nil {
		t.Fatal("非法正则应该报错")
	}
}

func TestGrepMissingPattern(t *testing.T) {
	if _, err := (GrepTool{}).Execute(json.RawMessage(`{}`)); err == nil {
		t.Fatal("缺 pattern 应该报错")
	}
}

func TestGrepSingleFile(t *testing.T) {
	root := newTempTree(t, map[string]string{"a.txt": "x\ny\nz\n"})
	out, err := runGrep(t, map[string]any{"pattern": "^y$", "path": filepath.Join(root, "a.txt")})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "a.txt:2:y") {
		t.Fatalf("单文件搜索不符合预期: %s", out)
	}
}

// 通配符带路径分隔符时只应匹配相对路径的对应层级，不能越级匹配
func TestGrepGlobWithPathSeparator(t *testing.T) {
	root := newTempTree(t, map[string]string{
		"sub/a.go":      "hit\n",
		"sub/b.md":      "hit\n",
		"sub/deep/d.go": "hit\n",
		"other/c.go":    "hit\n",
	})
	out, err := runGrep(t, map[string]any{"pattern": "hit", "path": root, "glob": "sub/*.go"})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "a.go:1:hit") {
		t.Fatalf("应该匹配到 sub/a.go: %s", out)
	}
	if strings.Contains(out, "d.go") || strings.Contains(out, "c.go") {
		t.Fatalf("sub/*.go 不该匹配到更深层或其他目录下的文件: %s", out)
	}
}

// 多个通配符要用逗号 / 空格拆开分别生效
func TestGrepMultiGlobs(t *testing.T) {
	root := newTempTree(t, map[string]string{
		"a.go":   "hit\n",
		"b.md":   "hit\n",
		"c.yaml": "hit\n",
		"d.txt":  "hit\n",
	})
	out, err := runGrep(t, map[string]any{"pattern": "hit", "path": root, "glob": "*.go, *.yaml"})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "找到 2 处匹配") {
		t.Fatalf("多个 glob 应该只匹配 .go 和 .yaml: %s", out)
	}
}

// CRLF 文件的行尾 \r 不应出现在输出里
func TestGrepCRLF(t *testing.T) {
	root := newTempTree(t, map[string]string{"a.txt": "first\r\nTODO here\r\nlast\r\n"})
	out, err := runGrep(t, map[string]any{"pattern": "TODO", "path": root})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "a.txt:2:TODO here") {
		t.Fatalf("CRLF 文件的行内容不符合预期: %q", out)
	}
	if strings.Contains(out, "\r") {
		t.Fatalf("输出里不该带 \\r: %q", out)
	}
}

// 超长行要截断，且不能切在多字节字符中间
func TestGrepLongLineTruncated(t *testing.T) {
	long := strings.Repeat("中", 300) + "MARK" + strings.Repeat("文", 300)
	root := newTempTree(t, map[string]string{"long.txt": long + "\n"})
	out, err := runGrep(t, map[string]any{"pattern": "MARK", "path": root})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "本行过长") {
		t.Fatalf("超长行应该提示已截断: %s", out)
	}
	if !utf8.ValidString(out) || strings.ContainsRune(out, utf8.RuneError) {
		t.Fatalf("截断后出现了非法 UTF-8: %q", out)
	}
	if len(out) > 2*maxGrepDisplayBytes {
		t.Fatalf("展示内容过长（%d 字节），说明没截断", len(out))
	}
}

// path 直接指向二进制文件时，要说清楚是跳过了，而不是误报"没有匹配"
func TestGrepSingleFileBinary(t *testing.T) {
	dir := t.TempDir()
	p := filepath.Join(dir, "blob.bin")
	if err := os.WriteFile(p, []byte("needle\x00needle"), 0o644); err != nil {
		t.Fatalf("写二进制文件失败: %v", err)
	}
	out, err := runGrep(t, map[string]any{"pattern": "needle", "path": p})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "二进制") {
		t.Fatalf("应提示这是二进制文件: %s", out)
	}
}

// 递归搜索时跳过的文件数体现在提示里
func TestGrepCountsSkippedFiles(t *testing.T) {
	root := newTempTree(t, map[string]string{"a.txt": "nothing\n"})
	if err := os.WriteFile(filepath.Join(root, "blob.bin"), []byte("x\x00y"), 0o644); err != nil {
		t.Fatalf("写二进制文件失败: %v", err)
	}
	out, err := runGrep(t, map[string]any{"pattern": "zzz", "path": root})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "跳过 1 个") {
		t.Fatalf("应说明跳过了二进制文件: %s", out)
	}
}
