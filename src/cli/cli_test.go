package main

import (
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"hamusuta/agent-dev/src/config"
	"hamusuta/agent-dev/src/domain"
	"hamusuta/agent-dev/src/tool"

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
	m.pending = []pendingCall{{id: call.Id, lines: m.toolCallLines(call)}}

	ok := m.toolResultLines(domain.Event{
		Kind:    domain.EventToolResult,
		Call:    call,
		Status:  domain.ToolStatusSuccess,
		Elapsed: 1200 * time.Millisecond,
	}, m.takePending(call))
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
	}, m.takePending(call))
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
	}, nil)[0])
	if !strings.Contains(noReason, "未提供失败原因") {
		t.Fatalf("缺少原因的兜底文案: %q", noReason)
	}
	long := m.toolResultLines(domain.Event{
		Call:   domain.ToolCall{Name: "Read"},
		Status: domain.ToolStatusFailed,
		Err:    strings.Repeat("很", toolErrRunes+50),
	}, nil)
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

	m.pending = []pendingCall{{lines: m.toolCallLines(domain.ToolCall{Name: "Read", Arguments: `{"file_path":"a.go"}`})}}
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

// 输入以 / 开头时列出能补全的命令，打全了就不再提示
func TestSuggestCommands(t *testing.T) {
	cases := map[string][]string{
		"/":     {"/help", "/new", "/session", "/config", "/mode", "/exit"},
		"/c":    {"/config"},
		"/se":   {"/session"},
		"/help": nil, // 已经是完整命令
		"/zzz":  nil,
		"":      nil,
		"hello": nil,
		"/new ": nil, // 开始写参数了，不再补全
		" /he":  {"/help"},
		"/HELP": nil,
	}
	for input, want := range cases {
		var got []string
		for _, spec := range suggestCommands(input) {
			got = append(got, spec.name)
		}
		if strings.Join(got, ",") != strings.Join(want, ",") {
			t.Fatalf("%q 的候选是 %v，应该是 %v", input, got, want)
		}
	}
}

// Tab 补全成选中的命令，↑↓ 只换选中项、不动输入框
func TestCompleteCommand(t *testing.T) {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))

	m.input.SetValue("/co")
	got, _ := m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	if v := got.(model).input.Value(); v != "/config " {
		t.Fatalf("Tab 应该补全成 /config，实际 %q", v)
	}

	m.input.SetValue("/")
	got, _ = m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	down := got.(model)
	if v := down.input.Value(); v != "/" || down.suggestIdx != 1 {
		t.Fatalf("↓ 应该只改选中项: %q %d", v, down.suggestIdx)
	}
	got, _ = down.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	if idx := got.(model).suggestIdx; idx != 0 {
		t.Fatalf("↑ 应该退回第一个: %d", idx)
	}
	// 选中项越界时按第一个算，别把列表渲染没了
	got, _ = got.(model).handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	if v := got.(model).input.Value(); v != "/help " {
		t.Fatalf("越界下标应该按第一项补全，实际 %q", v)
	}

	// 没候选时 Tab 不该改输入框
	m.input.SetValue("读一下 main.go")
	got, _ = m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab}))
	if v := got.(model).input.Value(); v != "读一下 main.go" {
		t.Fatalf("普通输入不该被补全: %q", v)
	}

	// Shift+Tab 也算 Tab，同样归我们管，不能漏给输入框
	got, _ = m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyTab, Mod: tea.ModShift}))
	if v := got.(model).input.Value(); v != "读一下 main.go" {
		t.Fatalf("Shift+Tab 不该改输入框: %q", v)
	}
}

// VS Code 集成终端里 Tab 会被拿去移动焦点，键到不了程序，得有备选键
func TestCompleteCommandAlternateKeys(t *testing.T) {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))

	// Ctrl+Space
	m.input.SetValue("/co")
	got, _ := m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeySpace, Mod: tea.ModCtrl}))
	if v := got.(model).input.Value(); v != "/config " {
		t.Fatalf("Ctrl+Space 应该补全成 /config，实际 %q", v)
	}

	// 有的终端把 Tab 解码成 ctrl+i（和 Tab 是同一个字节）
	m.input.SetValue("/co")
	got, _ = m.handleKey(tea.KeyPressMsg(tea.Key{Code: 'i', Mod: tea.ModCtrl}))
	if v := got.(model).input.Value(); v != "/config " {
		t.Fatalf("ctrl+i 应该当成 Tab 补全，实际 %q", v)
	}

	// 光标在行尾时 → 接受补全
	m.input.SetValue("/co")
	got, _ = m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}))
	if v := got.(model).input.Value(); v != "/config " {
		t.Fatalf("行尾 → 应该补全成 /config，实际 %q", v)
	}

	// 光标不在行尾时 → 还是正常右移，别乱补全
	m.input.SetValue("/co")
	got, _ = m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft}))
	if v := got.(model).input.Value(); v != "/co" {
		t.Fatalf("← 不该改输入框: %q", v)
	}
	got, _ = got.(model).handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyRight}))
	if v := got.(model).input.Value(); v != "/co" {
		t.Fatalf("光标在中间时 → 不该补全: %q", v)
	}
}

