package tool

import (
	"context"
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
	return EditTool{}.Execute(context.Background(), raw)
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

// 改完要回显：改动附近的行、去掉的用 -、加上的用 +，并带上行号
func TestEditEcho(t *testing.T) {
	path := newTempFile(t, "l1\nl2\nl3\nold\nl5\nl6\nl7\n")
	out, err := runEdit(t, map[string]any{
		"path":       path,
		"old_string": "old",
		"new_string": "new",
	})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "第 4 行:") {
		t.Fatalf("回显要标出改的是第几行: %s", out)
	}
	for _, want := range []string{"- old", "+ new"} {
		if !strings.Contains(out, want) {
			t.Fatalf("回显里少了 %q: %s", want, out)
		}
	}
	// 前后各带两行上下文，再远的行不该出现
	for _, want := range []string{"   l2", "   l3", "   l5", "   l6"} {
		if !strings.Contains(out, want) {
			t.Fatalf("回显里少了上下文 %q: %s", want, out)
		}
	}
	for _, notWant := range []string{"l1", "l7"} {
		if strings.Contains(out, notWant) {
			t.Fatalf("上下文只留两行，不该带出 %q: %s", notWant, out)
		}
	}
}

// 只换了行内一部分时，回显的 -/+ 也是完整的行，看着才知道改成了什么样
func TestEditEchoPartialLine(t *testing.T) {
	path := newTempFile(t, "a := value + 1\nb := 2\n")
	out, err := runEdit(t, map[string]any{
		"path":       path,
		"old_string": "value",
		"new_string": "other",
	})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "- a := value + 1") || !strings.Contains(out, "+ a := other + 1") {
		t.Fatalf("行内替换要回显整行: %s", out)
	}
}

// 删除（new_string 为空）也要说清楚是删掉了
func TestEditEchoDeletion(t *testing.T) {
	path := newTempFile(t, "keep\nremove me\n")
	out, err := runEdit(t, map[string]any{
		"path":       path,
		"old_string": "remove me\n",
		"new_string": "",
	})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "- remove me") || !strings.Contains(out, "被删掉") {
		t.Fatalf("删除要有明确回显: %s", out)
	}
}

// 替换多处时回显第一处，并说明一共有几处
func TestEditEchoReplaceAll(t *testing.T) {
	path := newTempFile(t, "x = 1\nx = 2\n")
	out, err := runEdit(t, map[string]any{
		"path":        path,
		"old_string":  "x =",
		"new_string":  "y =",
		"replace_all": true,
	})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "替换 2 处") || !strings.Contains(out, "共替换 2 处") {
		t.Fatalf("替换多处的回显不对: %s", out)
	}
	if !strings.Contains(out, "- x = 1") || !strings.Contains(out, "+ y = 1") {
		t.Fatalf("应回显第一处改动: %s", out)
	}
}
