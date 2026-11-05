package main

import (
	"fmt"
	"io"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hamusuta/agent-dev/src/config"
	"hamusuta/agent-dev/src/domain"
	"hamusuta/agent-dev/src/session"

	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
	"github.com/charmbracelet/x/ansi"
)

// 这个文件用一台"假终端"来验证界面的真实样子。
//
// 光断言 View() 的字符串是不够的：Bubble Tea 在 inline 模式下是按帧高记账、
// 用相对光标去挪屏幕的，View() 完全正确也可能在屏幕上留下残影。所以这里起一个
// 真的 tea.Program，把它的输出回放成"屏幕实际显示什么"，再对着屏幕断言。

// vtScreen 是一个够用的 VT 终端模拟器：只实现这类界面会发出的序列。
type vtScreen struct {
	w, h int
	grid [][]rune
	back []string // 滚出屏幕顶部的行
	curX int
	curY int
}

func newVTScreen(w, h int) *vtScreen {
	s := &vtScreen{w: w, h: h}
	s.grid = make([][]rune, h)
	for i := range s.grid {
		s.grid[i] = blankRow(w)
	}
	return s
}

func blankRow(n int) []rune {
	row := make([]rune, n)
	for i := range row {
		row[i] = ' '
	}
	return row
}

func (s *vtScreen) feed(data []byte) {
	p := ansi.NewParser()
	p.SetHandler(ansi.Handler{
		Print:   s.putRune,
		Execute: s.control,
		HandleCsi: func(cmd ansi.Cmd, params ansi.Params) {
			s.csi(cmd, params)
		},
	})
	p.Parse(data)
}

func (s *vtScreen) putRune(r rune) {
	w := ansi.StringWidth(string(r))
	if w <= 0 {
		w = 1
	}
	if s.curX+w > s.w {
		s.curX = 0
		s.lineFeed()
	}
	s.grid[s.curY][s.curX] = r
	for i := 1; i < w && s.curX+i < s.w; i++ {
		s.grid[s.curY][s.curX+i] = 0 // 宽字符占位
	}
	s.curX += w
}

func (s *vtScreen) control(b byte) {
	switch b {
	case '\r':
		s.curX = 0
	case '\n':
		s.lineFeed()
	case '\b':
		if s.curX > 0 {
			s.curX--
		}
	}
}

func (s *vtScreen) lineFeed() {
	if s.curY >= s.h-1 {
		s.back = append(s.back, strings.TrimRight(s.plain(s.grid[0]), " "))
		copy(s.grid, s.grid[1:])
		s.grid[s.h-1] = blankRow(s.w)
		return
	}
	s.curY++
}

func (s *vtScreen) csi(cmd ansi.Cmd, params ansi.Params) {
	num := func(i, def int) int {
		v, _, ok := params.Param(i, def)
		if !ok || v <= 0 {
			return def
		}
		return v
	}
	switch cmd.Final() {
	case 'A':
		s.curY = max(s.curY-num(0, 1), 0)
	case 'B':
		s.curY = min(s.curY+num(0, 1), s.h-1)
	case 'C':
		s.curX = min(s.curX+num(0, 1), s.w-1)
	case 'D':
		s.curX = max(s.curX-num(0, 1), 0)
	case 'G':
		s.curX = num(0, 1) - 1
	case 'd':
		s.curY = min(num(0, 1)-1, s.h-1)
	case 'H', 'f':
		s.curY = min(num(0, 1)-1, s.h-1)
		s.curX = min(num(1, 1)-1, s.w-1)
	case 'L': // 插入行：从底部往上搬，给光标行腾出空间
		n := num(0, 1)
		for i := s.h - 1; i >= s.curY+n; i-- {
			s.grid[i] = s.grid[i-n]
		}
		for i := s.curY; i < s.curY+n && i < s.h; i++ {
			s.grid[i] = blankRow(s.w)
		}
	case 'M': // 删除行
		n := num(0, 1)
		for i := s.curY; i < s.h; i++ {
			if i+n < s.h {
				s.grid[i] = s.grid[i+n]
			} else {
				s.grid[i] = blankRow(s.w)
			}
		}
	case 'K':
		switch num(0, 0) {
		case 0:
			for i := s.curX; i < s.w; i++ {
				s.grid[s.curY][i] = ' '
			}
		case 1:
			for i := 0; i <= s.curX && i < s.w; i++ {
				s.grid[s.curY][i] = ' '
			}
		case 2:
			s.grid[s.curY] = blankRow(s.w)
		}
	case 'J':
		switch num(0, 0) {
		case 0:
			for i := s.curX; i < s.w; i++ {
				s.grid[s.curY][i] = ' '
			}
			for y := s.curY + 1; y < s.h; y++ {
				s.grid[y] = blankRow(s.w)
			}
		case 1:
			for y := 0; y < s.curY; y++ {
				s.grid[y] = blankRow(s.w)
			}
			for i := 0; i <= s.curX && i < s.w; i++ {
				s.grid[s.curY][i] = ' '
			}
		case 2, 3:
			for y := 0; y < s.h; y++ {
				s.grid[y] = blankRow(s.w)
			}
		}
	case 'm', 'h', 'l', 'r', 'n', 't', 'q', 'p', 's', 'u':
		// 样式、模式、滚动区域之类，跟位置无关
	}
}