// 命令没打完时按 Enter 先补全，别把半截 "/co" 发给模型
func TestEnterCompletesPartialCommand(t *testing.T) {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	m.input.SetValue("/co")

	got, _ := m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	after := got.(model)
	if v := after.input.Value(); v != "/config " {
		t.Fatalf("Enter 应该先补全，实际 %q", v)
	}
	if after.state != stateIdle {
		t.Fatalf("补全不该开始跑任务，状态 %v", after.state)
	}
}

// 补全候选要跟选中项同步，高亮只打在选中那一行
func TestCompletionLines(t *testing.T) {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	m.input.SetValue("/")
	m.suggestIdx = 2
	lines := m.completionLines()
	if len(lines) != len(commandSpecs) {
		t.Fatalf("候选应该全列出来: %q", lines)
	}
	for i, line := range lines {
		hasMark := strings.HasPrefix(stripANSI(line), "▸ ")
		if hasMark != (i == 2) {
			t.Fatalf("第 %d 行的高亮不对: %q", i, line)
		}
	}

	// 不是命令输入时没有候选
	m.input.SetValue("你好")
	if lines := m.completionLines(); lines != nil {
		t.Fatalf("普通输入不该有候选: %q", lines)
	}
}

// /config 打开表单，填完 Ctrl+S 写回配置文件
func TestConfigFormSave(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	m.cfgPath = path

	// /config 进入表单
	m.input.SetValue("/config")
	got, _ := m.submit()
	if s := got.(model).state; s != stateConfig {
		t.Fatalf("/config 应该打开表单，状态 %v", s)
	}

	// 少填一项就不该写文件
	form := got.(model)
	form.form.inputs[0].SetValue("https://api.deepseek.com")
	form.form.inputs[1].SetValue("sk-test")
	got, _ = form.saveConfig()
	if s := got.(model).state; s != stateConfig {
		t.Fatalf("校验没过不该退出表单，状态 %v", s)
	}
	if _, err := os.Stat(path); err == nil {
		t.Fatal("校验没过不该写文件")
	}

	// 填全了才保存，并且回到输入状态
	form = got.(model)
	form.form.inputs[2].SetValue("deepseek-chat")
	got, _ = form.saveConfig()
	saved := got.(model)
	if saved.state != stateIdle {
		t.Fatalf("保存成功应该回到输入状态，状态 %v", saved.state)
	}
	if dc := deepSeekConfig(saved.cfg); dc.Model != "deepseek-chat" || dc.ApiKey != "sk-test" {
		t.Fatalf("内存里的配置没更新: %+v", dc)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("配置文件没写出来: %v", err)
	}
	if !strings.Contains(string(data), "deepseek-chat") || !strings.Contains(string(data), "Provider") {
		t.Fatalf("文件内容不对:\n%s", data)
	}
}

// 密钥在屏幕上要打码，太短就全打掉
func TestMaskSecret(t *testing.T) {
	if got := maskSecret("sk-6e5c245d4e9748eab48990e60651ee3e"); got != "sk-6…ee3e" {
		t.Fatalf("打码结果不对: %q", got)
	}
	if got := maskSecret("sk-123"); got != "••••••" {
		t.Fatalf("短密钥应该全打码: %q", got)
	}
	if got := maskSecret(""); got != "" {
		t.Fatalf("空值应该返回空: %q", got)
	}
}

// stripANSI 去掉着色用的转义序列，测试只关心文案本身
func stripANSI(s string) string {
	return ansiRe.ReplaceAllString(s, "")
}

