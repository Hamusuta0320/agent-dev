package main

import (
	"context"
	"strings"
	"testing"

	"hamusuta/agent-dev/src/config"
	"hamusuta/agent-dev/src/domain"

	tea "charm.land/bubbletea/v2"
)

// runningModel 摆成"正在跑一轮"：排队相关的按键都只在运行中有意义
func runningModel() model {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	m.width = 80
	m.state = stateRunning
	m.status = "回答中…"
	return m
}

func keyEnter() tea.KeyPressMsg  { return tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}) }
func keyUp() tea.KeyPressMsg     { return tea.KeyPressMsg(tea.Key{Code: tea.KeyUp}) }
func keyDown() tea.KeyPressMsg   { return tea.KeyPressMsg(tea.Key{Code: tea.KeyDown}) }
func keyDelete() tea.KeyPressMsg { return tea.KeyPressMsg(tea.Key{Code: tea.KeyDelete}) }
func keyEsc() tea.KeyPressMsg    { return tea.KeyPressMsg(tea.Key{Code: tea.KeyEscape}) }

// Alt+E 取回排队那条；ctrl+e 是 textarea 的"移到行尾"，不能用
func keyAltE() tea.KeyPressMsg { return tea.KeyPressMsg(tea.Key{Code: 'e', Mod: tea.ModAlt}) }

// Alt+方向键：排队消息的主入口，不占输入框的裸 ↑↓
func keyAltUp() tea.KeyPressMsg   { return tea.KeyPressMsg(tea.Key{Code: tea.KeyUp, Mod: tea.ModAlt}) }
func keyAltDown() tea.KeyPressMsg { return tea.KeyPressMsg(tea.Key{Code: tea.KeyDown, Mod: tea.ModAlt}) }

// 运行中按回车不再丢弃：消息排进队里，输入框腾空等着写下一条
func TestEnqueueWhileRunning(t *testing.T) {
	m := runningModel()
	m.input.SetValue("顺便把测试也补上")

	got, _ := m.handleKey(keyEnter())
	after := got.(model)

	if len(after.queue) != 1 || after.queue[0].text != "顺便把测试也补上" {
		t.Fatalf("应该排进队里: %+v", after.queue)
	}
	if v := after.input.Value(); v != "" {
		t.Fatalf("入队之后输入框该腾空: %q", v)
	}
	if after.state != stateRunning {
		t.Fatal("排队不该打断正在跑的这一轮")
	}
}

// Alt+↑ / Alt+↓ 选排队消息：输入框里写着字也照样能挑，裸 ↑↓ 完全不受影响
func TestQueueSelectWithAltArrows(t *testing.T) {
	m := runningModel()
	m.queue = []queuedPrompt{{text: "第一条"}, {text: "第二条"}}
	// 故意让输入框里有多行内容：这是裸 ↑↓ 必须留给输入框的场景
	m.input.SetValue("第一行\n第二行")

	got, _ := m.handleKey(keyAltUp())
	m = got.(model)
	if m.queueIdx != 1 {
		t.Fatalf("Alt+↑ 该从最后一条选中，实际 %d", m.queueIdx)
	}
	// 选中的过程中输入框的光标一格都不该动
	if line := m.input.Line(); line != 1 {
		t.Fatalf("Alt+↑ 不该挪输入框的光标，实际停在第 %d 行", line)
	}

	got, _ = m.handleKey(keyAltUp())
	m = got.(model)
	if m.queueIdx != 0 {
		t.Fatalf("再 Alt+↑ 该往上挪一格，实际 %d", m.queueIdx)
	}

	got, _ = m.handleKey(keyAltDown())
	if idx := got.(model).queueIdx; idx != 1 {
		t.Fatalf("Alt+↓ 该往下挪一格，实际 %d", idx)
	}

	// 越过最后一条就是取消选中
	got, _ = got.(model).handleKey(keyAltDown())
	if idx := got.(model).queueIdx; idx != -1 {
		t.Fatalf("Alt+↓ 越过最后一条该取消选中，实际 %d", idx)
	}
}