// resize 模拟终端改尺寸。宽度变化时做 reflow（重排）：终端会把内容按新宽度
// 重新折行，于是原来一行长的可能变成两行，整个内容的行数就变了。
//
// 这一点很要紧：Windows Terminal（以及多数现代终端）在拖窗口时会 reflow，
// 而程序打印出去的行是按当时的宽度折好的。窗口一窄，这些行被终端拆开，
// 光标被推下去，程序记的行数就和屏幕对不上了——而 inline 模式用的是相对光标，
// 没有绝对定位可以退回。
func (s *vtScreen) resize(nw, nh int) {
	// 1. 把 scrollback 和当前屏幕拼成一串逻辑行（每行都是内容，不是折好的物理行）
	var logical []string
	logical = append(logical, s.back...)
	for _, row := range s.grid {
		logical = append(logical, strings.TrimRight(s.plain(row), " "))
	}

	// 2. 按新宽度重新折行，得到新的物理行
	var physical []string
	for _, line := range logical {
		physical = append(physical, wrapTo(line, nw)...)
	}

	// 3. 从底部对齐：最后 nh 行留作屏幕，其余的进 scrollback
	if len(physical) >= nh {
		s.back = physical[:len(physical)-nh]
		physical = physical[len(physical)-nh:]
	} else {
		pad := make([]string, nh-len(physical))
		physical = append(pad, physical...)
		s.back = nil
	}

	next := make([][]rune, nh)
	for i, line := range physical {
		next[i] = s.fitRow(line, nw)
	}
	s.grid, s.w, s.h = next, nw, nh

	// 4. 程序打印完光标就停在内容的最后一行，reflow 之后它跟着内容走
	s.curY = nh - 1
	if last := strings.TrimRight(s.plain(next[nh-1]), " "); last != "" {
		s.curX = min(ansi.StringWidth(last), nw-1)
	} else {
		s.curX = 0
	}
}

// wrapTo 把一行按宽度折成多行，模拟终端的 reflow
func wrapTo(line string, nw int) []string {
	if nw <= 0 {
		return []string{line}
	}
	var out []string
	var cur strings.Builder
	width := 0
	for _, r := range line {
		w := ansi.StringWidth(string(r))
		if w <= 0 {
			w = 1
		}
		if width+w > nw {
			out = append(out, cur.String())
			cur.Reset()
			width = 0
		}
		cur.WriteRune(r)
		width += w
	}
	out = append(out, cur.String())
	return out
}

func (s *vtScreen) fitRow(text string, nw int) []rune {
	row := blankRow(nw)
	x := 0
	for _, r := range text {
		w := ansi.StringWidth(string(r))
		if w <= 0 {
			w = 1
		}
		if x+w > nw {
			break
		}
		row[x] = r
		x += w
	}
	return row
}

