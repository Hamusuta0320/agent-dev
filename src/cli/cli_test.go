package main

import (
	"regexp"
	"strings"
	"testing"
	"time"

	"hamusuta/agent-dev/src/config"
	"hamusuta/agent-dev/src/domain"

	tea "charm.land/bubbletea/v2"
)

// ansiRe 匹配着色的转义序列，断言文案前要先剥掉
var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

func TestParseCommand(t *testing.T) {
	cases := map[string]command{
		"/help":      cmdHelp,
		"/new":       cmdNew,
		"/session":   cmdSession,
		"/exit":      cmdExit,
		"/quit":      cmdExit,
		"exit":       cmdExit,
		"quit":       cmdExit,
		"  /help  ":  cmdHelp,
		"帮我改个 bug":   cmdNone,
		"/unknown":   cmdNone,
		"":           cmdNone,
		"/new 额外的参数": cmdNew,
	}
	for input, want := range cases {
		if got := parseCommand(input); got != want {
			t.Fatalf("%q 解析成了 %v，应该是 %v", input, got, want)
		}
	}
}

func TestHeadLines(t *testing.T) {
	got, cut := headLines("a\nb\nc", 2)
	if got != "a\nb" || !cut {
		t.Fatalf("截断错了: %q %v", got, cut)
	}
	got, cut = headLines("a\nb", 2)
	if got != "a\nb" || cut {
		t.Fatalf("够短就不该截断: %q %v", got, cut)
	}
	got, cut = headLines("a\nb\nc", 0)
	if got != "a\nb\nc" || cut {
		t.Fatalf("0 表示不截断: %q %v", got, cut)
	}
}

// 底部预览要显示最新的几行，末尾的空行不算
func TestTailLines(t *testing.T) {
	if got := tailLines("1\n2\n3\n4", 2); got != "3\n4" {
		t.Fatalf("尾部取错了: %q", got)
	}
	if got := tailLines("1\n2\n", 5); got != "1\n2" {
		t.Fatalf("行数不够时不该改内容: %q", got)
	}
	if got := tailLines("", 3); got != "" {
		t.Fatalf("空文本应该返回空: %q", got)
	}
}

// 工具参数这种 JSON 要压成一行再截断
func TestFirstLines(t *testing.T) {
	if got := firstLines("{\n  \"command\": \"ls\"\n}", 0); got != `{ "command": "ls" }` {
		t.Fatalf("换行没压掉: %q", got)
	}
	got := firstLines(strings.Repeat("很", 10), 3)
	if got != "很很很…" {
		t.Fatalf("截断错了: %q", got)
	}
}

// 工具结果和思考内容都以换行结尾，渲染时不能多出空行
func TestBlockNoTrailingBlankLine(t *testing.T) {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	m.width = 80

	for _, text := range []string{
		"exit_code: 0\nmain.go\nsub/\n",
		"先看目录\nls 一下\n\n",
	} {
		lines := m.block("", toolResStyle, text, 0)
		last := lines[len(lines)-1]
		if strings.TrimSpace(last) == "" {
			t.Fatalf("末尾多了空行 %q，原文 %q", lines, text)
		}
	}

	// 内容中间的空行是模型输出的一部分，要留着
	lines := m.block("", toolResStyle, "a\n\nb\n", 0)
	if strings.Join(lines, "|") != "  a|  |  b" {
		t.Fatalf("中间的空行不该被吃掉: %q", lines)
	}

	// 纯空白还是当没有内容
	if lines := m.block("", toolResStyle, "\n\n", 0); lines != nil {
		t.Fatalf("空白不该渲染: %q", lines)
	}
}

// Enter 发送，Shift+Enter 换行
func TestEnterKeys(t *testing.T) {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	m.input.SetValue("第一行")

	// Shift+Enter 只在输入框里插一个换行，不能发出去
	got, _ := m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter, Mod: tea.ModShift}))
	shifted, ok := got.(model)
	if !ok {
		t.Fatal("handleKey 应该返回 model")
	}
	if v := shifted.input.Value(); v != "第一行\n" {
		t.Fatalf("Shift+Enter 应该换行而不是发送: %q", v)
	}

	// 光按 Enter 是发送：输入框被清空
	got, _ = shifted.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	sent, ok := got.(model)
	if !ok {
		t.Fatal("handleKey 应该返回 model")
	}
	if v := sent.input.Value(); v != "" {
		t.Fatalf("Enter 应该发送并清空输入框: %q", v)
	}
}