// 队列空着时 Alt+↑↓ 什么都不做，别把键吞掉影响别处
func TestAltArrowsWithoutQueue(t *testing.T) {
	m := runningModel()
	m.input.SetValue("第一行\n第二行")

	got, _ := m.handleKey(keyAltUp())
	after := got.(model)
	if after.queueIdx != -1 {
		t.Fatalf("没排队时不该有选中项: %d", after.queueIdx)
	}
	if line := after.input.Line(); line != 1 {
		t.Fatalf("没排队时 Alt+↑ 不该动输入框光标，实际第 %d 行", line)
	}
}

// Alt+↑↓ 也走同一套挪动逻辑：往上能绕圈，往下从头不进选择
func TestMoveQueueSel(t *testing.T) {
	m := runningModel()
	m.queue = []queuedPrompt{{text: "一"}, {text: "二"}, {text: "三"}}

	// 没选中时 ↓ 不动
	m.moveQueueSel(1)
	if m.queueIdx != -1 {
		t.Fatalf("没选中时 ↓ 该不动，实际 %d", m.queueIdx)
	}

	// ↑ 从最后一条进
	m.moveQueueSel(-1)
	if m.queueIdx != 2 {
		t.Fatalf("↑ 该从最后一条进，实际 %d", m.queueIdx)
	}

	// 再 ↑ 两次绕回最后一圈
	m.moveQueueSel(-1)
	m.moveQueueSel(-1)
	if m.queueIdx != 0 {
		t.Fatalf("应该走到第一条，实际 %d", m.queueIdx)
	}
	m.moveQueueSel(-1)
	if m.queueIdx != 2 {
		t.Fatalf("第一条再 ↑ 该绕回最后一条，实际 %d", m.queueIdx)
	}
}

// 输入框里有多行内容时，↑↓ 归输入框：排着队也不能把挪光标这件事抢走。
// 这是这条功能最容易踩坏别人的地方——用户正在写多行提问，↑ 该往上一行去
func TestQueueArrowsYieldToMultilineInput(t *testing.T) {
	m := runningModel()
	m.queue = []queuedPrompt{{text: "排队的那条"}}
	// SetValue 之后光标在末尾，也就是第二行
	m.input.SetValue("第一行\n第二行")
	if got := m.input.Line(); got != 1 {
		t.Fatalf("准备状态就该停在第二行，实际 %d", got)
	}

	got, _ := m.handleKey(keyUp())
	after := got.(model)
	if after.queueIdx != -1 {
		t.Fatalf("输入框里写着字时 ↑ 不该去选排队消息: %d", after.queueIdx)
	}
	if line := after.input.Line(); line != 0 {
		t.Fatalf("↑ 该把输入框的光标挪到上一行，实际停在第 %d 行", line)
	}

	// ↓ 回到第二行；再按一次也不该跳进排队选择——最后一行按 ↓ 只是没地方去
	got, _ = after.handleKey(keyDown())
	after = got.(model)
	if line := after.input.Line(); line != 1 {
		t.Fatalf("↓ 该把光标挪回第二行，实际停在第 %d 行", line)
	}
	got, _ = after.handleKey(keyDown())
	if idx := got.(model).queueIdx; idx != -1 {
		t.Fatalf("输入框里写着字时 ↓ 也不该进排队选择: %d", idx)
	}
}

// 输入框空着（或者只剩空白）时 ↑↓ 才归排队用
func TestTakesQueueArrows(t *testing.T) {
	cases := []struct {
		name  string
		input string
		state uiState
		queue []queuedPrompt
		want  bool
	}{
		{"空输入框", "", stateRunning, []queuedPrompt{{text: "a"}}, true},
		{"只剩空白", "  \n ", stateRunning, []queuedPrompt{{text: "a"}}, true},
		{"写着字", "正在写", stateRunning, []queuedPrompt{{text: "a"}}, false},
		{"队列空着", "", stateRunning, nil, false},
		{"闲着的时候", "", stateIdle, []queuedPrompt{{text: "a"}}, false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			m := runningModel()
			m.state = c.state
			m.queue = c.queue
			m.input.SetValue(c.input)
			if got := m.takesQueueArrows(); got != c.want {
				t.Fatalf("takesQueueArrows() = %v，应该是 %v", got, c.want)
			}
		})
	}

	// 已经选中了某条就继续管着 ↑↓，跟输入框里有没有字无关
	m := runningModel()
	m.queue = []queuedPrompt{{text: "a"}}
	m.queueIdx = 0
	m.input.SetValue("写着字")
	if !m.takesQueueArrows() {
		t.Fatal("选中之后 ↑↓ 该继续归排队")
	}
}

