package tool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runEdit(t *testing.T, args map[string]any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("序列化参数失败: %v", err)
	}
	return EditTool{}.Execute(raw)
}

func newTempFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "sample.txt")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("准备文件失败: %v", err)
	}
	return path
}

func TestEditReplaceUnique(t *testing.T) {
	path := newTempFile(t, "a\nb\nc\n")
	out, err := runEdit(t, map[string]any{
		"path":       path,
		"old_string": "b",
		"new_string": "B",
	})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "替换 1 处") {
		t.Fatalf("返回信息不符合预期: %s", out)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取结果失败: %v", err)
	}
	if string(got) != "a\nB\nc\n" {
		t.Fatalf("文件内容不符合预期: %q", got)
	}
}

func TestEditNotUnique(t *testing.T) {
	path := newTempFile(t, "x\nx\n")
	_, err := runEdit(t, map[string]any{
		"path":       path,
		"old_string": "x",
		"new_string": "y",
	})
	if err == nil {
		t.Fatal("old_string 不唯一时应该报错")
	}
}

func TestEditReplaceAll(t *testing.T) {
	path := newTempFile(t, "x\nx\n")
	_, err := runEdit(t, map[string]any{
		"path":        path,
		"old_string":  "x",
		"new_string":  "y",
		"replace_all": true,
	})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	got, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读取结果失败: %v", err)
	}
	if string(got) != "y\ny\n" {
		t.Fatalf("文件内容不符合预期: %q", got)
	}
}

func TestEditOldStringNotFound(t *testing.T) {
	path := newTempFile(t, "abc\n")
	_, err := runEdit(t, map[string]any{
		"path":       path,
		"old_string": "zzz",
		"new_string": "y",
	})
	if err == nil {
		t.Fatal("old_string 不存在时应该报错")
	}
}

func TestEditSameString(t *testing.T) {
	path := newTempFile(t, "abc\n")
	_, err := runEdit(t, map[string]any{
		"path":       path,
		"old_string": "abc",
		"new_string": "abc",
	})
	if err == nil {
		t.Fatal("old_string 与 new_string 相同时应该报错")
	}
}
