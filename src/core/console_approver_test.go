package core

import (
	"bufio"
	"bytes"
	"strings"
	"testing"

	"hamusuta/agent-dev/src/domain"
)

// 控制台里的回答：回车算允许，a 是本回合都允许，n 拒绝，别的当没看懂
func TestParseApprovalInput(t *testing.T) {
	allow := domain.ApprovalAnswer{Allowed: true}
	always := domain.ApprovalAnswer{Allowed: true, Always: true}
	deny := domain.ApprovalAnswer{}

	cases := map[string]domain.ApprovalAnswer{
		"":       allow,
		"\n":     allow,
		"y":      allow,
		"Y\r\n":  allow,
		" yes ":  allow,
		"是":      allow,
		"1":      allow,
		"a":      always,
		"always": always,
		"2":      always,
		"总是":     always,
		"n":      deny,
		"no":     deny,
		"3":      deny,
		"拒绝":     deny,
	}
	for input, want := range cases {
		got, ok := parseApprovalInput(input)
		if !ok {
			t.Fatalf("%q 应该能看懂", input)
		}
		if got != want {
			t.Fatalf("%q 解析成了 %+v，应该是 %+v", input, got, want)
		}
	}

	for _, bad := range []string{"maybe", "y n", "确定"} {
		if _, ok := parseApprovalInput(bad); ok {
			t.Fatalf("%q 不该被当成有效回答", bad)
		}
	}
}

// newTestApprover 造一个假装连着终端的确认器，输入输出都走 buffer
func newTestApprover(input string) (*consoleApprover, *bytes.Buffer) {
	out := &bytes.Buffer{}
	return &consoleApprover{in: bufio.NewReader(strings.NewReader(input)), out: out, interactive: true}, out
}

// 确认框要把要跑的命令、补充说明和选项都摊开
func TestConsoleApprovalView(t *testing.T) {
	a, out := newTestApprover("n\n")
	req := domain.ApprovalRequest{
		Tool:   "Bash",
		Kind:   domain.ApprovalRunCommand,
		Target: "go test ./...",
		Detail: "跑测试 · 目录 src",
	}
	a.ask(req)

	text := out.String()
	for _, want := range []string{
		"[确认] Bash", "执行命令", "$ go test ./...", "跑测试 · 目录 src",
		"[y] 允许一次", "[a] 本回合都允许 Bash", "[n] 拒绝",
	} {
		if !strings.Contains(text, want) {
			t.Fatalf("确认输出里少了 %q:\n%s", want, text)
		}
	}

	// 改文件的确认框不用命令的样子显示
	a, out = newTestApprover("n\n")
	a.ask(domain.ApprovalRequest{
		Tool: "Edit", Kind: domain.ApprovalWriteFile,
		Target: "src/cli/ui.go", Detail: "3 行换成 5 行",
	})
	text = out.String()
	if strings.Contains(text, "$ ") {
		t.Fatalf("改文件不该用 $ 显示:\n%s", text)
	}
	for _, want := range []string{"改动文件", "src/cli/ui.go", "3 行换成 5 行"} {
		if !strings.Contains(text, want) {
			t.Fatalf("确认输出里少了 %q:\n%s", want, text)
		}
	}

	// 多行命令要按原样摊开
	a, out = newTestApprover("n\n")
	a.ask(domain.ApprovalRequest{
		Tool: "Bash", Kind: domain.ApprovalRunCommand,
		Target: "cd src\n  go build ./...",
	})
	if text := out.String(); !strings.Contains(text, "    go build ./...") {
		t.Fatalf("多行命令要保留缩进:\n%s", text)
	}
}

// 敲错一次再敲对：要重问，最后按第二次的回答走
func TestConsoleApprovalRetries(t *testing.T) {
	a, out := newTestApprover("随便打点什么\ny\n")
	if ans := a.ask(domain.ApprovalRequest{Tool: "Write", Target: "a.go"}); !ans.Allowed {
		t.Fatalf("第二次回答 y 应该放行: %+v", ans)
	}
	if text := out.String(); !strings.Contains(text, "没看懂") {
		t.Fatalf("敲错时应该提示一遍:\n%s", text)
	}
	if n := strings.Count(out.String(), "> "); n != 2 {
		t.Fatalf("应该问两遍，实际 %d 遍:\n%s", n, out.String())
	}
}

// 一直看不懂就按拒绝处理，别让 Run 停在这儿
func TestConsoleApprovalGivesUp(t *testing.T) {
	a, out := newTestApprover("aaa\nbbb\nccc\ny\n")
	if ans := a.ask(domain.ApprovalRequest{Tool: "Write", Target: "a.go"}); ans.Allowed {
		t.Fatalf("连着几次都看不懂时应该按拒绝处理: %+v", ans)
	}
	if text := out.String(); !strings.Contains(text, "连着几遍都没看懂") {
		t.Fatalf("应该说明放弃的原因:\n%s", text)
	}
}

// stdin 读不到东西（EOF / 被重定向）时按拒绝处理，不能卡住
func TestConsoleApprovalEOF(t *testing.T) {
	a, out := newTestApprover("")
	if ans := a.ask(domain.ApprovalRequest{Tool: "Write", Target: "a.go"}); ans.Allowed {
		t.Fatalf("没读到回答应该按拒绝处理: %+v", ans)
	}
	if text := out.String(); !strings.Contains(text, "已按拒绝处理") {
		t.Fatalf("应该说明按拒绝处理:\n%s", text)
	}
}

// 不是终端就问不了，直接拒绝并说清楚怎么办
func TestConsoleApprovalNonInteractive(t *testing.T) {
	out := &bytes.Buffer{}
	// 不设 interactive：newConsoleApprover 对非 *os.File 的输入也判定成非交互
	a := newConsoleApprover(strings.NewReader("y\n"), out)
	if a.interactive {
		t.Fatal("bytes.Buffer 不该被当成终端")
	}
	if ans := a.ask(domain.ApprovalRequest{Tool: "Write", Target: "a.go"}); ans.Allowed {
		t.Fatalf("非交互时应该按拒绝处理: %+v", ans)
	}
	if text := out.String(); !strings.Contains(text, "没法确认") || !strings.Contains(text, "/mode full") {
		t.Fatalf("应该说明怎么放开:\n%s", text)
	}
}

// 回答完要有一行"已允许/已拒绝"的反馈，打在确认框下面
func TestAskOnConsoleFeedback(t *testing.T) {
	a, out := newTestApprover("a\n")
	if ans := askOnConsole(a, domain.ApprovalRequest{Tool: "Bash", Target: "ls"}); !ans.Always {
		t.Fatalf("a 应该是本回合都允许: %+v", ans)
	}
	if text := out.String(); !strings.Contains(text, "本回合不再问 Bash") {
		t.Fatalf("缺少允许后的反馈:\n%s", text)
	}
}