// plain 把一行格子拼成字符串，去掉宽字符的占位格
func (s *vtScreen) plain(row []rune) string {
	var b strings.Builder
	for _, r := range row {
		if r != 0 {
			b.WriteRune(r)
		}
	}
	return b.String()
}

// screen 是可见区域的文本，行尾空格已去掉
func (s *vtScreen) screen() []string {
	out := make([]string, s.h)
	for i, row := range s.grid {
		out[i] = strings.TrimRight(s.plain(row), " ")
	}
	return out
}

// harness 是一个跑起来的真程序 + 一台跟着回放的假终端
type harness struct {
	t   *testing.T
	p   *tea.Program
	out *strings.Builder
	scr *vtScreen
	fed int
}

func newHarness(t *testing.T, w, h int) *harness {
	t.Helper()
	// 回放测试验的是"打印出来的行有没有把界面账算错"，所以默认走详细显示：
	// 思考过程和工具输出都得真的打进历史区才测得到。精简显示另有用例
	return newHarnessWith(t, w, h, false)
}

// newHarnessWith 和 newHarness 一样，quiet 决定这轮回放的是精简显示还是详细显示
func newHarnessWith(t *testing.T, w, h int, quiet bool) *harness {
	return newHarnessStore(t, w, h, quiet, nil)
}

// newHarnessStore 是带会话目录的版本：/resume 这类要读写会话的用例用它，
// 其余用例传 nil 就行
func newHarnessStore(t *testing.T, w, h int, quiet bool, store *session.Store) *harness {
	t.Helper()
	pr, pw := io.Pipe()
	t.Cleanup(func() { _ = pw.Close() })

	// 摆成"正在跑一轮"：下面要验的是渲染，不是真的去连模型。
	// runner 的 channel 没人写，waitEvent 会挂在后台，不影响画面。
	m := newModel(config.GlobalConfig{Mode: "full"}, store, ".", domain.NewSession("."))
	m.quiet = quiet
	m.state = stateRunning
	m.runner = &runner{ch: make(chan domain.Event, 8)}
	m.status = "回答中…"

	out := &strings.Builder{}
	p := tea.NewProgram(&m,
		tea.WithInput(pr),
		tea.WithOutput(out),
		tea.WithEnvironment([]string{"TERM=xterm-256color"}),
		tea.WithoutSignals(),
		tea.WithWindowSize(w, h),
	)
	done := make(chan struct{})
	go func() {
		defer close(done)
		_, _ = p.Run()
	}()
	t.Cleanup(func() {
		p.Quit()
		select {
		case <-done:
		case <-time.After(3 * time.Second):
			t.Error("程序没退出")
		}
	})

	hs := &harness{t: t, p: p, out: out, scr: newVTScreen(w, h)}
	time.Sleep(250 * time.Millisecond) // 等开场白
	return hs
}

// sync 只把新产生的输出喂给假终端，这样可以在中途改终端尺寸
func (hs *harness) sync() {
	hs.t.Helper()
	data := hs.out.String()
	if len(data) > hs.fed {
		hs.scr.feed([]byte(data[hs.fed:]))
		hs.fed = len(data)
	}
}

