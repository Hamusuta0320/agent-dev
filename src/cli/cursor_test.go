package main

import (
	"fmt"
	"strings"
	"testing"
	"time"

	"hamusuta/agent-dev/src/config"
	"hamusuta/agent-dev/src/domain"

	tea "charm.land/bubbletea/v2"
	"github.com/charmbracelet/x/ansi"
)

// 输入法（IME）的候选框跟的是终端的真光标，不是我们在屏幕上画的那个方块。
// 不报位置的时候终端光标停在渲染器最后一次落笔的地方——界面的最后一行末尾，
// 打拼音时候选框就飞到输入框外面去了。这一组用例拿真终端回放守着"光标在输入框里"。

// boxRows 找出屏幕上的输入框：返回上边框行号和下边框行号，没找到返回 -1
func (hs *harness) boxRows() (top, bottom int) {
	top, bottom = -1, -1
	for i, line := range hs.scr.screen() {
		r := []rune(line)
		if len(r) == 0 {
			continue
		}
		switch r[0] {
		case '╭':
			top = i
		case '╰':
			bottom = i
		}
	}
	return top, bottom
}

// caret 断言"真光标落在输入框里、并且正好在打字的位置上"。
// col 是光标所在行里第几格（从 0 数），wantCol 传 -1 表示不校验列
func (hs *harness) caret(what string, wantRow, wantCol int) {
	hs.t.Helper()
	hs.settle()
	top, bottom := hs.boxRows()
	if top < 0 {
		hs.t.Fatalf("%s：屏幕上没有输入框:\n%s", what, strings.Join(hs.scr.screen(), "\n"))
	}
	if x, y := hs.scr.curX, hs.scr.curY; x != wantCol || y != wantRow {
		hs.t.Fatalf("%s：真光标在 (%d,%d)，应该在 (%d,%d)（输入框在 %d-%d 行）:\n%s",
			what, x, y, wantCol, wantRow, top, bottom, strings.Join(hs.scr.screen(), "\n"))
	}
	if hs.scr.curY <= top || hs.scr.curY >= bottom {
		hs.t.Fatalf("%s：真光标跑出输入框了（输入框 %d-%d 行，光标在第 %d 行）:\n%s",
			what, top, bottom, hs.scr.curY, strings.Join(hs.scr.screen(), "\n"))
	}
}

// 打字：一个字一个字地发按键，和真敲键盘一样
func (hs *harness) typeText(s string) {
	hs.t.Helper()
	for _, r := range s {
		hs.p.Send(tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)}))
		time.Sleep(10 * time.Millisecond)
	}
	time.Sleep(120 * time.Millisecond)
}

// 打字的时候光标要待在输入框里，跟着字往后走
func TestCursorFollowsTypedText(t *testing.T) {
	hs := newHarness(t, 80, 24)
	hs.send(doneMsg{})
	time.Sleep(200 * time.Millisecond)

	// 空输入框：光标在内容第一行的最左边（边框 1 格 + 内边距 1 格）
	top, _ := func() (int, int) { hs.sync(); return hs.boxRows() }()
	hs.caret("空输入框", top+1, 2)

	hs.typeText("hello")
	hs.caret("打了 hello", top+1, 2+len("hello"))

	// 换行之后光标到第二行；光标列是显示宽度，中文占两格
	hs.p.Send(tea.KeyPressMsg{Code: tea.KeyEnter, Mod: tea.ModShift})
	time.Sleep(100 * time.Millisecond)
	hs.typeText("中文")
	hs.caret("第二行的中文", top+2, 2+ansi.StringWidth("中文"))
}

// 往历史区打完东西之后，光标还得回到输入框里——不能留在屏幕上别的地方
func TestCursorStaysInBoxAfterPrint(t *testing.T) {
	for _, size := range []struct{ w, h int }{{80, 24}, {60, 18}, {100, 30}} {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			hs := newHarness(t, size.w, size.h)
			hs.send(doneMsg{})
			time.Sleep(200 * time.Millisecond)

			// /help 一次打几十行，最能把光标甩出去
			hs.typeText("/help")
			hs.p.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
			time.Sleep(400 * time.Millisecond)

			hs.sync()
			top, _ := hs.boxRows()
			hs.caret("/help 打印之后", top+1, 2)

			// 打印完接着打字，光标跟着走
			hs.typeText("再来")
			hs.caret("/help 之后接着打字", top+1, 2+ansi.StringWidth("再来"))
		})
	}
}

// 跑任务时工具结果一行行往上打，光标同样不能跑丢
func TestCursorStaysInBoxWhileRunning(t *testing.T) {
	hs := newHarness(t, 80, 24)
	hs.typeText("hi")
	for i := range 3 {
		hs.send(eventMsg(domain.Event{
			Kind:   domain.EventToolResult,
			Text:   fmt.Sprintf("第 %d 段输出\n第二行\n第三行", i),
			Call:   domain.ToolCall{Id: itoa(i), Name: "Bash"},
			Status: domain.ToolStatusSuccess,
		}))
	}
	hs.sync()
	top, _ := hs.boxRows()
	hs.caret("工具结果打印之后", top+1, 2+len("hi"))
}

// /config 表单里编辑某一格时，光标也要落在那一格里
func TestCursorInConfigForm(t *testing.T) {
	hs := newHarness(t, 90, 26)
	hs.send(doneMsg{})
	time.Sleep(200 * time.Millisecond)

	hs.typeText("/config")
	hs.p.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	time.Sleep(250 * time.Millisecond)

	// 往下走两格（接口地址 → 密钥 → 模型），回车开始编辑
	for range 2 {
		hs.p.Send(tea.KeyPressMsg{Code: tea.KeyDown})
		time.Sleep(60 * time.Millisecond)
	}
	hs.p.Send(tea.KeyPressMsg{Code: tea.KeyEnter})
	time.Sleep(200 * time.Millisecond)
	hs.typeText("deepseek-chat")

	hs.sync()
	lines := hs.scr.screen()
	row := -1
	for i, line := range lines {
		if strings.Contains(line, "模型") && strings.HasPrefix(strings.TrimLeft(line, " "), "▸") {
			row = i
		}
	}
	if row < 0 {
		t.Fatalf("没找到正在编辑的「模型」那行:\n%s", strings.Join(lines, "\n"))
	}
	// 值前面是"两格标记 + 标签 + 两格空格"
	wantCol := 2 + configLabelWidth() + 2 + len("deepseek-chat")
	if x, y := hs.scr.curX, hs.scr.curY; x != wantCol || y != row {
		t.Fatalf("表单里的光标在 (%d,%d)，应该在 (%d,%d):\n%s",
			x, y, wantCol, row, strings.Join(lines, "\n"))
	}
}

// 光标位置本身就是一项要报出去的东西：没打字的地方（确认框）该藏起来
func TestCursorTargetHiddenOutsideTyping(t *testing.T) {
	m := newModel(config.GlobalConfig{Mode: "full"}, nil, ".", domain.NewSession("."))
	m.width, m.height = 80, 24
	m.input.SetValue("写了一半的话")

	if c := m.View().Cursor; c == nil {
		t.Fatal("空闲时该把真光标报给终端，不然输入法不知道往哪儿弹")
	}
	// 空闲/运行中都算"在打字"
	m.state = stateRunning
	if c := m.cursorTarget(); c == nil {
		t.Fatal("跑任务时输入框还在，光标也该在")
	}
	// 确认框里那行光标是手画的竖线，真光标先藏起来
	m.state = stateApproval
	if c := m.cursorTarget(); c != nil {
		t.Fatalf("确认框里不该报真光标，实际 %+v", c)
	}
}