// Bash 调用要能显示意图和完整命令，多行命令不截断
func TestBashCallLines(t *testing.T) {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	m.width = 80

	call := domain.ToolCall{
		Name:      "Bash",
		Arguments: `{"intent":"跑测试","command":"go test ./...\ngo vet ./..."}`,
	}
	lines := m.toolCallLines(call)
	got := stripANSI(strings.Join(lines, "\n"))
	for _, want := range []string{"Bash", "跑测试", "go test ./...", "go vet ./..."} {
		if !strings.Contains(got, want) {
			t.Fatalf("少了 %q:\n%s", want, got)
		}
	}

	// 长命令不能被截断
	long := strings.Repeat("a", 300)
	lines = m.toolCallLines(domain.ToolCall{Name: "Bash", Arguments: `{"command":"` + long + `"}`})
	if !strings.Contains(strings.Join(lines, "\n"), long) {
		t.Fatal("命令被截断了")
	}

	// 参数坏了也只剩标题行，不能崩
	if lines := m.toolCallLines(domain.ToolCall{Name: "Bash", Arguments: `not json`}); len(lines) != 1 {
		t.Fatalf("坏参数应该只留标题行: %q", lines)
	}

	// 后台命令不会等结果，标题上要标出来
	lines = m.toolCallLines(domain.ToolCall{
		Name:      "Bash",
		Arguments: `{"intent":"起个 dev server","command":"npm run dev","background":true}`,
	})
	if !strings.Contains(stripANSI(lines[0]), "后台") {
		t.Fatalf("后台调用要标出来: %q", lines[0])
	}
}

// 每个工具都要显示意图，并按各自的参数补一行细节
func TestToolCallLinesShowIntent(t *testing.T) {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	m.width = 120

	cases := []struct {
		name  string
		args  string
		wants []string
	}{
		{"Read", `{"intent":"看看登录逻辑","path":"auth/login.go","offset":10,"lines":20}`, []string{"Read", "看看登录逻辑", "auth/login.go", "第 10 行起", "最多 20 行"}},
		{"Write", `{"intent":"补一个测试","path":"auth/login_test.go","content":"package auth\n"}`, []string{"Write", "补一个测试", "auth/login_test.go", "写入 13 字节"}},
		{"Edit", `{"intent":"修掉越界","path":"auth/login.go","old_string":"a","new_string":"b","replace_all":true}`, []string{"Edit", "修掉越界", "auth/login.go", "替换全部匹配"}},
		{"Grep", `{"intent":"找一下这个函数","pattern":"func Login","glob":"*.go","case_insensitive":true}`, []string{"Grep", "找一下这个函数", "func Login", "*.go", "忽略大小写"}},
		{"Glob", `{"intent":"看看有哪些测试","pattern":"**/*_test.go"}`, []string{"Glob", "看看有哪些测试", "**/*_test.go"}},
		{"Task", `{"intent":"等等构建","action":"wait","task_id":"task_1"}`, []string{"Task", "等等构建", "wait task_1"}},
	}
	for _, c := range cases {
		lines := m.toolCallLines(domain.ToolCall{Name: c.name, Arguments: c.args})
		got := stripANSI(strings.Join(lines, "\n"))
		for _, want := range c.wants {
			if !strings.Contains(got, want) {
				t.Fatalf("%s 的调用行里少了 %q:\n%s", c.name, want, got)
			}
		}
		// 标题行最前面是工具名，后面紧跟意图
		if !strings.HasPrefix(stripANSI(lines[0]), c.name) {
			t.Fatalf("%s 的标题行不对: %q", c.name, lines[0])
		}
	}

	// 没传 intent（老会话里的调用）也要能显示，不能空着
	lines := m.toolCallLines(domain.ToolCall{Name: "Read", Arguments: `{"path":"a.go"}`})
	if !strings.Contains(stripANSI(strings.Join(lines, "\n")), "a.go") {
		t.Fatalf("没给 intent 时也要显示细节: %q", lines)
	}

	// 认不出的工具退回参数摘要
	lines = m.toolCallLines(domain.ToolCall{Name: "Future", Arguments: `{"x":1}`})
	if len(lines) != 2 || !strings.Contains(stripANSI(lines[1]), `"x":1`) {
		t.Fatalf("未知工具应该退回参数摘要: %q", lines)
	}
}

// 每个工具的 schema 里都要有 intent，而且模型不给就算缺参数
func TestEveryToolHasIntent(t *testing.T) {
	for name, tl := range tool.Tools {
		schema := tl.Schema()
		props, ok := schema["properties"].(map[string]any)
		if !ok {
			t.Fatalf("%s 的 schema 没有 properties", name)
		}
		if _, ok := props["intent"]; !ok {
			t.Fatalf("%s 的 schema 里没有 intent: %v", name, props)
		}
		required, _ := schema["required"].([]string)
		found := false
		for _, r := range required {
			if r == "intent" {
				found = true
			}
		}
		if !found {
			t.Fatalf("%s 应该把 intent 列进 required: %v", name, required)
		}
		// 描述里也要提一句，模型才知道这个字段是干嘛的
		if !strings.Contains(tl.Description(), "intent") {
			t.Fatalf("%s 的描述里没提 intent", name)
		}
	}
}