// snapshot 返回当前屏幕，并断言屏幕上没有留下上一帧的痕迹
func (hs *harness) snapshot(what string) []string {
	hs.t.Helper()
	hs.sync()
	lines := hs.scr.screen()

	var problems []string

	// 1. 同一个东西不该画两遍
	usage, running, idle := 0, 0, 0
	for _, line := range lines {
		if strings.Contains(line, "上下文 ") && strings.Contains(line, "tokens") {
			usage++
		}
		if strings.Contains(line, "Ctrl+C 中断") {
			running++
		}
		if strings.Contains(line, "Ctrl+C 退出") {
			idle++
		}
	}
	if usage > 1 {
		problems = append(problems, fmt.Sprintf("上下文占用出现 %d 次", usage))
	}
	if running > 0 && idle > 0 {
		problems = append(problems, "运行中提示和空闲提示同时在场")
	}

	// 2. 屏幕不能是空的
	empty := true
	for _, line := range lines {
		if strings.TrimSpace(line) != "" {
			empty = false
			break
		}
	}
	if empty {
		problems = append(problems, "屏幕是空的")
	}

	// 3. 框的上下边框各只能有一次，不然就是旧框没擦掉
	top, bottom := 0, 0
	for _, line := range lines {
		trimmed := strings.TrimSpace(line)
		if strings.HasPrefix(trimmed, "╭") {
			top++
		}
		if strings.HasPrefix(trimmed, "╰") {
			bottom++
		}
	}
	if top > 1 || bottom > 1 {
		problems = append(problems, fmt.Sprintf("边框重复（上 %d 个，下 %d 个）", top, bottom))
	}

	if len(problems) > 0 {
		var b strings.Builder
		for i, line := range lines {
			fmt.Fprintf(&b, "%2d|%s\n", i, line)
		}
		hs.t.Fatalf("%s：%s\n%s", what, strings.Join(problems, "；"), b.String())
	}
	return lines
}

func (hs *harness) send(msg tea.Msg) {
	hs.t.Helper()
	hs.p.Send(msg)
	time.Sleep(80 * time.Millisecond)
}

func probeUsage() domain.ContextUsage {
	return domain.ContextUsage{
		PromptTokens: 12000, ContextLimit: 1_000_000,
		SystemTokens: 8200, ToolTokens: 2700, MessageTokens: 1100,
		CompletionTokens: 249,
	}
}

// 帧一旦变高或变矮，inline 渲染就容易把上一帧的残影留在屏幕上。
// 这里把几条会让帧高变的路径都走一遍，每一帧都对着"真屏幕"验。
func TestReplayNoResidueAcrossStates(t *testing.T) {
	for _, size := range []struct{ w, h int }{
		{60, 24}, {80, 30}, {60, 18}, {100, 40},
	} {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			hs := newHarness(t, size.w, size.h)
			hs.snapshot("开场")

			hs.send(eventMsg(domain.Event{Kind: domain.EventUsage, Usage: probeUsage()}))
			hs.send(eventMsg(domain.Event{Kind: domain.EventContent, Text: "第一轮回答"}))
			hs.send(eventMsg(domain.Event{Kind: domain.EventAnswerEnd, Text: "第一轮回答"}))
			hs.snapshot("运行中")

			// 进确认框：帧从主界面高度掉到确认框高度
			hs.send(eventMsg(domain.Event{
				Kind: domain.EventApproval,
				Approval: &domain.ApprovalRequest{
					Tool: "Bash", Kind: domain.ApprovalRunCommand,
					Target: "go test ./...", Detail: "跑测试",
				},
				Reply: make(chan domain.ApprovalAnswer, 1),
			}))
			if view := strings.Join(hs.snapshot("确认框"), "\n"); !strings.Contains(view, "需要确认") {
				t.Fatalf("确认框没显示出来:\n%s", view)
			}

			// 回到运行态：帧又长回来
			hs.send(eventMsg(domain.Event{Kind: domain.EventContent, Text: "继续"}))
			hs.snapshot("确认框之后")

			// 结束：footer 从"中断"切回"发送"，这块最容易留残影
			hs.send(doneMsg{})
			time.Sleep(180 * time.Millisecond)
			if view := strings.Join(hs.snapshot("结束"), "\n"); !strings.Contains(view, "Ctrl+C 退出") {
				t.Fatalf("结束后 footer 没切回空闲态:\n%s", view)
			}
			// 结束之后再来的事件不该被处理，屏幕也不该动
			hs.send(eventMsg(domain.Event{Kind: domain.EventUsage, Usage: probeUsage()}))
			hs.snapshot("结束后又收到事件")
		})
	}
}

