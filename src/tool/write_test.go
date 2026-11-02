package tool

import (
	"context"
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
	return WriteTool{}.Execute(context.Background(), raw)
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
	t.Chdir(t.TempDir()) // 覆盖会留备份，别写进源码目录
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
	t.Chdir(t.TempDir()) // 第二次写是覆盖，别把备份写进源码目录
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
	t.Chdir(t.TempDir()) // 覆盖会留备份，别写进源码目录
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

// 覆盖已有文件前先把原内容备份到 .agent/backups 下，结果里要说出备份去哪儿了
func TestWriteBacksUpOverwrittenFile(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)

	path := filepath.Join(root, "sub", "a.txt")
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("创建目录失败: %v", err)
	}
	if err := os.WriteFile(path, []byte("原来的内容\n"), 0o644); err != nil {
		t.Fatalf("准备文件失败: %v", err)
	}

	out, err := runWrite(t, map[string]any{"path": path, "content": "新内容\n"})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "备份到") {
		t.Fatalf("覆盖时要说明备份位置: %s", out)
	}

	backups, err := filepath.Glob(filepath.Join(root, ".agent", "backups", "**", "*"))
	if err != nil {
		t.Fatalf("找备份文件失败: %v", err)
	}
	var files []string
	for _, b := range backups {
		if info, err := os.Stat(b); err == nil && info.Mode().IsRegular() {
			files = append(files, b)
		}
	}
	if len(files) != 1 {
		t.Fatalf("应该有 1 份备份，实际 %d 份: %v", len(files), files)
	}
	got, err := os.ReadFile(files[0])
	if err != nil {
		t.Fatalf("读取备份失败: %v", err)
	}
	if string(got) != "原来的内容\n" {
		t.Fatalf("备份内容应该是覆盖前的原文: %q", got)
	}
	// 备份按相对路径存，能看出是哪个文件被覆盖的
	if !strings.Contains(filepath.ToSlash(files[0]), "sub/a.txt") {
		t.Fatalf("备份路径里应带上被覆盖文件的相对路径: %s", files[0])
	}
}

// 新建文件时没有原内容可备份，不该凭空造出备份目录
func TestWriteNoBackupForNewFile(t *testing.T) {
	root := t.TempDir()
	t.Chdir(root)

	path := filepath.Join(root, "new.txt")
	out, err := runWrite(t, map[string]any{"path": path, "content": "x\n"})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if strings.Contains(out, "备份") {
		t.Fatalf("新建文件不该提备份: %s", out)
	}
	if _, err := os.Stat(filepath.Join(root, ".agent")); err == nil {
		t.Fatal("新建文件不该创建备份目录")
	}
}