// 工具调用的标题行最前面是圆圈：成功绿色、失败红色，耗时和原因都跟在同一行
func TestToolResultLines(t *testing.T) {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	m.width = 80
	call := domain.ToolCall{Name: "Bash", Arguments: `{"intent":"跑测试","command":"go test ./..."}`}
	m.pending = m.toolCallLines(call)

	ok := m.toolResultLines(domain.Event{
		Kind:    domain.EventToolResult,
		Call:    call,
		Status:  domain.ToolStatusSuccess,
		Elapsed: 1200 * time.Millisecond,
	})
	if len(ok) != 2 || stripANSI(ok[1]) != "  $ go test ./..." {
		t.Fatalf("成功应该只有标题行和命令行: %q", ok)
	}
	if !strings.HasPrefix(ok[0], doneMark(domain.ToolStatusSuccess)+" ") ||
		!strings.HasPrefix(stripANSI(ok[0]), "● Bash") {
		t.Fatalf("标题行前面应该是绿色圆圈: %q", ok[0])
	}
	for _, want := range []string{"调用成功", "耗时 1.20s"} {
		if !strings.Contains(stripANSI(ok[0]), want) {
			t.Fatalf("成功状态少了 %q: %q", want, ok[0])
		}
	}

	fail := m.toolResultLines(domain.Event{
		Kind:    domain.EventToolResult,
		Call:    call,
		Status:  domain.ToolStatusFailed,
		Err:     "打开文件失败\n: a.go",
		Elapsed: 3 * time.Millisecond,
	})
	if !strings.HasPrefix(fail[0], doneMark(domain.ToolStatusFailed)+" ") {
		t.Fatalf("标题行前面应该是红色圆圈: %q", fail[0])
	}
	for _, want := range []string{"调用失败", "耗时 3ms", "原因: 打开文件失败 : a.go"} {
		if !strings.Contains(stripANSI(fail[0]), want) {
			t.Fatalf("失败状态少了 %q: %q", want, fail[0])
		}
	}

	// 失败没带原因时也不能空着，长原因要压成一行
	// （pending 清掉，走“没有调用中的行，重新渲染”的兜底路径）
	m.pending = nil
	noReason := stripANSI(m.toolResultLines(domain.Event{
		Call:   domain.ToolCall{Name: "Read"},
		Status: domain.ToolStatusFailed,
	})[0])
	if !strings.Contains(noReason, "未提供失败原因") {
		t.Fatalf("缺少原因的兜底文案: %q", noReason)
	}
	long := m.toolResultLines(domain.Event{
		Call:   domain.ToolCall{Name: "Read"},
		Status: domain.ToolStatusFailed,
		Err:    strings.Repeat("很", toolErrRunes+50),
	})
	if len(long) != 1 || !strings.Contains(stripANSI(long[0]), "…") {
		t.Fatalf("长原因应该压成一行并截断: %q", long)
	}
}

// 调用中的圆圈是转圈，跟在工具名前面；没工具在跑时还是原来的状态行
func TestRunningViewToolMark(t *testing.T) {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	m.width = 80
	m.state = stateRunning
	m.status = "执行 Read…"

	m.pending = m.toolCallLines(domain.ToolCall{Name: "Read", Arguments: `{"file_path":"a.go"}`})
	head := strings.Split(stripANSI(m.runningView()), "\n")[0]
	if want := stripANSI(m.spinMark()) + " Read"; !strings.HasPrefix(head, want) {
		t.Fatalf("调用中应该在工具前面转圈，想要 %q，实际 %q", want, head)
	}

	m.pending = nil
	if got := stripANSI(m.runningView()); !strings.Contains(got, "执行 Read…") {
		t.Fatalf("没有工具在跑时不该贴调用行: %q", got)
	}
}

// 圆圈只贴在标题行最前面，原来的调用内容不能动
func TestWithMark(t *testing.T) {
	lines := []string{"Bash", "  $ ls"}
	got := withMark(lines, "●")
	if len(got) != 2 || got[0] != "● Bash" || got[1] != "  $ ls" {
		t.Fatalf("圆圈没贴对: %q", got)
	}
	if lines[0] != "Bash" {
		t.Fatalf("不该改动传进来的切片: %q", lines)
	}
	if got := withMark(lines, ""); got[0] != "Bash" {
		t.Fatalf("没有圆圈时不该加内容: %q", got)
	}
}

// stripANSI 去掉着色用的转义序列，测试只关心文案本身
func stripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

// Bash 调用要能显示意图和完整命令，多行命令不截断
func TestBashCallLines(t *testing.T) {
	call := domain.ToolCall{
		Name:      "Bash",
		Arguments: `{"intent":"跑测试","command":"go test ./...\ngo vet ./..."}`,
	}
	lines, ok := bashCallLines(call)
	if !ok {
		t.Fatal("Bash 调用应该走专门的分支")
	}
	got := strings.Join(lines, "\n")
	for _, want := range []string{"Bash", "跑测试", "go test ./...", "go vet ./..."} {
		if !strings.Contains(got, want) {
			t.Fatalf("少了 %q:\n%s", want, got)
		}
	}

	// 长命令不能被截断
	long := strings.Repeat("a", 300)
	lines, _ = bashCallLines(domain.ToolCall{Name: "Bash", Arguments: `{"command":"` + long + `"}`})
	if !strings.Contains(strings.Join(lines, "\n"), long) {
		t.Fatal("命令被截断了")
	}

	// 参数坏的、或者不是 Bash，就不要走这个分支
	if _, ok := bashCallLines(domain.ToolCall{Name: "Read", Arguments: `{"file_path":"a"}`}); ok {
		t.Fatal("Read 不该走 Bash 分支")
	}
	if _, ok := bashCallLines(domain.ToolCall{Name: "Bash", Arguments: `not json`}); ok {
		t.Fatal("坏参数应该退回摘要")
	}
}
