package tool

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

func runRead(t *testing.T, args map[string]any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("序列化参数失败: %v", err)
	}
	return ReadTool{}.Execute(raw)
}

func writeTempFile(t *testing.T, content string) string {
	t.Helper()
	path := filepath.Join(t.TempDir(), "read.txt")
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("准备文件失败: %v", err)
	}
	return path
}

func TestReadWithLineNumbers(t *testing.T) {
	path := writeTempFile(t, "alpha\nbeta\n")
	out, err := runRead(t, map[string]any{"path": path})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	want := "     1→alpha\n     2→beta\n"
	if out != want {
		t.Fatalf("输出不符合预期:\n%q\nwant:\n%q", out, want)
	}
}

func TestReadLineNumbersOff(t *testing.T) {
	path := writeTempFile(t, "alpha\nbeta\n")
	out, err := runRead(t, map[string]any{"path": path, "line_numbers": false})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if out != "alpha\nbeta\n" {
		t.Fatalf("关掉行号后应输出原文: %q", out)
	}
}

func TestReadOffsetAndLimit(t *testing.T) {
	path := writeTempFile(t, "1\n2\n3\n4\n5\n")
	out, err := runRead(t, map[string]any{"path": path, "offset": 2, "lines": 2})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "     2→2\n") || !strings.Contains(out, "     3→3\n") {
		t.Fatalf("offset/lines 没有生效: %q", out)
	}
	if strings.Contains(out, "     4→") {
		t.Fatalf("超出 lines 的行不该输出: %q", out)
	}
	// 没读完时要提示下一次该用的 offset
	if !strings.Contains(out, "offset=4") {
		t.Fatalf("缺少继续读取的提示: %q", out)
	}
}

func TestReadToEOFNoHint(t *testing.T) {
	path := writeTempFile(t, "a\nb")
	out, err := runRead(t, map[string]any{"path": path})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "     2→b\n") {
		t.Fatalf("最后一行没有输出: %q", out)
	}
	if strings.Contains(out, "还没读完") {
		t.Fatalf("读到文件末尾不该再提示继续读: %q", out)
	}
}

func TestReadEmptyFile(t *testing.T) {
	path := writeTempFile(t, "")
	out, err := runRead(t, map[string]any{"path": path})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "空") {
		t.Fatalf("空文件要有明确提示: %q", out)
	}
}

func TestReadOffsetBeyondFile(t *testing.T) {
	path := writeTempFile(t, "a\nb\n")
	if _, err := runRead(t, map[string]any{"path": path, "offset": 10}); err == nil {
		t.Fatal("offset 超出文件行数时应该报错")
	}
}

func TestReadDirectory(t *testing.T) {
	if _, err := runRead(t, map[string]any{"path": t.TempDir()}); err == nil {
		t.Fatal("读目录应该报错")
	}
}

func TestReadMissingPath(t *testing.T) {
	if _, err := runRead(t, map[string]any{"path": "  "}); err == nil {
		t.Fatal("path 为空应该报错")
	}
}

func TestReadCRLF(t *testing.T) {
	path := writeTempFile(t, "a\r\nb\r\n")
	out, err := runRead(t, map[string]any{"path": path, "line_numbers": false})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if out != "a\nb\n" {
		t.Fatalf("\\r 应该被去掉: %q", out)
	}
}

func TestReadInvalidUTF8(t *testing.T) {
	path := writeTempFile(t, "ok\n\xff\xfe\n")
	if _, err := runRead(t, map[string]any{"path": path}); err == nil {
		t.Fatal("非法 UTF-8 的文本应该报错")
	}
}

// 单行超长时只保留前面一段，并明确告诉模型这行被截断了
func TestReadLongLine(t *testing.T) {
	path := writeTempFile(t, strings.Repeat("a", maxLineBytes+1024)+"\ntail\n")
	out, err := runRead(t, map[string]any{"path": path})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "本行过长") {
		t.Fatalf("超长行应有截断提示: %q", out[:min(len(out), 200)])
	}
	if len(out) > maxLineBytes+4096 {
		t.Fatalf("超长行的输出没有被限制住: %d 字节", len(out))
	}
	if !strings.Contains(out, "     2→tail\n") {
		t.Fatalf("超长行后面的行要保持行号正确: %q", out[max(0, len(out)-200):])
	}
}