// 上下文占用贴在输入框上方：总数照抄响应里的 usage，三项分项显示占比，
// 配了上下文窗口时还要显示上限和已用百分比
func TestUsageLine(t *testing.T) {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	m.width = 80
	m.state = stateRunning

	// 还没拿到 usage 时不该多出一行
	if line := m.usageLine(); line != "" {
		t.Fatalf("没有 usage 时不该显示: %q", line)
	}

	got, _ := m.handleEvent(domain.Event{Kind: domain.EventUsage, Usage: domain.ContextUsage{
		SystemTokens:     1200,
		ToolTokens:       3000,
		MessageTokens:    5800,
		PromptTokens:     10000,
		CompletionTokens: 250,
		ContextLimit:     40000,
	}})
	m = got.(model)

	line := stripANSI(m.usageLine())
	// 显示按 1024 进：10000 → 9.8k、40000 → 39k、3000 → 2.9k
	for _, want := range []string{
		"上下文 9.8k / 39k",
		"已用 25%",
		"tokens",
		"系统提示词 1.2k 12%",
		"工具定义 2.9k 30%",
		"对话消息 5.7k 58%",
		"本轮回复 250",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("占用行里少了 %q: %q", want, line)
		}
	}

	// 事件里没带上限时，用配置里的默认窗口补上，不能退化成不显百分比
	got, _ = m.handleEvent(domain.Event{Kind: domain.EventUsage, Usage: domain.ContextUsage{
		PromptTokens: 64000,
	}})
	m = got.(model)
	line = stripANSI(m.usageLine())
	for _, want := range []string{
		"上下文 62k / " + domain.FormatTokens(config.DefaultContextLimit),
		"已用 50%",
	} {
		if !strings.Contains(line, want) {
			t.Fatalf("没补上配置里的上下文上限，少了 %q: %q", want, line)
		}
	}

	// 这一行要真的出现在界面里，紧挨着输入框
	if view := stripANSI(m.View().Content); !strings.Contains(view, "已用 50%") {
		t.Fatalf("界面里没显示上下文占用:\n%s", view)
	}
}

// 配置表单里的上下文上限：默认带出默认值，填了就用填的，填了非正整数要拦住
func TestConfigFormContextLimit(t *testing.T) {
	f := newConfigForm(config.DeepSeekConfig{ApiUrl: "https://api.deepseek.com", ApiKey: "sk-1", Model: "deepseek-chat"})
	if got := f.value(3); got != strconv.Itoa(config.DefaultContextLimit) {
		t.Fatalf("表单里应该带出默认上限，实际 %q", got)
	}
	if dc, err := f.deepSeekConfig(); err != nil || dc.Limit() != config.DefaultContextLimit {
		t.Fatalf("没动过上限时应该用默认值: %+v, %v", dc, err)
	}

	f.inputs[3].SetValue("64000")
	dc, err := f.deepSeekConfig()
	if err != nil {
		t.Fatalf("填了合法上限不该报错: %v", err)
	}
	if dc.ContextLimit != 64000 {
		t.Fatalf("上限应该是 64000，实际 %d", dc.ContextLimit)
	}

	for _, bad := range []string{"abc", "0", "-5", "12x"} {
		f.inputs[3].SetValue(bad)
		if _, err := f.deepSeekConfig(); err == nil {
			t.Fatalf("上限填 %q 应该报错", bad)
		}
	}

	// 带单位的写法按 1024 进制换算，1M = 1024k
	for _, c := range []struct {
		in   string
		want int
	}{
		{"128k", 131072},
		{"1M", 1048576},
		{"1.5M", 1572864},
	} {
		f.inputs[3].SetValue(c.in)
		dc, err := f.deepSeekConfig()
		if err != nil {
			t.Fatalf("上限填 %q 不该报错: %v", c.in, err)
		}
		if dc.ContextLimit != c.want {
			t.Fatalf("上限填 %q 应该是 %d，实际 %d", c.in, c.want, dc.ContextLimit)
		}
	}
}

