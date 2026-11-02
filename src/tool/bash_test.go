package tool

import (
	"context"
	"encoding/json"
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"time"
	"unicode/utf8"
)

func runBash(t *testing.T, args map[string]any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("序列化参数失败: %v", err)
	}
	return BashTool{}.Execute(context.Background(), raw)
}

func TestBashEcho(t *testing.T) {
	out, err := runBash(t, map[string]any{"command": "echo hello"})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "exit_code: 0") {
		t.Fatalf("退出码应为 0: %s", out)
	}
	if !strings.Contains(out, "hello") {
		t.Fatalf("缺少命令输出: %s", out)
	}
}

func TestBashNonZeroExit(t *testing.T) {
	out, err := runBash(t, map[string]any{"command": "exit 3"})
	if err != nil {
		t.Fatalf("非零退出码不该当成工具错误: %v", err)
	}
	if !strings.Contains(out, "exit_code: 3") {
		t.Fatalf("退出码应为 3: %s", out)
	}
}

func TestBashEmptyCommand(t *testing.T) {
	if _, err := runBash(t, map[string]any{"command": "   "}); err == nil {
		t.Fatal("command 为空应该报错")
	}
}

func TestBashMissingWorkdir(t *testing.T) {
	_, err := runBash(t, map[string]any{
		"command": "echo x",
		"workdir": filepath.Join(t.TempDir(), "不存在"),
	})
	if err == nil {
		t.Fatal("工作目录不存在时应该报错")
	}
	if !strings.Contains(err.Error(), "工作目录") {
		t.Fatalf("错误信息应点明工作目录: %v", err)
	}
}

// 重定向在 pwsh / cmd / sh 下都能用，正好可以验证 workdir 生效
func TestBashWorkdir(t *testing.T) {
	dir := t.TempDir()
	out, err := runBash(t, map[string]any{
		"command": "echo x > out.txt",
		"workdir": dir,
	})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "exit_code: 0") {
		t.Fatalf("命令没执行成功: %s", out)
	}
	if _, err := os.Stat(filepath.Join(dir, "out.txt")); err != nil {
		t.Fatalf("文件应该写在 workdir 里: %v", err)
	}
}

func TestBashTimeout(t *testing.T) {
	if runtime.GOOS == "windows" && activeShell().Name == "cmd" {
		t.Skip("cmd 下没有统一的 sleep 命令，跳过")
	}
	command := "sleep 5"
	if runtime.GOOS == "windows" {
		command = "Start-Sleep -Seconds 5"
	}
	start := time.Now()
	out, err := runBash(t, map[string]any{"command": command, "timeout_seconds": 1})
	if err != nil {
		t.Fatalf("超时不该当成工具错误: %v", err)
	}
	if !strings.Contains(out, "超时") {
		t.Fatalf("应提示超时: %s", out)
	}
	if elapsed := time.Since(start); elapsed > 4*time.Second {
		t.Fatalf("超时没有生效，耗时 %s", elapsed)
	}
}

func TestBashNoArgs(t *testing.T) {
	if _, err := (BashTool{}).Execute(context.Background(), nil); err == nil {
		t.Fatal("缺少参数应该报错")
	}
}

// 父 Context 被取消（用户按 Ctrl+C）时，命令要立刻停下来，不用等到超时
func TestBashCancelStopsCommand(t *testing.T) {
	if runtime.GOOS == "windows" && activeShell().Name == "cmd" {
		t.Skip("cmd 下没有统一的 sleep 命令，跳过")
	}
	command := "sleep 30"
	if runtime.GOOS == "windows" {
		command = "Start-Sleep -Seconds 30"
	}
	raw, err := json.Marshal(map[string]any{"command": command})
	if err != nil {
		t.Fatalf("序列化参数失败: %v", err)
	}

	ctx, cancel := context.WithCancel(context.Background())
	go func() {
		time.Sleep(300 * time.Millisecond)
		cancel()
	}()
	defer cancel()

	start := time.Now()
	out, err := (BashTool{}).Execute(ctx, raw)
	if err != nil {
		t.Fatalf("中断不该当成工具错误: %v", err)
	}
	if !strings.Contains(out, "中断") {
		t.Fatalf("应提示被中断: %s", out)
	}
	if elapsed := time.Since(start); elapsed > 5*time.Second {
		t.Fatalf("中断没有生效，耗时 %s", elapsed)
	}
}

func TestOutputBufferKeepsHeadAndTail(t *testing.T) {
	buf := newOutputBuffer(10)
	if _, err := buf.Write([]byte("0123456789ABCDEF")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	out := buf.String()
	if !strings.HasPrefix(out, "01234") {
		t.Fatalf("应该保留开头: %q", out)
	}
	if !strings.HasSuffix(out, "BCDEF") {
		t.Fatalf("应该保留结尾: %q", out)
	}
	if !strings.Contains(out, "省略 6 字节") {
		t.Fatalf("应该说明省略了多少字节: %q", out)
	}
}

func TestOutputBufferSmallOutputVerbatim(t *testing.T) {
	buf := newOutputBuffer(64)
	if _, err := buf.Write([]byte("hello\n")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if out := buf.String(); out != "hello\n" {
		t.Fatalf("没超限时不该改动输出: %q", out)
	}
}

// 截断发生在多字节字符中间时，输出必须仍然是合法 UTF-8
func TestOutputBufferDoesNotSplitRunes(t *testing.T) {
	buf := newOutputBuffer(6)
	if _, err := buf.Write([]byte("中文内容测试")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	out := buf.String()
	if !utf8.ValidString(out) {
		t.Fatalf("截断后不是合法 UTF-8: %q", out)
	}
	if !strings.Contains(out, "省略") {
		t.Fatalf("超限时应说明省略: %q", out)
	}
}
