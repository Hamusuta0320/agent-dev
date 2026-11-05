package main

import (
	"fmt"
	"os"
	"path/filepath"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"

	"hamusuta/agent-dev/src/config"
	"hamusuta/agent-dev/src/core"
	"hamusuta/agent-dev/src/domain"
	"hamusuta/agent-dev/src/session"
	"hamusuta/agent-dev/src/tool"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// ansiRe 匹配着色的转义序列，断言文案前要先剥掉
var ansiRe = regexp.MustCompile("\x1b\\[[0-9;]*m")

func TestParseCommand(t *testing.T) {
	cases := map[string]command{
		"/help":      cmdHelp,
		"/new":       cmdNew,
		"/resume":    cmdResume,
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
		"/":     {"/help", "/new", "/resume", "/session", "/config", "/mode", "/model", "/skills", "/todo", "/quiet", "/exit"},
		"/c":    {"/config"},
		"/re":   {"/resume"},
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
	// 本机装了什么技能不该影响这条断言，技能单独的补全在 TestSuggestSkills 里测
	m.skills = nil
	m.input.SetValue("/")
	m.suggestIdx = 2
	lines := m.completionLines()
	// 候选比槽位多：留一行报总数，其余是选中项附近的几个
	if len(lines) != maxActiveRows {
		t.Fatalf("候选应该压到 %d 行: %q", maxActiveRows, lines)
	}
	for i, line := range lines[:maxActiveRows-1] {
		hasMark := strings.HasPrefix(stripANSI(line), "▸ ")
		if hasMark != (i == 2) {
			t.Fatalf("第 %d 行的高亮不对: %q", i, line)
		}
	}
	if last := stripANSI(lines[len(lines)-1]); !strings.Contains(last, strconv.Itoa(len(commandSpecs))) {
		t.Fatalf("最后一行该说明一共有多少候选: %q", last)
	}

	// 不是命令输入时没有候选
	m.input.SetValue("你好")
	if lines := m.completionLines(); lines != nil {
		t.Fatalf("普通输入不该有候选: %q", lines)
	}
}

// 候选多到一屏放不下时，选中项必须始终留在可见的那一段里，
// 否则 ↑↓ 就是在摸黑挪光标
func TestCompletionLinesFollowsSelection(t *testing.T) {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	m.skills = nil
	for i := 0; i < 30; i++ {
		m.skills = append(m.skills, core.Skill{Name: fmt.Sprintf("skill-%02d", i), Desc: "技能"})
	}
	m.input.SetValue("/skill-")

	last := len(m.suggestAll()) - 1
	m.suggestIdx = last
	lines := m.completionLines()
	if len(lines) != maxActiveRows {
		t.Fatalf("应该压到 %d 行，实际 %d 行", maxActiveRows, len(lines))
	}
	found := false
	for _, line := range lines {
		if strings.HasPrefix(stripANSI(line), "▸ ") && strings.Contains(stripANSI(line), "skill-29") {
			found = true
		}
	}
	if !found {
		t.Fatalf("选到最后一项时它该留在可见范围内: %q", lines)
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
	if dc, err := saved.cfg.CurrentModel(); err != nil || dc.Model != "deepseek-chat" || dc.ApiKey != "sk-test" {
		t.Fatalf("内存里的配置没更新: %+v, %v", dc, err)
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

// viewRows 数界面这会儿占了几行：View 按换行切行，行数就是 \n 的个数 + 1
func viewRows(m model) int {
	return strings.Count(m.View().Content, "\n") + 1
}

// 打进历史区的每一行都不能超过终端宽度：tea.Println 是直接写终端的，
// 行太宽终端会自己折行，程序记的行数就和屏幕对不上，界面从此错位
func TestPrintNeverExceedsWidth(t *testing.T) {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	m.width = 40

	// 一份带空行、带缩进、中英文混杂的多行文本
	text := "  " + strings.Repeat("中文", 20) + "\n\n" +
		"  目录 D:\\code\\go\\agent-dev\\src\\cli\n" + strings.Repeat("a", 120)
	got := m.fitWidth(text)

	before := strings.Count(text, "\n") + 1
	after := strings.Count(got, "\n") + 1
	if after <= before {
		t.Fatalf("超宽的行应该折成多行: %d -> %d 行\n%s", before, after, got)
	}
	for i, line := range strings.Split(got, "\n") {
		// 留一列余量，避免正好写满最后一列
		if w := lipgloss.Width(line); w >= m.width {
			t.Fatalf("第 %d 行宽 %d，超过了终端宽度 %d: %q", i, w, m.width, line)
		}
	}

	// 空行得原样留着：打出去几行就是几行，行数不能凭空变
	if !strings.Contains(got, "\n\n") {
		t.Fatalf("空行不该被吃掉: %q", got)
	}
	// 宽度还不知道的时候原样交出去，别把内容折坏
	unknown := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	unknown.width = 0
	if got := unknown.fitWidth(text); got != text {
		t.Fatalf("宽度未知时不该动内容: %q", got)
	}
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

// 上下文占用贴在输入框下面：总数照抄响应里的 usage，三项分项显示占比，
// 配了上下文窗口时还要显示上限和已用百分比
func TestUsageLine(t *testing.T) {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	// 给足宽度：这一行放不下时会把尾巴上的分项丢掉，那样就测不到分项了
	m.width = 200
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

	// 这一行要真的出现在界面里，而且贴着输入框的下边框、在底部提示行上面
	m.state = stateIdle // 回到闲着的样子，底部提示行就是"Enter 发送 …"
	view := stripANSI(m.View().Content)
	if !strings.Contains(view, "已用 50%") {
		t.Fatalf("界面里没显示上下文占用:\n%s", view)
	}
	usageAt, boxAt, hintAt := -1, -1, -1
	for i, line := range strings.Split(view, "\n") {
		switch {
		case strings.Contains(line, "已用 50%"):
			usageAt = i
		case strings.Contains(line, "╰"):
			boxAt = i // 输入框的下边框
		case strings.Contains(line, "Enter 发送"):
			hintAt = i
		}
	}
	if boxAt < 0 || usageAt <= boxAt {
		t.Fatalf("上下文占用应该贴在输入框下面（输入框底边在第 %d 行，占用行在第 %d 行）:\n%s",
			boxAt, usageAt, view)
	}
	if hintAt < 0 || usageAt >= hintAt {
		t.Fatalf("上下文占用应该在底部提示行上面（占用行在第 %d 行，提示行在第 %d 行）:\n%s",
			usageAt, hintAt, view)
	}
}

// 配置表单里的上下文上限：默认带出默认值，填了就用填的，填了非正整数要拦住
func TestConfigFormContextLimit(t *testing.T) {
	f := newConfigForm(config.ModelConfig{ApiUrl: "https://api.deepseek.com", ApiKey: "sk-1", Model: "deepseek-chat"})
	if got := f.value(3); got != strconv.Itoa(config.DefaultContextLimit) {
		t.Fatalf("表单里应该带出默认上限，实际 %q", got)
	}
	if dc, err := f.modelConfig(); err != nil || dc.Limit() != config.DefaultContextLimit {
		t.Fatalf("没动过上限时应该用默认值: %+v, %v", dc, err)
	}

	f.inputs[3].SetValue("64000")
	dc, err := f.modelConfig()
	if err != nil {
		t.Fatalf("填了合法上限不该报错: %v", err)
	}
	if dc.ContextLimit != 64000 {
		t.Fatalf("上限应该是 64000，实际 %d", dc.ContextLimit)
	}

	for _, bad := range []string{"abc", "0", "-5", "12x"} {
		f.inputs[3].SetValue(bad)
		if _, err := f.modelConfig(); err == nil {
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
		dc, err := f.modelConfig()
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

// 输入框上方那块按内容占行，但永远不超过 activeRows() 这个上限：
// 内容再多也只是裁，帧高不许顶到屏幕上去（顶上去 print 的插行账就错了）
func TestActiveViewCapsRows(t *testing.T) {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	m.width, m.height = 80, 30
	m.cur.Messages = []domain.SessionMessage{{Role: "user", Content: "问点什么"}}

	lines := func() int {
		if v := m.activeView(); v != "" {
			return strings.Count(v, "\n") + 1
		}
		return 0
	}
	// 闲着又没有别的东西：一行不占
	if got := lines(); got != 0 {
		t.Fatalf("闲着时这块不该占行，实际 %d 行", got)
	}

	// 补全候选该占它自己的行数，也不超上限
	m.input.SetValue("/")
	if got, cap := lines(), m.activeRows(); got == 0 || got > cap {
		t.Fatalf("有补全候选时该占 1..%d 行，实际 %d 行", cap, got)
	}

	// 跑任务时内容越长越是只裁到上限
	m.state = stateRunning
	m.status = "思考中…"
	m.reasoning.WriteString(strings.Repeat("想了很久\n", 40))
	m.answer.WriteString(strings.Repeat("回答很长\n", 40))
	if got, cap := lines(), m.activeRows(); got != cap {
		t.Fatalf("运行中内容超了就该正好裁到上限 %d 行，实际 %d 行", cap, got)
	}
	// 状态行不能被裁掉，够长的时候还要留出预览的尾巴
	view := stripANSI(m.activeView())
	if !strings.Contains(view, "思考中…") {
		t.Fatalf("状态行不该被裁掉:\n%s", view)
	}
	if !strings.Contains(view, "回答很长") {
		t.Fatalf("应该留出最新的预览:\n%s", view)
	}

	// 多个工具同时在跑也一样守着上限
	m.pending = []pendingCall{
		{id: "1", lines: m.toolCallLines(domain.ToolCall{Name: "Read", Arguments: `{"path":"a.go"}`})},
		{id: "2", lines: m.toolCallLines(domain.ToolCall{Name: "Grep", Arguments: `{"pattern":"x"}`})},
	}
	if got, cap := lines(), m.activeRows(); got != cap {
		t.Fatalf("有工具在跑时该裁到上限 %d 行，实际 %d 行", cap, got)
	}
}

// 闲着时没有内容要摆，这块就一行都不占：界面紧贴着历史区往下排，
// 不为了对齐留一片空白，也不再把上一轮回答的尾巴挂回来
func TestIdleSlotTakesNoRows(t *testing.T) {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	m.width, m.height = 80, 30
	// 聊过一轮之后
	m.cur.Messages = []domain.SessionMessage{{Role: "user", Content: "问点什么"}, {Role: "assistant", Content: "答了"}}

	if view := m.activeView(); view != "" {
		t.Fatalf("闲着又没有别的信息时这块不该占行，实际:\n%s", view)
	}

	// 输入 / 之后补全候选还是要摆出来
	m.input.SetValue("/")
	if view := m.activeView(); view == "" {
		t.Fatalf("有补全候选时该摆候选")
	}
}

// 还没聊过的会话摆一份提示：常用命令、按键。工具说明不在这儿列
func TestIdleSlotShowsHints(t *testing.T) {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	m.width, m.height = 80, 30

	view := stripANSI(m.activeView())
	for _, want := range []string{"/help", "/config", "/mode", "Enter 发送", "Ctrl+V 贴图"} {
		if !strings.Contains(view, want) {
			t.Fatalf("提示里该提到 %q:\n%s", want, view)
		}
	}
	// 提示就是它自己的行数，不再补空行凑高度
	if got, want := strings.Count(m.activeView(), "\n")+1, len(m.hintLines()); m.activeView() != "" && got != want {
		t.Fatalf("摆提示时该占 %d 行，实际 %d 行", want, got)
	}

	// 工具说明不再摆在这块，换模式也不该把工具名列出来
	m.mode = domain.ModeReadOnly
	if view := stripANSI(m.activeView()); strings.Contains(view, "Bash") || strings.Contains(view, "读文件") {
		t.Fatalf("提示里不该再列工具:\n%s", view)
	}

	// 聊过之后提示让位，这块也不留空行
	m.mode = domain.ModeAsk
	m.cur.Messages = []domain.SessionMessage{{Role: "user", Content: "问点什么"}}
	if view := stripANSI(m.activeView()); view != "" {
		t.Fatalf("聊过之后不该再占位置摆提示:\n%s", view)
	}
}

// 开场白就是开场那几行，不再往上补空行去把输入框顶到屏幕底部：
// 打了几行就是几行，跟终端高度无关
func TestStartupTextNoPadding(t *testing.T) {
	for _, h := range []int{3, 12, 30, 60} {
		m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
		m.width, m.height = 100, h

		text := stripANSI(m.startupText())
		if got, want := strings.Count(text, "\n")+1, len(m.bannerLines()); got != want {
			t.Fatalf("高 %d：开场白该是 %d 行，实际 %d 行: %q", h, want, got, text)
		}
		// 不能有任何一段空行，也不能以空行开头
		if strings.Contains(text, "\n\n") || strings.HasPrefix(text, "\n") || strings.HasSuffix(text, "\n") {
			t.Fatalf("高 %d：开场白不该补空行: %q", h, text)
		}
		// 开场白本身还得在，不能被算没了
		if !strings.Contains(text, "agent-dev") {
			t.Fatalf("高 %d：开场白丢了:\n%s", h, text)
		}
	}
}

// 后台任务栏：没有任务就不占行，有任务时显示状态和耗时
func TestTasksLine(t *testing.T) {
	if line := tasksLine(nil, 0); line != "" {
		t.Fatalf("没有任务时不该有任务栏: %q", line)
	}

	line := stripANSI(tasksLine([]tool.TaskSnapshot{
		{ID: "task_1", Status: "运行中", Running: true, Elapsed: 3 * time.Second, Command: "npm run dev"},
		{ID: "task_2", Status: "已完成", Elapsed: 2 * time.Second, Command: "go build ./..."},
	}, 0))
	for _, want := range []string{"后台任务", "1 个运行中", "1 个已结束", "task_1 运行中 3 秒", "task_2 已完成"} {
		if !strings.Contains(line, want) {
			t.Fatalf("任务栏里少了 %q: %q", want, line)
		}
	}

	// 全都跑完了就别再说"运行中"
	line = stripANSI(tasksLine([]tool.TaskSnapshot{
		{ID: "task_1", Status: "已完成"},
	}, 0))
	if !strings.Contains(line, "1 个已结束") || strings.Contains(line, "运行中") {
		t.Fatalf("都结束了不该提运行中: %q", line)
	}

	// 任务太多时只列前面几个
	many := make([]tool.TaskSnapshot, 0, maxTaskChips+2)
	for i := 0; i < maxTaskChips+2; i++ {
		many = append(many, tool.TaskSnapshot{ID: "task_" + strconv.Itoa(i+1), Status: "已完成"})
	}
	line = stripANSI(tasksLine(many, 0))
	if !strings.Contains(line, "…还有 2 个") {
		t.Fatalf("任务多时要收起来: %q", line)
	}

	// 窄终端里少列几个，绝不能超过给的宽度（折行会把界面高度顶乱）
	for limit := 20; limit <= 80; limit += 5 {
		got := tasksLine(many, limit)
		if w := lipgloss.Width(got); limit >= 30 && w > limit {
			t.Fatalf("限宽 %d 时任务栏宽 %d，超了: %q", limit, w, stripANSI(got))
		}
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
	// 闲着时上方槽位把任务摊开，带上命令，就不空着也不至于认不出是哪个任务
	idleView := stripANSI(m.activeView())
	if !strings.Contains(idleView, "echo hi") {
		t.Fatalf("闲时槽位里该能看到任务的命令:\n%s", idleView)
	}
	// 每个任务都带上状态和时长：跑着的看已跑多久，结束的看总耗时
	if !strings.Contains(idleView, "task_1") ||
		(!strings.Contains(idleView, "已跑") && !strings.Contains(idleView, "耗时")) {
		t.Fatalf("闲时槽位里该有任务 id 和时长:\n%s", idleView)
	}
	if got, cap := strings.Count(m.activeView(), "\n")+1, m.activeRows(); got > cap {
		t.Fatalf("有任务时这块该占任务自己的行数、不超上限 %d，实际 %d 行", cap, got)
	}
}

// 帧高不再要求恒定（输入框上方那块有内容就占行、没内容就不占），但两条不变量还在：
// 帧高不能顶到屏幕上去（顶上去 print 的插行账就错了，屏幕上叠出残影），
// 每一行也不能宽到让终端自己折行（终端一折，程序记的帧高就和屏幕对不上）。
func TestViewHeightWithinTerminal(t *testing.T) {
	t.Cleanup(tool.StopAllTasks)

	// 窄终端也跑一遍：这一行放不下时只能裁内容，不能折成两行
	for _, width := range []int{50, 80, 100} {
		m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
		m.width, m.height = width, 30

		check := func(what string) {
			t.Helper()
			if got, cap := viewRows(m), m.height-reserveHistoryRows; got > cap {
				t.Fatalf("宽 %d %s：帧占了 %d 行，超过上限 %d 行（帧顶到屏幕顶上，插行会算错）",
					width, what, got, cap)
			}
			for i, line := range strings.Split(m.View().Content, "\n") {
				if w := lipgloss.Width(line); w >= m.width {
					t.Fatalf("宽 %d %s：第 %d 行宽 %d，超过终端宽度（终端会自己折行，界面从此错位）: %q",
						width, what, i, w, stripANSI(line))
				}
			}
		}

		check("刚打开")

		// 加上上下文占用：仍然一行，放不下就裁分项
		m.usage = domain.ContextUsage{
			PromptTokens: 45000, ContextLimit: 1_000_000,
			SystemTokens: 8700, ToolTokens: 2600, MessageTokens: 25000,
			CompletionTokens: 364,
		}
		check("有上下文占用")

		// 起个后台任务：任务栏冒出来
		if _, err := (tool.BashTool{}).Execute(nil, []byte(`{"command":"echo hi","background":true}`)); err != nil {
			t.Fatalf("起后台任务失败: %v", err)
		}
		check("有后台任务")

		// 跑任务时也一样：长命令、长状态都不能把某一行顶出终端
		m.state = stateRunning
		m.status = "等待模型响应（这一轮挺长的，得看看会不会把状态行顶出去）"
		m.pending = []pendingCall{{
			id:    "1",
			lines: m.toolCallLines(domain.ToolCall{Name: "Bash", Arguments: `{"intent":"跑一个很长的命令看看行宽","command":"` + strings.Repeat("a", 200) + `"}`}),
		}}
		check("跑任务")

		tool.StopAllTasks()
	}
}

// 输入框的宽度必须正好等于"框总宽减去边框内边距"。
//
// 之前这里差 6 格（textarea 按 width-4 设，但框里只装得下 width-8），
// 结果字提前折行、框从 5 行长到 6 行，整个界面行数变了，inline 渲染把顶上的
// 边框裁掉，看着就像输入框缺了一块。
func TestInputBoxWidthMatchesContentArea(t *testing.T) {
	for _, width := range []int{50, 80, 120} {
		m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
		// 走真实消息路径，跟运行时一致
		mi, _ := m.update(tea.WindowSizeMsg{Width: width, Height: 30})
		m = mi.(model)

		want := width - 4 - boxOverhead
		if got := m.input.Width(); got != want {
			t.Fatalf("宽 %d：输入框该是 %d 格（框总宽 %d 减边框内边距 %d），实际 %d 格",
				width, want, width-4, boxOverhead, got)
		}

		// 不管打多少字，框都必须是 5 行（上边框 + 3 行内容 + 下边框），
		// 而且不能比框还宽——长了就得自己滚动，不能把界面撑变形
		for name, text := range map[string]string{
			"空":    "",
			"刚满一行": strings.Repeat("a", want),
			"超一行":  strings.Repeat("a", want+2),
			"很多行":  strings.Repeat("b", want*5),
			"中文一堆": strings.Repeat("问", want/2+10),
			"带换行的": strings.Repeat("x\n", 8),
		} {
			m.input.SetValue(text)
			box := boxStyle.Width(width - 4).Render(m.input.View())
			lines := strings.Split(box, "\n")
			if len(lines) != 5 {
				t.Fatalf("宽 %d %s：输入框该恒定 5 行，实际 %d 行", width, name, len(lines))
			}
			for i, ln := range lines {
				if w := lipgloss.Width(ln); w > width-4 {
					t.Fatalf("宽 %d %s：第 %d 行宽 %d，超过框宽 %d",
						width, name, i, w, width-4)
				}
			}
		}
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

// /resume 序号或 id 都能挑中会话，挑不到要说清楚
func TestPickSession(t *testing.T) {
	infos := []session.Info{{Id: "20240103-000000-0002"}, {Id: "20240102-000000-0001"}}

	id, err := pickSession(infos, "1")
	if err != nil || id != "20240103-000000-0002" {
		t.Fatalf("序号 1 应该是最新的那个: %q %v", id, err)
	}
	id, err = pickSession(infos, "20240102-000000-0001")
	if err != nil || id != "20240102-000000-0001" {
		t.Fatalf("按 id 应该能挑中: %q %v", id, err)
	}
	if _, err := pickSession(infos, "3"); err == nil {
		t.Fatal("序号越界应该报错")
	}
	if _, err := pickSession(infos, "0"); err == nil {
		t.Fatal("序号 0 应该报错")
	}
	if _, err := pickSession(infos, "不存在的会话"); err == nil {
		t.Fatal("找不到 id 应该报错")
	}
}

// /resume <序号> 把当前会话换成存盘的那一份，下一句提问接着这段历史聊
func TestResumeSession(t *testing.T) {
	store := session.NewStore(filepath.Join(t.TempDir(), "sessions"))
	old := domain.NewSession(`D:\旧目录`)
	old.AppendUser("上次聊到一半")
	if err := store.Save(old); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	m := newModel(config.GlobalConfig{}, store, ".", domain.NewSession("."))
	got, _ := m.resume("/resume 1")
	restored := got.(model)
	if restored.cur.Id != old.Id {
		t.Fatalf("应该恢复成 %s，实际 %s", old.Id, restored.cur.Id)
	}
	if len(restored.cur.Messages) != 1 || restored.cur.Messages[0].Content != "上次聊到一半" {
		t.Fatalf("历史消息没恢复对: %+v", restored.cur.Messages)
	}

	// 别的目录里开的会话按 id 也能恢复，工具仍按当前目录跑
	m.workdir = `D:\code\agent-dev`
	if got, _ := m.resume("/resume " + old.Id); got.(model).cur.Id != old.Id {
		t.Fatalf("按 id 恢复失败，当前是 %s", got.(model).cur.Id)
	}
}

// 恢复会话要把这段历史重新打一遍：提问、思考、回答、工具调用和结果按原来的顺序排开，
// 和当时一轮轮聊出来的样子一致
func TestResumeRendersHistory(t *testing.T) {
	s := &domain.Session{Id: "20240102-000000-0001"}
	s.AppendUser("上次问的问题")
	s.AppendAssistant("上次的回答", "上次的思考", nil)

	ok := domain.ToolCall{Id: "c1", Name: "Bash", Arguments: `{"command":"go build ./...","intent":"编译一下"}`}
	s.AppendAssistant("", "", []domain.ToolCall{ok})
	s.AppendToolResult(ok, "exit_code: 0\n编译过了")

	// 被中断的那次调用：只有调用、没有结果
	lost := domain.ToolCall{Id: "c2", Name: "Read", Arguments: `{"path":"src/cli/ui.go"}`}
	s.AppendAssistant("", "", []domain.ToolCall{lost})

	// 失败的那次调用：core 把原因写成"执行失败: …"开头
	bad := domain.ToolCall{Id: "c3", Name: "Bash", Arguments: `{"command":"go test ./..."}`}
	s.AppendAssistant("", "", []domain.ToolCall{bad})
	s.AppendToolResult(bad, "执行失败: 找不到 go 命令")

	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	m.width = 80
	m.quiet = false // 详细显示：思考和工具输出都要真的打出来
	lines := m.historyLines(s)
	text := stripANSI(strings.Join(lines, "\n"))

	// 顺序得和当时跑的一致：提问 → 思考 → 回答 → 工具调用 → 结果
	order := []string{"上次问的问题", "上次的思考", "上次的回答", "go build ./...", "编译过了",
		"Read", "未完成", "src/cli/ui.go", "go test ./...", "执行失败: 找不到 go 命令"}
	last := -1
	for _, want := range order {
		at := strings.Index(text, want)
		if at < 0 {
			t.Fatalf("重打的历史里少了 %q:\n%s", want, text)
		}
		if at < last {
			t.Fatalf("%q 的顺序不对:\n%s", want, text)
		}
		last = at
	}

	// 工具结果贴在调用下面只打一次，别再单拎出来打第二遍
	if n := strings.Count(text, "编译过了"); n != 1 {
		t.Fatalf("工具结果打了两遍（%d 次）:\n%s", n, text)
	}

	// 圆圈按当时的结果上色：成功绿，失败和没跑完的红。
	// 细节行里也有同样的命令，所以只认以圆圈开头的那行
	green, red := doneMark(domain.ToolStatusSuccess), doneMark(domain.ToolStatusFailed)
	greenTitles, redTitles := 0, 0
	for _, line := range lines {
		switch {
		case strings.HasPrefix(line, green):
			greenTitles++
			if !strings.Contains(line, "Bash") || !strings.Contains(line, "编译一下") {
				t.Fatalf("绿圈该贴在成功的那次调用上: %q", line)
			}
		case strings.HasPrefix(line, red):
			redTitles++
			if !strings.Contains(line, "Bash") && !strings.Contains(line, "Read") {
				t.Fatalf("红圈该贴在失败或没跑完的那次调用上: %q", line)
			}
		}
	}
	if greenTitles != 1 || redTitles != 2 {
		t.Fatalf("圆圈数量不对：绿 %d 个、红 %d 个", greenTitles, redTitles)
	}
}

// 精简显示下恢复会话同样只留正文和工具行的标题，思考和工具输出不打
func TestResumeHistoryQuiet(t *testing.T) {
	s := &domain.Session{Id: "20240102-000000-0001"}
	s.AppendUser("上次问的问题")
	call := domain.ToolCall{Id: "c1", Name: "Bash", Arguments: `{"command":"go build ./..."}`}
	s.AppendAssistant("上次的回答", "上次的思考", []domain.ToolCall{call})
	s.AppendToolResult(call, "命令输出的那几行")

	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	m.width = 80
	m.quiet = true
	text := stripANSI(strings.Join(m.historyLines(s), "\n"))

	for _, banned := range []string{"上次的思考", "命令输出的那几行"} {
		if strings.Contains(text, banned) {
			t.Fatalf("精简显示下不该出现 %q:\n%s", banned, text)
		}
	}
	for _, want := range []string{"上次问的问题", "上次的回答", "Bash"} {
		if !strings.Contains(text, want) {
			t.Fatalf("精简显示下还得留着 %q:\n%s", want, text)
		}
	}
}

// 恢复之后打进历史区的那一段：历史在前，恢复说明垫在最后，随时看得见
func TestResumeText(t *testing.T) {
	store := session.NewStore(filepath.Join(t.TempDir(), "sessions"))
	s := domain.NewSession(".")
	s.AppendUser("那次聊到的地方")
	s.AppendAssistant("那次给的回答", "", nil)

	m := newModel(config.GlobalConfig{}, store, ".", domain.NewSession("."))
	m.width = 80
	text := stripANSI(m.resumeText(s))

	if i, j := strings.Index(text, "那次聊到的地方"), strings.Index(text, "已恢复会话"); i < 0 || j < 0 || i > j {
		t.Fatalf("历史该在恢复说明前面:\n%s", text)
	}
	for _, want := range []string{"那次给的回答", "共 2 条消息", s.Id} {
		if !strings.Contains(text, want) {
			t.Fatalf("恢复说明里少了 %q:\n%s", want, text)
		}
	}
}

// /resume 不带参数的列表：最近的在最前，当前会话要标出来，会话标题跟在后面
func TestResumeListText(t *testing.T) {
	cur := &domain.Session{Id: "20240102-000000-0001"}
	infos := []session.Info{
		{Id: "20240103-000000-0002", Title: "给恢复会话列表加标题", Size: 2048, ModTime: time.Date(2024, 1, 3, 9, 30, 0, 0, time.UTC)},
		{Id: cur.Id, Size: 512, ModTime: time.Date(2024, 1, 2, 9, 30, 0, 0, time.UTC)},
	}
	text := stripANSI(resumeListText(infos, cur, 76))

	for _, want := range []string{"共 2 个", "1. 20240103-000000-0002", "2K", "512B", "/resume 序号", "给恢复会话列表加标题"} {
		if !strings.Contains(text, want) {
			t.Fatalf("列表里应该有 %q:\n%s", want, text)
		}
	}
	if !strings.Contains(text, "▸ 2. "+cur.Id) {
		t.Fatalf("当前会话应该标出来:\n%s", text)
	}

	// 一条都没有时给句提示，不要打出空列表
	empty := stripANSI(resumeListText(nil, cur, 76))
	if !strings.Contains(empty, "还没有存过的会话") {
		t.Fatalf("空列表的说明不对: %s", empty)
	}
}

// 老会话没有标题，列表里也不该留一截空档或者打个占位符出来
func TestResumeListTextWithoutTitle(t *testing.T) {
	infos := []session.Info{
		{Id: "20240103-000000-0002", Size: 2048, ModTime: time.Date(2024, 1, 3, 9, 30, 0, 0, time.UTC)},
	}
	text := stripANSI(resumeListText(infos, nil, 76))
	line := ""
	for _, l := range strings.Split(text, "\n") {
		if strings.Contains(l, "20240103-000000-0002") {
			line = l
		}
	}
	if !strings.HasSuffix(strings.TrimRight(line, " "), "2K") {
		t.Fatalf("没标题时行尾就是大小，实际 %q", line)
	}
}

// 启动时会话目录指向用户目录下按工作区分的那一层，项目里留着的老会话顺手迁过来
func TestOpenStoreUsesWorkspaceDir(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows 上 os.UserHomeDir 读的是这个
	work := t.TempDir()

	// 老版本把它存在项目里的 .agent/sessions
	legacyStore := session.NewStore(session.LegacyDir(work))
	old := domain.NewSession(work)
	old.AppendUser("老会话说的话")
	if err := legacyStore.Save(old); err != nil {
		t.Fatalf("准备老会话失败: %v", err)
	}

	store, notice, err := openStore(work)
	if err != nil {
		t.Fatalf("准备会话目录失败: %v", err)
	}
	root := filepath.Join(home, ".agent-dev", "sessions")
	if !strings.HasPrefix(store.Dir, root+string(filepath.Separator)) {
		t.Fatalf("会话目录应该在用户目录下的 %s 里，实际 %s", root, store.Dir)
	}
	if store.Dir == session.LegacyDir(work) {
		t.Fatal("不该再往项目目录里写会话")
	}
	if !strings.Contains(notice, "1 个老会话") {
		t.Fatalf("应该说明迁了几个老会话: %q", notice)
	}

	// 迁过来的会话 /resume 能看到，原件也还在
	infos, err := store.ListInfo()
	if err != nil || len(infos) != 1 || infos[0].Id != old.Id {
		t.Fatalf("迁移后的会话没出现在列表里: %v %v", infos, err)
	}
	if _, err := os.Stat(filepath.Join(session.LegacyDir(work), old.Id+".json")); err != nil {
		t.Fatalf("老文件应该留着: %v", err)
	}
	// 工作区信息写下来了，说明记的是哪个目录
	if _, err := os.Stat(filepath.Join(store.Dir, "workspace.json")); err != nil {
		t.Fatalf("工作区信息没写出来: %v", err)
	}

	// 第二次启动没东西可迁，也就不再提示
	if _, notice, err := openStore(work); err != nil || notice != "" {
		t.Fatalf("第二次启动不该再提示: %q %v", notice, err)
	}

	// 换个目录启动就是另一个工作区，看不到这边的会话
	other, _, err := openStore(t.TempDir())
	if err != nil {
		t.Fatalf("准备另一个工作区失败: %v", err)
	}
	if infos, err := other.ListInfo(); err != nil || len(infos) != 0 {
		t.Fatalf("别的工作区不该看到这里的会话: %v %v", infos, err)
	}
}

// writeTestImage 在临时目录里造一张假 PNG：认图片只看文件头，内容不用是真图
func writeTestImage(t *testing.T, name string, size int) string {
	t.Helper()
	dir := t.TempDir()
	path := filepath.Join(dir, name)
	raw := append([]byte("\x89PNG\r\n\x1a\n"), make([]byte, max(size-8, 0))...)
	if err := os.WriteFile(path, raw, 0o644); err != nil {
		t.Fatalf("造图片失败: %v", err)
	}
	return path
}

// 把图片文件拖进终端（终端贴进来的是路径）就当成附图，别当文字塞进输入框
func TestPasteImagePath(t *testing.T) {
	path := writeTestImage(t, "shot.png", 2048)
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))

	got, _ := m.Update(tea.PasteMsg{Content: path})
	after := got.(model)
	if len(after.attachments) != 1 {
		t.Fatalf("应该挂上一张图，实际 %d 张", len(after.attachments))
	}
	if after.input.Value() != "" {
		t.Fatalf("图片路径不该变成输入框里的文字: %q", after.input.Value())
	}
	if after.attachments[0].label != "shot.png" {
		t.Fatalf("名字应该用文件名，实际 %q", after.attachments[0].label)
	}
	if after.attachments[0].img.Size() != 2048 {
		t.Fatalf("图片大小不对: %d", after.attachments[0].img.Size())
	}

	// 输入框上方那行要能看出带了几张
	if view := stripANSI(after.View().Content); !strings.Contains(view, "附图") {
		t.Fatalf("界面上该显示附图那行:\n%s", view)
	}
}

// 普通文字照旧贴进输入框；带引号的路径、file:// 地址也要认得出
func TestPasteTextAndQuotedPaths(t *testing.T) {
	path := writeTestImage(t, "我的 截图.png", 512)

	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	got, _ := m.Update(tea.PasteMsg{Content: "帮我看下这段代码"})
	after := got.(model)
	if len(after.attachments) != 0 {
		t.Fatalf("普通文字不该变成附图: %+v", after.attachments)
	}
	if !strings.Contains(after.input.Value(), "帮我看下这段代码") {
		t.Fatalf("文字该贴进输入框: %q", after.input.Value())
	}

	// 路径带空格时终端会加引号
	if atts, _ := imagesFromPaste(`"` + path + `"`); len(atts) != 1 {
		t.Fatalf("带引号的路径应该认得出来: %+v", atts)
	}
	// 有的终端给的是 file:// 地址
	if atts, _ := imagesFromPaste("file://" + filepath.ToSlash(path)); len(atts) != 1 {
		t.Fatalf("file:// 地址应该认得出来: %+v", atts)
	}
	// 存在但压根不是图片的路径：还是当文字
	notImage := filepath.Join(t.TempDir(), "main.go")
	if err := os.WriteFile(notImage, []byte("package main"), 0o644); err != nil {
		t.Fatalf("准备文件失败: %v", err)
	}
	if atts, problem := imagesFromPaste(notImage); len(atts) != 0 || problem != "" {
		t.Fatalf("不是图片就别当附图: %+v %q", atts, problem)
	}
	// 认得出是图片、但太大读不了：要说一句，不能默默当成文字贴进输入框
	huge := writeTestImage(t, "huge.png", domain.MaxImageBytes+1)
	if atts, problem := imagesFromPaste(huge); len(atts) != 0 || !strings.Contains(problem, "太大") {
		t.Fatalf("太大的图片该提示一句: %+v %q", atts, problem)
	}
}

// 附图之后按 Esc 清空，免得下一句提问莫名带上一张图
func TestEscapeClearsAttachments(t *testing.T) {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	got, _ := m.Update(tea.PasteMsg{Content: writeTestImage(t, "a.png", 64)})
	m = got.(model)
	if len(m.attachments) != 1 {
		t.Fatalf("还没挂上: %+v", m.attachments)
	}

	got, _ = m.handleKey(tea.KeyPressMsg(tea.Key{Code: tea.KeyEsc}))
	if after := got.(model); len(after.attachments) != 0 {
		t.Fatalf("Esc 之后该清空: %+v", after.attachments)
	}
	if view := stripANSI(got.(model).View().Content); strings.Contains(view, "附图") {
		t.Fatalf("清空之后界面上不该还挂着附图那行:\n%s", view)
	}
}

// 只贴图不写字也能发：替用户说一句"看看这张图"，图片跟着提问一起走
func TestSubmitImageOnly(t *testing.T) {
	if got := imagePrompt(1); !strings.Contains(got, "这张图") {
		t.Fatalf("一张图的说法不对: %q", got)
	}
	if got := imagePrompt(3); !strings.Contains(got, "3 张图") {
		t.Fatalf("多张图的说法不对: %q", got)
	}

	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	got, _ := m.Update(tea.PasteMsg{Content: writeTestImage(t, "a.png", 64)})
	m = got.(model)

	// 配置是空的，provider 建不起来：这里只关心"图片被取走了、不会再跟着下一句"
	got, _ = m.submit()
	after := got.(model)
	if len(after.attachments) != 0 {
		t.Fatalf("发出去之后附件该清空: %+v", after.attachments)
	}
	if after.input.Value() != "" {
		t.Fatalf("输入框该是空的: %q", after.input.Value())
	}
}

// 一张提问最多带 maxAttachments 张，到上限要提示，不是默默攒着
func TestAttachmentLimit(t *testing.T) {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	atts := make([]imageAttachment, 0, maxAttachments+1)
	for i := 0; i <= maxAttachments; i++ {
		img, err := domain.NewImage([]byte("\x89PNG\r\n\x1a\n")) // 零字节的图不要
		if err != nil {
			t.Fatalf("造图片失败: %v", err)
		}
		atts = append(atts, imageAttachment{label: strconv.Itoa(i) + ".png", img: img})
	}

	got, cmd := m.attachImages(atts)
	if n := len(got.(model).attachments); n != maxAttachments {
		t.Fatalf("应该收到 %d 张，实际 %d 张", maxAttachments, n)
	}
	if cmd == nil {
		t.Fatal("到上限该提示一句")
	}
}

// Ctrl+V / Alt+V 交到我们手里时去剪贴板取一次。终端自己接走 Ctrl+V 时这个键根本到不了，
// 所以这里只验"按下去会去取"，真去读剪贴板是另一个 goroutine 的事
func TestClipboardKeysAskForImage(t *testing.T) {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	for _, mod := range []tea.KeyMod{tea.ModCtrl, tea.ModAlt} {
		got, cmd := m.handleKey(tea.KeyPressMsg(tea.Key{Code: 'v', Mod: mod}))
		if cmd == nil {
			t.Fatalf("%v+v 应该去剪贴板取东西", mod)
		}
		if n := len(got.(model).attachments); n != 0 {
			t.Fatalf("取东西是异步的，这一刻不该凭空多出 %d 张", n)
		}
	}
}

// 剪贴板里是文字时，Ctrl+V 就是普通粘贴：插到光标处，别当成附图、也别拿"没有图片"打回去
func TestClipboardTextPastesIntoInput(t *testing.T) {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))

	got, _ := m.Update(clipboardPasteMsg{text: "看看这段"})
	after := got.(model)
	if after.input.Value() != "看看这段" {
		t.Fatalf("文字该贴进输入框: %q", after.input.Value())
	}
	if len(after.attachments) != 0 {
		t.Fatalf("文字不该变成附图: %+v", after.attachments)
	}
}

// 确认框里按 Ctrl+V 贴进来的文字，当成"拒绝并说明"的内容
func TestClipboardTextPatchesApprovalFeedback(t *testing.T) {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	m.state = stateApproval
	m.approval = &approvalState{req: domain.ApprovalRequest{Tool: "write_file"}}

	got, _ := m.Update(clipboardPasteMsg{text: "别动这个文件"})
	after := got.(model)
	if !after.approval.typing {
		t.Fatal("贴了字就该进说明输入")
	}
	if string(after.approval.feedback) != "别动这个文件" {
		t.Fatalf("说明内容不对: %q", string(after.approval.feedback))
	}
	if after.input.Value() != "" {
		t.Fatalf("输入框不该被改到: %q", after.input.Value())
	}
}

// 技能名也能补全：候选里名字前面带上 /，跟内置命令摆在同一条链上
func TestSuggestSkills(t *testing.T) {
	skills := []core.Skill{
		{Name: "add-disease", Desc: "添加病种"},
		{Name: "add-scheduled-task", Desc: "开发定时任务"},
		{Name: "research", Desc: "查资料"},
	}
	cases := map[string]string{
		"/add":         "/add-disease,/add-scheduled-task",
		"/res":         "/research",
		"/ADD":         "/add-disease,/add-scheduled-task", // 技能名是用户自己起的，不该逼他记大小写
		"/add-disease": "",                                 // 已经打全了
		"/new":         "",                                 // 内置命令不在这张表里
		"/add 加个病种":    "",                                 // 开始写参数了
		"":             "",
		"你好":           "",
	}
	for input, want := range cases {
		var got []string
		for _, spec := range suggestSkills(input, skills) {
			got = append(got, spec.name)
		}
		if strings.Join(got, ",") != want {
			t.Fatalf("%q 的技能候选是 %v，应该是 %q", input, got, want)
		}
	}
}

// 补全候选里内置命令永远排在技能前面：打一个 / 时第一项还得是 /help，
// 不会因为装了几百个技能就把熟悉的命令挤走
func TestSuggestAllKeepsBuiltinsFirst(t *testing.T) {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	m.skills = []core.Skill{{Name: "aaa-skill", Desc: "排在最前面的技能"}}
	m.input.SetValue("/")
	specs := m.suggestAll()
	if len(specs) != len(commandSpecs)+1 {
		t.Fatalf("候选数不对: %d", len(specs))
	}
	if specs[0].name != "/help" {
		t.Fatalf("第一项应该是 /help，实际 %q", specs[0].name)
	}
	if last := specs[len(specs)-1].name; last != "/aaa-skill" {
		t.Fatalf("技能应该接在命令后面，实际最后一项 %q", last)
	}
}

// / 开头、后面全是名字里该有的字符，才算"想打命令"；带路径分隔符的照旧当提问
func TestLooksLikeCommand(t *testing.T) {
	cases := map[string]bool{
		"/zzz":         true,
		"/add-disease": true,
		"/a_b":         true,
		"/技能名":         true,
		"/":            false,
		"":             false,
		"/tmp/foo.log": false,
		"/a.b":         false,
		"hello":        false,
	}
	for input, want := range cases {
		if got := looksLikeCommand(input); got != want {
			t.Fatalf("%q 应该 %v，实际 %v", input, want, got)
		}
	}
}

// "/技能名 补充说明" 要拆成技能和后面那截；认不出就交回去让调用方报错
func TestParseSkillCall(t *testing.T) {
	skills := []core.Skill{
		{Name: "add-disease", Path: "x"},
		{Name: "add-scheduled-task", Path: "y"},
	}
	if skill, rest, ok := parseSkillCall("/add-disease 加个病种，参考 XX", skills); !ok {
		t.Fatal("应该认出技能")
	} else if skill.Name != "add-disease" || rest != "加个病种，参考 XX" {
		t.Fatalf("拆得不对: %+v %q", skill, rest)
	}
	// 只打命令名，没有补充说明
	if _, rest, ok := parseSkillCall("/add-disease", skills); !ok || rest != "" {
		t.Fatalf("应该认出技能、补充说明为空: %q %v", rest, ok)
	}
	// 前缀能对上两个就不猜
	if _, _, ok := parseSkillCall("/add", skills); ok {
		t.Fatal("前缀对上多个时不该选一个")
	}
	if _, _, ok := parseSkillCall("/zzz", skills); ok {
		t.Fatal("不认识的命令不该认成技能")
	}
}

// 打错的命令要指出相近的，别只甩一句"没有"
func TestUnknownCommandText(t *testing.T) {
	skills := []core.Skill{{Name: "add-disease", Desc: "添加病种"}}
	got := unknownCommandText("/disease", skills)
	if !strings.Contains(got, "相近的：") || !strings.Contains(got, "/add-disease") {
		t.Fatalf("应该列出相近的技能:\n%s", got)
	}
	if !strings.Contains(got, "/skills") {
		t.Fatalf("应该提示 /skills 看装了哪些技能:\n%s", got)
	}
}