// 运行中一个字没写就按回车：给一句提示，别往队里塞空条目
func TestEnqueueEmptyWhileRunning(t *testing.T) {
	m := runningModel()

	got, cmd := m.handleKey(keyEnter())
	if after := got.(model); len(after.queue) != 0 {
		t.Fatalf("空输入不该排队: %+v", after.queue)
	}
	if cmd == nil {
		t.Fatal("该提示一句，别默默吞掉这次回车")
	}
}

// 附了图的提问也能排队：图片跟着这一条走，不会挂到下一条上
func TestEnqueueMovesAttachments(t *testing.T) {
	m := runningModel()
	m.attachments = []imageAttachment{{label: "截屏"}}

	m.enqueue()

	if len(m.queue) != 1 || len(m.queue[0].images) != 1 {
		t.Fatalf("图片该跟着这条一起排队: %+v", m.queue)
	}
	if len(m.attachments) != 0 {
		t.Fatalf("排队之后攒着的图该清掉: %+v", m.attachments)
	}
}

// 运行中 ↑↓ 挑排队里的某条：↑ 从最后一条进，↓ 退到底就是取消选中
func TestQueueSelectWithArrows(t *testing.T) {
	m := runningModel()
	m.queue = []queuedPrompt{{text: "第一条"}, {text: "第二条"}}

	got, _ := m.handleKey(keyUp())
	m = got.(model)
	if m.queueIdx != 1 {
		t.Fatalf("↑ 该从最后一条选中，实际 %d", m.queueIdx)
	}

	got, _ = m.handleKey(keyUp())
	m = got.(model)
	if m.queueIdx != 0 {
		t.Fatalf("再 ↑ 该往上挪一格，实际 %d", m.queueIdx)
	}

	// 从第一条再 ↑ 绕回最后一条
	got, _ = m.handleKey(keyUp())
	m = got.(model)
	if m.queueIdx != 1 {
		t.Fatalf("第一条再 ↑ 该绕到最后一条，实际 %d", m.queueIdx)
	}

	// ↓ 走到头就是"不选了"，键盘还给输入框
	got, _ = m.handleKey(keyDown())
	if idx := got.(model).queueIdx; idx != -1 {
		t.Fatalf("↓ 越过最后一条该取消选中，实际 %d", idx)
	}

	// Esc 也能取消选中
	got, _ = m.handleKey(keyUp())
	got, _ = got.(model).handleKey(keyEsc())
	if idx := got.(model).queueIdx; idx != -1 {
		t.Fatalf("Esc 该取消选中，实际 %d", idx)
	}
}

// 一打字就取消选中：否则用户以为在往输入框写，实际焦点还在某条排队消息上
func TestTypingCancelsQueueSelection(t *testing.T) {
	m := runningModel()
	m.queue = []queuedPrompt{{text: "第一条"}}
	m.queueIdx = 0

	got, _ := m.handleKey(tea.KeyPressMsg(tea.Key{Code: 'a', Text: "a"}))
	if idx := got.(model).queueIdx; idx != -1 {
		t.Fatalf("打字之后该取消选中，实际 %d", idx)
	}
}

// 选中某条按 Del 删掉它，连着按能一条条清；删空之后队列那几行就不占了
func TestRemoveQueued(t *testing.T) {
	m := runningModel()
	m.queue = []queuedPrompt{{text: "第一条"}, {text: "第二条"}}

	got, cmd := m.handleKey(keyUp())
	m = got.(model)
	if cmd != nil {
		// ↑ 只是选中，不该往历史区打东西
		t.Fatalf("选中不该打印: %v", cmd)
	}

	got, cmd = m.handleKey(keyDelete())
	m = got.(model)
	if len(m.queue) != 1 || m.queue[0].text != "第一条" {
		t.Fatalf("该删掉选中的第二条: %+v", m.queue)
	}
	if m.queueIdx != 0 {
		t.Fatalf("删完选中项该往前挪一格，实际 %d", m.queueIdx)
	}
	if cmd == nil {
		t.Fatal("删掉之后该在历史区留一句说明")
	}

	got, _ = m.handleKey(keyDelete())
	m = got.(model)
	if len(m.queue) != 0 || m.queueIdx != -1 {
		t.Fatalf("应该删空了: %+v idx=%d", m.queue, m.queueIdx)
	}
	if lines := m.queueLines(); lines != nil {
		t.Fatalf("队列空了不该再占行: %v", lines)
	}
}