// 收到确认事件后界面切到确认状态，把要改什么、跑什么原样摊开
func TestApprovalView(t *testing.T) {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	m.width = 80
	m.state = stateRunning

	reply := make(chan domain.ApprovalAnswer, 1)
	got, _ := m.handleEvent(domain.Event{
		Kind: domain.EventApproval,
		Approval: &domain.ApprovalRequest{
			Tool:   "Bash",
			Kind:   domain.ApprovalRunCommand,
			Target: "go test ./...",
			Detail: "跑测试 · 目录 src",
		},
		Reply: reply,
	})
	m = got.(model)
	if m.state != stateApproval {
		t.Fatalf("应该在等确认，实际状态 %v", m.state)
	}
	view := stripANSI(m.approvalView())
	for _, want := range []string{
		"需要确认", "Bash", "$ go test ./...", "跑测试",
		"▸ 1. 允许一次", "2. 本回合都允许 Bash", "3. 拒绝", "直接打字可以拒绝并说明",
	} {
		if !strings.Contains(view, want) {
			t.Fatalf("确认框里少了 %q:\n%s", want, view)
		}
	}
	// 默认停在"允许一次"上，所以直接回车就是放行
	if m.approval.choice != 0 {
		t.Fatalf("默认应该选中第一项，实际 %d", m.approval.choice)
	}

	// 改文件的确认框把路径、行数和改动内容都说清楚
	m.approval = &approvalState{
		req: domain.ApprovalRequest{
			Tool: "Edit", Kind: domain.ApprovalWriteFile,
			Target: "src/cli/ui.go", Detail: "3 行换成 5 行",
			Diff: &domain.DiffPreview{
				Removed: []string{"old line"},
				Added:   []string{"new line"},
			},
		},
		reply: make(chan domain.ApprovalAnswer, 1),
	}
	view = stripANSI(m.approvalView())
	for _, want := range []string{"src/cli/ui.go", "3 行换成 5 行", "- old line", "+ new line"} {
		if !strings.Contains(view, want) {
			t.Fatalf("确认框里少了 %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "$ ") {
		t.Fatalf("改文件不该用命令的样子显示:\n%s", view)
	}

	// 预览行数太多时给个说明，别让人以为改动就这么点
	m.approval.req.Diff.Truncated = true
	if view := stripANSI(m.approvalView()); !strings.Contains(view, "预览只显示开头几行") {
		t.Fatalf("截断时应该说明:\n%s", view)
	}
}

// y 允许一次、a 本回合都允许、n 拒绝，回答要真的送到 core 那边
func TestApprovalKeys(t *testing.T) {
	cases := map[string]domain.ApprovalAnswer{
		"y":     {Allowed: true},
		"enter": {Allowed: true},
		"a":     {Allowed: true, Always: true},
		"n":     {},
		"esc":   {},
	}
	keys := map[string]tea.Key{
		"y":     {Code: 'y', Text: "y"},
		"enter": {Code: tea.KeyEnter},
		"a":     {Code: 'a', Text: "a"},
		"n":     {Code: 'n', Text: "n"},
		"esc":   {Code: tea.KeyEscape},
	}
	for name, want := range cases {
		m := newApprovalModel(t)
		reply := m.approval.reply

		got, _ := m.handleApprovalKey(tea.KeyPressMsg(keys[name]))
		after := got.(model)
		if after.state != stateRunning || after.approval != nil {
			t.Fatalf("%q 之后应该回到运行状态，实际 %v / %+v", name, after.state, after.approval)
		}
		if ans, ok := tryAnswer(reply); !ok || ans != want {
			t.Fatalf("%q 回答是 %+v，应该是 %+v", name, ans, want)
		}
	}
}

// 方向键换选项，Enter 确认选中的那个：默认第一项是允许，选到第三项就是拒绝
func TestApprovalChooseWithArrows(t *testing.T) {
	m := newApprovalModel(t)
	reply := m.approval.reply

	// ↓ 两次到"拒绝"
	got, _ := m.handleApprovalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	got, _ = got.(model).handleApprovalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}))
	m = got.(model)
	if m.approval.choice != 2 {
		t.Fatalf("↓ 两次应该停在第三项，实际 %d", m.approval.choice)
	}
	got, _ = m.handleApprovalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if ans, ok := tryAnswer(reply); !ok || ans.Allowed {
		t.Fatalf("选到拒绝时回车应该拒绝: %+v", ans)
	}

	// ↑ 从第一项绕到最后一项（拒绝），再 ↑ 回到第二项（本回合都允许）
	m = newApprovalModel(t)
	reply = m.approval.reply
	got, _ = m.handleApprovalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	got, _ = got.(model).handleApprovalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}))
	got, _ = got.(model).handleApprovalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if ans, ok := tryAnswer(reply); !ok || !ans.Always {
		t.Fatalf("绕过一圈应该落在本回合都允许: %+v", ans)
	}

	// j / k 也能上下移动
	m = newApprovalModel(t)
	got, _ = m.handleApprovalKey(tea.KeyPressMsg(tea.Key{Code: 'j', Text: "j"}))
	if got.(model).approval.choice != 1 {
		t.Fatalf("j 应该往下移一项，实际 %d", got.(model).approval.choice)
	}
	got, _ = got.(model).handleApprovalKey(tea.KeyPressMsg(tea.Key{Code: 'k', Text: "k"}))
	if got.(model).approval.choice != 0 {
		t.Fatalf("k 应该往上移一项，实际 %d", got.(model).approval.choice)
	}
}