// 终端改尺寸之后，插件（ultraviolet）自己承认光标模型在相对光标模式下无法恢复。
// 这里验的是：改完尺寸再继续画，屏幕上不能留下两套界面。
func TestReplayNoResidueAfterResize(t *testing.T) {
	for _, c := range []struct {
		name   string
		w, h   int
		nw, nh int
	}{
		{"变矮", 60, 24, 60, 16},
		{"变高", 60, 18, 60, 26},
		{"变窄", 80, 24, 50, 24},
		{"变宽", 50, 24, 80, 24},
	} {
		t.Run(c.name, func(t *testing.T) {
			hs := newHarness(t, c.w, c.h)
			hs.send(eventMsg(domain.Event{Kind: domain.EventUsage, Usage: probeUsage()}))
			hs.send(eventMsg(domain.Event{Kind: domain.EventContent, Text: "回答"}))
			hs.snapshot("改尺寸前")

			hs.p.Send(tea.WindowSizeMsg{Width: c.nw, Height: c.nh})
			hs.scr.resize(c.nw, c.nh) // 真实终端会先重排，程序只能事后收到通知
			time.Sleep(250 * time.Millisecond)
			hs.snapshot("改尺寸后")

			hs.send(eventMsg(domain.Event{Kind: domain.EventUsage, Usage: probeUsage()}))
			hs.send(doneMsg{})
			time.Sleep(180 * time.Millisecond)
			hs.snapshot("改尺寸后继续")
		})
	}
}

// 帧里任何一行都不能宽到让终端自己折行：终端一折行，程序记的帧高就和屏幕
// 对不上，从那行起整个界面错位。窄终端里最容易超宽的是上下文占用那行——
// 它的第一项（总数）在 fitItems 里是不受宽度限制的。
func TestComposeClampsEveryLine(t *testing.T) {
	for _, width := range []int{20, 24, 30, 50, 80} {
		m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
		m.width = width
		m.usage = probeUsage()
		m.lastAnswer = strings.Repeat("很长的上一轮回答", 8) // 顺手压下空闲槽位的长内容

		frame := compose(m.frameLines(), width)
		for i, line := range strings.Split(frame, "\n") {
			if w := lipgloss.Width(line); w > width-1 {
				t.Fatalf("宽 %d：第 %d 行 %d 格，宽过终端（%d）会让终端自己折行: %q",
					width, i, w, width-1, stripANSI(line))
			}
		}

		// 四种状态都得过这条线
		for name, state := range map[string]uiState{
			"空闲":   stateIdle,
			"运行中":  stateRunning,
			"确认框":  stateApproval,
			"配置表单": stateConfig,
		} {
			m.state = state
			m.approval = &approvalState{
				req:   domain.ApprovalRequest{Tool: "Bash", Kind: domain.ApprovalRunCommand, Target: "go test ./..."},
				reply: make(chan domain.ApprovalAnswer, 1),
			}
			f := compose(m.frameLines(), width)
			for i, line := range strings.Split(f, "\n") {
				if w := lipgloss.Width(line); w > width-1 {
					t.Fatalf("宽 %d %s：第 %d 行 %d 格宽过终端: %q",
						width, name, i, w, stripANSI(line))
				}
			}
		}
	}
}

