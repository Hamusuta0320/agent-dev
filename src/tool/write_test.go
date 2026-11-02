package tool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runWrite(t *testing.T, args map[string]any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("序列化参数失败: %v", err)
	}
	return WriteTool{}.Execute(raw)
}

func TestWriteCreateWithParentDirs(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a", "b", "c.txt")
	out, err := runWrite(t, map[string]any{"path": path, "content": "x\ny\n"})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "2 行") {
		t.Fatalf("行数统计不符合预期: %s", out)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取结果失败: %v", err)
	}
	if string(got) != "x\ny\n" {
		t.Fatalf("文件内容不符合预期: %q", got)
	}
}

func TestWriteOverwrite(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(path, []byte("old content\nold\n"), 0o644); err != nil {
		t.Fatalf("准备文件失败: %v", err)
	}
	if _, err := runWrite(t, map[string]any{"path": path, "content": "new"}); err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取结果失败: %v", err)
	}
	if string(got) != "new" {
		t.Fatalf("覆盖后内容不符合预期: %q", got)
	}
}

func TestWriteEmptyContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "empty.txt")
	out, err := runWrite(t, map[string]any{"path": path, "content": ""})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "0 行") {
		t.Fatalf("空文件行数应为 0: %s", out)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("文件应该被创建: %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("空文件大小应为 0: %d", info.Size())
	}
}

func TestWriteMissingContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.txt")
	if _, err := runWrite(t, map[string]any{"path": path}); err == nil {
		t.Fatal("缺 content 应该报错（空内容需要显式传空字符串）")
	}
}

func TestWriteEmptyPath(t *testing.T) {
	if _, err := runWrite(t, map[string]any{"path": "  ", "content": "x"}); err == nil {
		t.Fatal("path 为空应该报错")
	}
}

func TestWriteToDirectory(t *testing.T) {
	dir := t.TempDir()
	if _, err := runWrite(t, map[string]any{"path": dir, "content": "x"}); err == nil {
		t.Fatal("目标是目录时应该报错")
	}
}

// 内容要原样落盘：不转换 \r\n、不动多字节字符、不补末尾换行
func TestWriteKeepsBytesVerbatim(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.txt")
	content := "中文\r\n第二行\r\n"
	if _, err := runWrite(t, map[string]any{"path": path, "content": content}); err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取结果失败: %v", err)
	}
	if string(got) != content {
		t.Fatalf("内容被改写: %q", got)
	}
}

// 以尾换行结尾时，行数按实际行数算，不要多算一行
func TestWriteLineCountWithTrailingNewline(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.txt")
	out, err := runWrite(t, map[string]any{"path": path, "content": "x\ny\nz\n"})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "3 行") {
		t.Fatalf("3 行的文件不该算成 4 行: %s", out)
	}
	// 没有尾换行时也要算对
	out, err = runWrite(t, map[string]any{"path": path, "content": "x\ny\nz"})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "3 行") {
		t.Fatalf("无尾换行的 3 行文件行数算错了: %s", out)
	}
}

// 用空内容覆盖已有文件等于清空，不能残留旧内容
func TestWriteOverwriteWithEmptyContent(t *testing.T) {
	path := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(path, []byte("old\n"), 0o644); err != nil {
		t.Fatalf("准备文件失败: %v", err)
	}
	if _, err := runWrite(t, map[string]any{"path": path, "content": ""}); err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	info, err := os.Stat(path)
	if err != nil {
		t.Fatalf("文件应该还在: %v", err)
	}
	if info.Size() != 0 {
		t.Fatalf("覆盖成空内容后大小应为 0: %d", info.Size())
	}
}