// 直接打字就进"拒绝并说明"，写完回车把这句话连同拒绝一起转给模型
func TestApprovalTypeFeedback(t *testing.T) {
	m := newApprovalModel(t)
	reply := m.approval.reply

	// 打一个字就进入输入状态，这个字算第一个字符
	got, _ := m.handleApprovalKey(tea.KeyPressMsg(tea.Key{Code: '别', Text: "别"}))
	m = got.(model)
	if !m.approval.typing {
		t.Fatal("开始打字应该进入说明输入")
	}
	if want := "别"; string(m.approval.feedback) != want {
		t.Fatalf("第一个字没进缓冲区: %q", string(m.approval.feedback))
	}
	if len(reply) != 0 {
		t.Fatal("还在打字时不该回答 core")
	}

	for _, r := range "动这个文件" {
		got, _ = m.handleApprovalKey(tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)}))
		m = got.(model)
	}
	// 退格删掉一个字，再补回来，顺便验证光标能左右移
	got, _ = m.handleApprovalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyBackspace}))
	m = got.(model)
	if want := "别动这个文"; string(m.approval.feedback) != want {
		t.Fatalf("退格没生效: %q", string(m.approval.feedback))
	}
	got, _ = m.handleApprovalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyLeft}))
	got, _ = got.(model).handleApprovalKey(tea.KeyPressMsg(tea.Key{Code: '件', Text: "件"}))
	m = got.(model)
	if want := "别动这个件文"; string(m.approval.feedback) != want {
		t.Fatalf("光标处插入没生效: %q", string(m.approval.feedback))
	}

	got, _ = m.handleApprovalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	after := got.(model)
	if after.state != stateRunning || after.approval != nil {
		t.Fatalf("回车之后应该回到运行状态，实际 %v", after.state)
	}
	ans, ok := tryAnswer(reply)
	if !ok {
		t.Fatal("说明写完没有回答 core")
	}
	if ans.Allowed || ans.Feedback != "别动这个件文" {
		t.Fatalf("应该是拒绝并带上说明: %+v", ans)
	}
}

// 打字状态下 Esc 回到选项列表，不回答 core
func TestApprovalTypeEsc(t *testing.T) {
	m := newApprovalModel(t)
	reply := m.approval.reply

	got, _ := m.handleApprovalKey(tea.KeyPressMsg(tea.Key{Code: 'x', Text: "x"}))
	got, _ = got.(model).handleApprovalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}))
	m = got.(model)
	if m.approval.typing {
		t.Fatal("Esc 应该退回选项列表")
	}
	if len(reply) != 0 {
		t.Fatal("Esc 不该回答 core")
	}
	// 退回之后照常能用方向键和回车
	got, _ = m.handleApprovalKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	if ans, ok := tryAnswer(reply); !ok || !ans.Allowed {
		t.Fatalf("回车应该允许: %+v", ans)
	}
	if got.(model).state != stateRunning {
		t.Fatal("回答完应该回到运行状态")
	}
}

// 打字时的确认框要能看见光标和那句话
func TestApprovalTypingView(t *testing.T) {
	m := newApprovalModel(t)
	m.approval.typing = true
	m.approval.insert([]rune("改 config 就行")...)

	view := stripANSI(m.approvalView())
	for _, want := range []string{"拒绝并说明", "改 config 就行", "Enter 连同这句话拒绝", "Esc 回到选项"} {
		if !strings.Contains(view, want) {
			t.Fatalf("输入状态下少了 %q:\n%s", want, view)
		}
	}
	if strings.Contains(view, "1. 允许一次") {
		t.Fatalf("输入状态下不该还铺着选项:\n%s", view)
	}
}