// 没选中就别拦 Del：输入框里 Del 是"删光标后面那个字"，拦下来会删错东西
func TestDeleteGoesToInputWhenNothingSelected(t *testing.T) {
	m := runningModel()
	m.queue = []queuedPrompt{{text: "第一条"}}
	m.input.SetValue("正在写的正文")

	got, _ := m.handleKey(keyDelete())
	if after := got.(model); len(after.queue) != 1 {
		t.Fatalf("没选中时 Del 不该删队列: %+v", after.queue)
	}
}

// Alt+E 把排队那条取回输入框改：从队里拿掉，内容和图都放回去
func TestEditQueued(t *testing.T) {
	m := runningModel()
	m.queue = []queuedPrompt{{text: "第一条"}, {text: "第二条要改"}}
	m.queueIdx = 1

	got, _ := m.handleKey(keyAltE())
	m = got.(model)

	if len(m.queue) != 1 || m.queue[0].text != "第一条" {
		t.Fatalf("取回的那条该从队里消失: %+v", m.queue)
	}
	if v := m.input.Value(); v != "第二条要改" {
		t.Fatalf("内容该回到输入框: %q", v)
	}
	if m.queueIdx != -1 {
		t.Fatalf("取回之后不该还选着谁: %d", m.queueIdx)
	}
}

// 输入框里有内容时不覆盖——那会把用户正在写的东西弄丢，只说一句
func TestEditQueuedKeepsTypedInput(t *testing.T) {
	m := runningModel()
	m.queue = []queuedPrompt{{text: "排队的那条"}}
	m.input.SetValue("正在写的正文")

	got, cmd := m.handleKey(keyAltE())
	after := got.(model)

	if v := after.input.Value(); v != "正在写的正文" {
		t.Fatalf("正在写的正文不该被顶掉: %q", v)
	}
	if len(after.queue) != 1 {
		t.Fatalf("取回失败时那条该留在队里: %+v", after.queue)
	}
	if cmd == nil {
		t.Fatal("该提示一句为什么没取回")
	}
}

// 一轮正常跑完，队里排着的下一条自动接上：排队就是为了不用再敲一遍
func TestFinishStartsQueuedPrompt(t *testing.T) {
	m := runningModel()
	m.runner = &runner{ch: make(chan domain.Event, 4)}
	m.queue = []queuedPrompt{{text: "接着改配置"}, {text: "再跑测试"}}

	got, cmd := m.finish(nil)
	after := got.(model)

	if len(after.queue) != 1 || after.queue[0].text != "再跑测试" {
		t.Fatalf("队头那条该被取走: %+v", after.queue)
	}
	if cmd == nil {
		t.Fatal("接上队头那条该产生命令")
	}
}

// 中断或出错时不动队列：那时候该由人决定还要不要发
func TestFinishKeepsQueueOnAbort(t *testing.T) {
	for _, err := range []error{context.Canceled, context.DeadlineExceeded} {
		m := runningModel()
		m.runner = &runner{ch: make(chan domain.Event, 4)}
		m.queue = []queuedPrompt{{text: "还没轮到的那条"}}

		got, _ := m.finish(err)
		if after := got.(model); len(after.queue) != 1 {
			t.Fatalf("%v 之后队列该留着: %+v", err, after.queue)
		}
	}
}