// 这是真正复现出问题来的那个场景：终端矮、历史又长。
//
// 曾经的现象：50×18 的终端上聊几轮之后，屏幕上叠出 4 份状态行、3 份输入框、
// 4 份上下文占用——因为帧（17 行）几乎占满 18 行的屏幕，每次 print 往上插行时
// 算出来的滚动量全错。修法是让帧高随终端收缩（见 activeRows），这里守着它。
func TestReplayShortTerminalLongHistory(t *testing.T) {
	for _, size := range []struct{ w, h int }{
		{50, 18}, {60, 20}, {60, 16}, {80, 24},
	} {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			hs := newHarness(t, size.w, size.h)
			hs.send(eventMsg(domain.Event{Kind: domain.EventUsage, Usage: probeUsage()}))

			// 连续攒很多轮，让历史远超屏幕高度
			for i := range 10 {
				hs.p.Send(eventMsg(domain.Event{Kind: domain.EventContent, Text: "回答片段"}))
				time.Sleep(80 * time.Millisecond)
				hs.p.Send(eventMsg(domain.Event{
					Kind:   domain.EventToolResult,
					Text:   fmt.Sprintf("结果 %d", i),
					Call:   domain.ToolCall{Id: fmt.Sprintf("%d", i), Name: "Bash"},
					Status: domain.ToolStatusSuccess,
				}))
				time.Sleep(80 * time.Millisecond)
				hs.snapshot(fmt.Sprintf("第 %d 轮结束", i))
			}

			// 最后收尾那一下最容易错位：帧从"运行中"切到"空闲"
			hs.send(doneMsg{})
			time.Sleep(200 * time.Millisecond)
			hs.snapshot("结束")
		})
	}
}

// 大段工具输出（一次打十几行）在矮终端上是"帧被顶出屏幕"的重灾区。
//
// 渲染器往上插行时，是先按帧顶在屏幕上的位置往上数 offset 行、再去插的。offset
// 一旦超过帧顶那一行，光标就从屏幕顶上顶出去了，插行落点全错，旧帧的下半截留在
// 屏幕上再也不会被擦——屏幕上同时出现两份输入框、两行上下文占用（见 printRows）。
// 这里拿一个真终端回放守着它。
func TestReplayBigPrintKeepsFrame(t *testing.T) {
	for _, size := range []struct{ w, h int }{
		{100, 16}, {80, 20}, {60, 18}, {80, 30},
	} {
		t.Run(fmt.Sprintf("%dx%d", size.w, size.h), func(t *testing.T) {
			hs := newHarness(t, size.w, size.h)
			hs.send(eventMsg(domain.Event{Kind: domain.EventUsage, Usage: probeUsage()}))

			// 连着三段"标题行 + 12 行输出"的工具结果
			for i := range 3 {
				call := domain.ToolCall{
					Id:        itoa(i),
					Name:      "Bash",
					Arguments: `{"command":"go test ./... 2>&1","intent":"跑测试"}`,
				}
				hs.send(eventMsg(domain.Event{Kind: domain.EventToolCall, Call: call}))

				body := make([]string, 0, printToolResultLines)
				for j := range printToolResultLines {
					body = append(body, fmt.Sprintf("第%d段输出第%d行", i, j))
				}
				hs.send(eventMsg(domain.Event{
					Kind:   domain.EventToolResult,
					Text:   strings.Join(body, "\n"),
					Call:   call,
					Status: domain.ToolStatusSuccess,
				}))

				lines := hs.snapshot(fmt.Sprintf("第 %d 段输出之后", i))
				// 最后一行输出得真的打出来：切块不能把内容切丢
				last := fmt.Sprintf("第%d段输出第%d行", i, printToolResultLines-1)
				if !strings.Contains(strings.Join(lines, "\n"), last) {
					t.Fatalf("最后一行输出 %q 没显示出来:\n%s", last, strings.Join(lines, "\n"))
				}
			}

			// 收尾那一刻帧从"运行中"切到"空闲"，再验一次
			hs.send(doneMsg{})
			time.Sleep(200 * time.Millisecond)
			hs.snapshot("结束")
		})
	}
}