// newApprovalModel 造一个停在确认框上的界面，工具名是 Write
func newApprovalModel(t *testing.T) model {
	t.Helper()
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	m.width = 80
	m.state = stateApproval
	m.approval = &approvalState{
		req:   domain.ApprovalRequest{Tool: "Write", Kind: domain.ApprovalWriteFile, Target: "a.go"},
		reply: make(chan domain.ApprovalAnswer, 1),
	}
	return m
}

// tryAnswer 取 core 那边的回答，没送到时 ok 为 false
func tryAnswer(reply chan domain.ApprovalAnswer) (domain.ApprovalAnswer, bool) {
	select {
	case ans := <-reply:
		return ans, true
	default:
		return domain.ApprovalAnswer{}, false
	}
}

// 开场白要把输入框顶到屏幕底部：开场白 + 界面 + 空行正好铺满一屏
func TestStartupTextFillsScreen(t *testing.T) {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	got, _ := m.Update(tea.WindowSizeMsg{Width: 100, Height: 30})
	m = got.(model)

	text := m.startupText()
	// 数一下开场白一共占几行：tea.Println 按换行切行，行数就是 \n 的个数 + 1
	printed := strings.Count(text, "\n") + 1
	if total := printed + m.viewLines(); total != m.height {
		t.Fatalf("开场白 + 界面应该正好铺满 %d 行，实际 %d 行（开场白 %d + 界面 %d）",
			m.height, total, printed, m.viewLines())
	}
	// 开场白本身还得在，不能被算没了
	if !strings.Contains(stripANSI(text), "agent-dev") {
		t.Fatalf("开场白丢了:\n%s", text)
	}
}

// 终端太矮塞不下开场白时不要倒贴空行
func TestStartupTextTinyTerminal(t *testing.T) {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	m.width, m.height = 80, 3
	if text := m.startupText(); strings.Contains(text, "\n\n") {
		t.Fatalf("矮终端不该补空行: %q", text)
	}
}

// 后台任务栏：没有任务就不占行，有任务时显示状态和耗时
func TestTasksLine(t *testing.T) {
	if line := tasksLine(nil); line != "" {
		t.Fatalf("没有任务时不该有任务栏: %q", line)
	}

	line := stripANSI(tasksLine([]tool.TaskSnapshot{
		{ID: "task_1", Status: "运行中", Running: true, Elapsed: 3 * time.Second, Command: "npm run dev"},
		{ID: "task_2", Status: "已完成", Elapsed: 2 * time.Second, Command: "go build ./..."},
	}))
	for _, want := range []string{"后台任务", "1 个运行中", "1 个已结束", "task_1 运行中 3 秒", "task_2 已完成"} {
		if !strings.Contains(line, want) {
			t.Fatalf("任务栏里少了 %q: %q", want, line)
		}
	}

	// 全都跑完了就别再说"运行中"
	line = stripANSI(tasksLine([]tool.TaskSnapshot{
		{ID: "task_1", Status: "已完成"},
	}))
	if !strings.Contains(line, "1 个已结束") || strings.Contains(line, "运行中") {
		t.Fatalf("都结束了不该提运行中: %q", line)
	}

	// 任务太多时只列前面几个
	many := make([]tool.TaskSnapshot, 0, maxTaskChips+2)
	for i := 0; i < maxTaskChips+2; i++ {
		many = append(many, tool.TaskSnapshot{ID: "task_" + strconv.Itoa(i+1), Status: "已完成"})
	}
	line = stripANSI(tasksLine(many))
	if !strings.Contains(line, "…还有 2 个") {
		t.Fatalf("任务多时要收起来: %q", line)
	}
}

// 真的起了后台任务之后，任务栏要出现在界面里，不用等模型再动一下
func TestTasksLineInView(t *testing.T) {
	t.Cleanup(tool.StopAllTasks)

	if line := stripANSI(newModel(config.GlobalConfig{}, nil, ".", domain.NewSession(".")).tasksLine()); line != "" {
		t.Fatalf("一开始不该有任务栏: %q", line)
	}

	raw := []byte(`{"command":"echo hi","background":true}`)
	if _, err := (tool.BashTool{}).Execute(nil, raw); err != nil {
		t.Fatalf("起后台任务失败: %v", err)
	}

	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	m.width = 80
	if line := stripANSI(m.tasksLine()); !strings.Contains(line, "task_1") {
		t.Fatalf("任务栏里应该有刚起的任务: %q", line)
	}
	if view := stripANSI(m.View().Content); !strings.Contains(view, "后台任务") {
		t.Fatalf("界面里没显示后台任务:\n%s", view)
	}
}