// 排队那几条用黄色标出来：和旁边灰色的提示、绿色的输入标记分得开，
// 选中的那条黄色加粗；操作说明那截仍然是灰的
func TestQueueLinesUseOwnColor(t *testing.T) {
	m := runningModel()
	m.queue = []queuedPrompt{{text: "第一条"}, {text: "第二条"}}
	m.queueIdx = 1

	if queueStyle.Render("x") == hintStyle.Render("x") {
		t.Fatal("排队消息该有自己的颜色，不能和提示色一样")
	}
	if queueSelStyle.Render("x") == queueStyle.Render("x") {
		t.Fatal("选中的那条要能看出区别")
	}

	lines := m.queueLines()
	for i, want := range map[int]string{
		0: queueStyle.Render("排队 2 条"),
		1: queueStyle.Render("  1. 第一条"),
		2: queueSelStyle.Render("▸ 2. 第二条"),
	} {
		if !strings.HasPrefix(lines[i], want) {
			t.Fatalf("第 %d 行的样式不对，想要以 %q 开头，实际 %q", i, want, lines[i])
		}
	}
	// 操作说明那截还是灰的：黄色只用来标"这是排着队的消息"
	if tail := hintStyle.Render(" · Alt+↑↓ 选 · Alt+E 改 · Del 删"); !strings.HasSuffix(lines[0], tail) {
		t.Fatalf("操作说明该是提示色: %q", lines[0])
	}
}

// 排队列表要看得见哪几条在等、哪条被选中，太长的消息压成一行
func TestQueueLines(t *testing.T) {
	m := runningModel()
	m.queue = []queuedPrompt{
		{text: "第一条"},
		{text: "第二条\n这句是换行之后的", images: []imageAttachment{{label: "截屏"}}},
	}
	m.queueIdx = 1

	got := stripANSI(strings.Join(m.queueLines(), "\n"))
	for _, want := range []string{"排队", "2 条", "1. 第一条", "▸ 2. 第二条 …", "（+1 图）"} {
		if !strings.Contains(got, want) {
			t.Fatalf("列表里少了 %q:\n%s", want, got)
		}
	}

	// 再多就滚窗，选中项得保证露头，标题里交代落在第几条到第几条
	m.queue = append(m.queue, queuedPrompt{text: "第三条"}, queuedPrompt{text: "第四条"})
	m.queueIdx = 3
	got = stripANSI(strings.Join(m.queueLines(), "\n"))
	if !strings.Contains(got, "▸ 4. 第四条") {
		t.Fatalf("选中的那条必须在窗口里:\n%s", got)
	}
	if !strings.Contains(got, "2-4") {
		t.Fatalf("窗口不是全部时该交代落在第几条到第几条:\n%s", got)
	}
	if strings.Contains(got, "1. 第一条") {
		t.Fatalf("窗口外面那条不该露头:\n%s", got)
	}

	// 纯图片的提问没有文字，别显示成空行
	m.queue = []queuedPrompt{{images: []imageAttachment{{label: "截屏"}}}}
	m.queueIdx = -1
	if got := stripANSI(strings.Join(m.queueLines(), "\n")); !strings.Contains(got, "(只有图片)") {
		t.Fatalf("纯图片该标出来:\n%s", got)
	}
}

// 排队那几行要挤进输入框上方的槽位里，不能被裁掉——它是刚敲进去、马上要发的东西
func TestQueueLinesSurviveTrimming(t *testing.T) {
	m := runningModel()
	m.height = 24
	m.queue = []queuedPrompt{{text: "排队一"}, {text: "排队二"}}
	m.answer.WriteString("回答预览\n")

	view := stripANSI(m.activeView())
	if !strings.Contains(view, "排队") || !strings.Contains(view, "排队二") {
		t.Fatalf("裁完之后排队那几行该还在:\n%s", view)
	}
}

// 队列里的内容出队后走的是正常提交流程，所以排队写命令、点技能都照常生效。
// 这里验的是"出队时内容真的回到了被 submit 处理的位置"
func TestDequeueRestoresInput(t *testing.T) {
	m := runningModel()
	m.queue = []queuedPrompt{{text: "/help"}, {text: "后面那条"}}

	// buildProvider 会失败（没有配置），于是 submit 走到 startRun 就停住；
	// 但那已经说明 /help 是被当成一次正常输入处理的
	got, _ := m.dequeue()
	after := got.(model)

	if len(after.queue) != 1 || after.queue[0].text != "后面那条" {
		t.Fatalf("只该取走队头一条: %+v", after.queue)
	}
	if v := after.input.Value(); v != "" {
		t.Fatalf("出队之后输入框该是空的: %q", v)
	}
	if after.queueIdx != -1 {
		t.Fatalf("出队后选中该复位: %d", after.queueIdx)
	}
}