// 精简显示（默认就是它）：历史区只留正文，思考过程和工具输出的内容都不打，
// 工具调用还是留一行标题，知道刚才在跑什么。
func TestQuietHidesReasoningAndToolOutput(t *testing.T) {
	hs := newHarnessWith(t, 80, 30, true)
	hs.send(eventMsg(domain.Event{Kind: domain.EventReasoning, Text: "先想想要怎么做"}))
	hs.send(eventMsg(domain.Event{Kind: domain.EventContent, Text: "正文第一段"}))
	hs.send(eventMsg(domain.Event{
		Kind:   domain.EventToolResult,
		Text:   "命令输出的那几行",
		Call:   domain.ToolCall{Id: "1", Name: "Bash", Arguments: `{"command":"go build ./..."}`},
		Status: domain.ToolStatusSuccess,
	}))
	hs.send(doneMsg{})
	time.Sleep(200 * time.Millisecond)

	view := strings.Join(hs.snapshot("精简显示"), "\n")
	for _, banned := range []string{"先想想要怎么做", "命令输出的那几行"} {
		if strings.Contains(view, banned) {
			t.Fatalf("精简显示下不该出现 %q:\n%s", banned, view)
		}
	}
	if !strings.Contains(view, "正文第一段") {
		t.Fatalf("正文本体得留着:\n%s", view)
	}
	if !strings.Contains(view, "Bash") {
		t.Fatalf("工具调用那行标题得留着，不然不知道在跑什么:\n%s", view)
	}
}

// 高频打印：回答和工具结果接连打进历史区的时候，插行和重绘是交替发生的。
// 这里连着打很多次，确认不会攒出一堆重复内容。
func TestReplayNoResidueUnderRapidPrint(t *testing.T) {
	hs := newHarness(t, 70, 26)
	hs.send(eventMsg(domain.Event{Kind: domain.EventUsage, Usage: probeUsage()}))

	for i := range 12 {
		hs.p.Send(eventMsg(domain.Event{Kind: domain.EventContent, Text: fmt.Sprintf("片段 %d", i)}))
		time.Sleep(8 * time.Millisecond)
		hs.p.Send(eventMsg(domain.Event{
			Kind:   domain.EventToolResult,
			Text:   fmt.Sprintf("结果 %d", i),
			Call:   domain.ToolCall{Id: fmt.Sprintf("%d", i), Name: "Bash"},
			Status: domain.ToolStatusSuccess,
		}))
		time.Sleep(8 * time.Millisecond)
	}
	time.Sleep(300 * time.Millisecond)
	hs.snapshot("高频打印后")

	// 历史区的内容要真的出现在屏幕上（上方可以滚出去了，至少最后几条得在）
	view := strings.Join(hs.snapshot("检查内容"), "\n")
	if !strings.Contains(view, "结果 11") {
		t.Fatalf("最后一条工具结果没显示出来:\n%s", view)
	}
}

// /resume 恢复会话之后要把这段历史重新打一遍：屏幕上真的能看到上次的提问、思考和回答，
// 同时帧（输入框、底部提示）不能因为这一大段打印叠出残影
func TestReplayResumeRendersHistory(t *testing.T) {
	store := session.NewStore(filepath.Join(t.TempDir(), "sessions"))
	old := domain.NewSession(".")
	old.AppendUser("上次问的那个问题")
	call := domain.ToolCall{Id: "c1", Name: "Bash", Arguments: `{"command":"go build ./...","intent":"编译一下"}`}
	old.AppendAssistant("上次给的回答", "上次的思考", []domain.ToolCall{call})
	old.AppendToolResult(call, "exit_code: 0\n编译过了")
	if err := store.Save(old); err != nil {
		t.Fatalf("准备会话失败: %v", err)
	}

	hs := newHarnessStore(t, 80, 30, false, store)
	hs.send(doneMsg{}) // 这一轮跑完了，回到空闲才能敲命令
	time.Sleep(200 * time.Millisecond)

	// 敲 /resume 1 回车
	for _, r := range "/resume 1" {
		hs.p.Send(tea.KeyPressMsg(tea.Key{Code: r, Text: string(r)}))
		time.Sleep(10 * time.Millisecond)
	}
	hs.p.Send(tea.KeyPressMsg(tea.Key{Code: tea.KeyEnter}))
	time.Sleep(300 * time.Millisecond)

	view := strings.Join(hs.snapshot("恢复会话后"), "\n")
	for _, want := range []string{"上次问的那个问题", "上次的思考", "上次给的回答", "编译过了", "已恢复会话"} {
		if !strings.Contains(view, want) {
			t.Fatalf("恢复后屏幕上少了 %q:\n%s", want, view)
		}
	}
}