// 有任务在跑时界面要自己续上定时刷新，跑完之后别再空转
func TestTaskTickerRearms(t *testing.T) {
	if runtime.GOOS == "windows" && tool.ShellName() == "cmd" {
		t.Skip("cmd 下没有统一的 sleep 命令，跳过")
	}
	t.Cleanup(tool.StopAllTasks)

	// 没有任务时不该起定时器
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	if got, cmd := m.Update(taskTickMsg{}); cmd != nil || got.(model).tasksTicking {
		t.Fatal("没有后台任务时不该定时刷新")
	}

	// 起一个还在跑的任务，刷新一拍之后要自己续上下一拍
	sleep := "sleep 30"
	if runtime.GOOS == "windows" {
		sleep = "Start-Sleep -Seconds 30"
	}
	raw := []byte(`{"command":"` + sleep + `","timeout_seconds":60,"background":true}`)
	if _, err := (tool.BashTool{}).Execute(nil, raw); err != nil {
		t.Fatalf("起后台任务失败: %v", err)
	}

	got, cmd := m.Update(taskTickMsg{})
	if cmd == nil {
		t.Fatal("还有任务在跑时应该继续定时刷新")
	}
	if !got.(model).tasksTicking {
		t.Fatal("继续刷新时要记着定时器已经起来了，别重复起")
	}
}

// /mode 不带参数报当前模式，带了就切过去并写回配置
func TestSwitchMode(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	m := newModel(config.GlobalConfig{Mode: "read"}, nil, ".", domain.NewSession("."))
	m.cfgPath = path
	if m.mode != domain.ModeReadOnly {
		t.Fatalf("配置里的模式没生效: %v", m.mode)
	}

	got, _ := m.switchMode("/mode")
	if text := stripANSI(modeText(got.(model).mode)); !strings.Contains(text, "只读") {
		t.Fatalf("/mode 没报当前模式:\n%s", text)
	}

	got, _ = m.switchMode("/mode full")
	after := got.(model)
	if after.mode != domain.ModeFull {
		t.Fatalf("没切到完全授权: %v", after.mode)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("配置没写回: %v", err)
	}
	if !strings.Contains(string(data), "Mode: full") {
		t.Fatalf("写回的配置里没有 Mode: full:\n%s", data)
	}

	// 认不出的模式报错，模式不变
	before := after.mode
	got, _ = after.switchMode("/mode 半自动")
	if got.(model).mode != before {
		t.Fatal("认不出的模式不该切换")
	}
}

// 粘贴进来的多行文本要压成一行，控制字符丢掉，别把确认框撑坏
func TestApprovalInsertSanitizes(t *testing.T) {
	m := newApprovalModel(t)
	m.approval.typing = true

	// 括号是 paste 的 String() 形式，真实的粘贴消息里 Text 是原文
	got, _ := m.handleApprovalKey(tea.KeyPressMsg(tea.Key{Text: "第一行\n第二行\r\n带\t制表符"}))
	m = got.(model)
	want := "第一行 第二行 带 制表符"
	if string(m.approval.feedback) != want {
		t.Fatalf("粘贴内容应该压成一行: %q", string(m.approval.feedback))
	}
	if m.approval.cursor != len([]rune(want)) {
		t.Fatalf("光标应该在末尾，实际 %d", m.approval.cursor)
	}

	// 控制字符（比如 ctrl+a 那种）不进缓冲区
	before := string(m.approval.feedback)
	got, _ = m.handleApprovalKey(tea.KeyPressMsg(tea.Key{Text: "\x01"}))
	if after := string(got.(model).approval.feedback); after != before {
		t.Fatalf("控制字符不该进缓冲区: %q", after)
	}
}

// 确认框里粘贴整段文字：当成"拒绝并说明"的内容，不再改到藏起来的输入框
func TestApprovalPaste(t *testing.T) {
	m := newApprovalModel(t)
	m.input.SetValue("原本在输入框里的内容")

	got, _ := m.Update(tea.PasteMsg{Content: "改 config\n就行"})
	after := got.(model)
	if !after.approval.typing {
		t.Fatal("粘贴应该进入说明输入")
	}
	if want := "改 config 就行"; string(after.approval.feedback) != want {
		t.Fatalf("粘贴内容不对: %q", string(after.approval.feedback))
	}
	if v := after.input.Value(); v != "原本在输入框里的内容" {
		t.Fatalf("输入框不该被粘贴内容改到: %q", v)
	}
}
