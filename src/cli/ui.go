package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"

	"hamusuta/agent-dev/src/config"
	"hamusuta/agent-dev/src/core"
	"hamusuta/agent-dev/src/domain"
	"hamusuta/agent-dev/src/provider"
	"hamusuta/agent-dev/src/session"
	"hamusuta/agent-dev/src/tool"

	"charm.land/bubbles/v2/key"
	"charm.land/bubbles/v2/spinner"
	"charm.land/bubbles/v2/textarea"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

type uiState int

const (
	// stateIdle 是等用户输入的状态
	stateIdle uiState = iota
	// stateRunning 是模型或工具在跑的状态
	stateRunning
)

// 运行中在底部预览几行，结束后打到上方历史区；太长的内容只显示开头几行，
// 完整内容在会话文件里
const (
	previewReasoningLines = 4
	previewAnswerLines    = 10
	printReasoningLines   = 30
	printToolResultLines  = 12
	// toolArgRunes 是一行工具参数摘要最多保留的字符数
	toolArgRunes = 80
	// toolErrRunes 是失败原因最多保留的字符数
	toolErrRunes = 200
	// toolMarkDone 是工具调用结束后贴在标题行最前面的圆圈，颜色表示成败
	toolMarkDone = "●"
)

var (
	bannerStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("212"))
	promptStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("114"))
	statusStyle  = lipgloss.NewStyle().Foreground(lipgloss.Color("212"))
	thinkStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("243"))
	toolStyle    = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("111"))
	toolOKStyle  = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("114"))
	toolResStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("245"))
	errStyle     = lipgloss.NewStyle().Foreground(lipgloss.Color("203"))
	hintStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("240"))
	boxStyle     = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("238")).
			Padding(0, 1)
)

// 从模型那边流回来的消息
type eventMsg domain.Event

// doneMsg 表示这一轮跑完了
type doneMsg struct{ err error }

// runner 在后台跑一轮 core.Loop，把过程事件通过 channel 送给 UI。
// err 要在 close(ch) 之前写好，UI 读到 channel 关闭时才有确定的 err。
type runner struct {
	ch  chan domain.Event
	err error
}

type model struct {
	cfg     config.GlobalConfig
	store   *session.Store
	workdir string
	cur     *domain.Session

	input  textarea.Model
	spin   spinner.Model
	state  uiState
	status string

	// toolSpin 是工具调用标题行前面那个转圈的圆圈，和底部状态行的转圈各转各的
	toolSpin spinner.Model
	// pending 是正在执行、还没出结果的工具调用（渲染好的行，不含最前面的圆圈）。
	// 调用期间它顶在底部转圈，结果回来才带着状态色打进上方历史区——
	// 历史区打出去的行改不了，颜色得等结果才知道
	pending []string

	cancel context.CancelFunc
	runner *runner

	// 流式输出先攒在这里，边攒边在底部预览，这一轮结束再整段打到上方。
	// 用指针而不是值：Bubble Tea 的 model 按值复制，值类型的 Builder 写过之后再被复制、写入会 panic
	// （illegal use of non-zero Builder copied by value）；指针复制后仍指向同一个 Builder，
	// 且只有 UI goroutine 会写它，不存在并发问题。
	reasoning      *strings.Builder
	answer         *strings.Builder
	reasoningShown bool

	width    int
	quitting bool
}

func newModel(cfg config.GlobalConfig, store *session.Store, workdir string, cur *domain.Session) model {
	input := textarea.New()
	input.Placeholder = "问点什么，/help 看命令"
	input.ShowLineNumbers = false
	input.MaxHeight = 10
	input.SetHeight(3)
	input.Prompt = "" // 输入框前面不放箭头，边框已经够用了
	styles := input.Styles()
	styles.Focused.Prompt = promptStyle
	styles.Focused.CursorLine = lipgloss.NewStyle()
	styles.Blurred = styles.Focused
	input.SetStyles(styles)
	// Enter 留给"发送"，换行改用 Shift+Enter（终端不区分修饰键时 Ctrl+J 也能换行）
	input.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("shift+enter", "ctrl+j"))
	input.SetWidth(78)
	input.Focus()

	spin := spinner.New()
	spin.Spinner = spinner.Dot
	spin.Style = statusStyle

	toolSpin := spinner.New()
	// 调用中的圆圈：Dot 的每一帧自带一个尾随空格，这里去掉，圆圈后面统一补空格
	// （不能直接改 spinner.Dot.Frames，那是包级变量共享的切片）
	frames := make([]string, len(spinner.Dot.Frames))
	for i, f := range spinner.Dot.Frames {
		frames[i] = strings.TrimRight(f, " ")
	}
	toolSpin.Spinner = spinner.Spinner{Frames: frames, FPS: spinner.Dot.FPS}
	toolSpin.Style = toolStyle

	return model{
		cfg:       cfg,
		store:     store,
		workdir:   workdir,
		cur:       cur,
		input:     input,
		spin:      spin,
		toolSpin:  toolSpin,
		state:     stateIdle,
		reasoning: &strings.Builder{},
		answer:    &strings.Builder{},
		width:     80,
	}
}

func (m model) Init() tea.Cmd {
	banner := strings.Join([]string{
		bannerStyle.Render("agent-dev") + hintStyle.Render("  会话 "+m.cur.Id),
		hintStyle.Render("Enter 发送 · Shift+Enter 换行 · Ctrl+C 退出 · /help 看命令"),
	}, "\n")
	return tea.Sequence(tea.Println(banner), textarea.Blink)
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = max(msg.Width, 24)
		m.input.SetWidth(m.width - 4)
		return m, nil

	case tea.KeyPressMsg:
		return m.handleKey(msg)

	case spinner.TickMsg:
		if m.state != stateRunning {
			return m, nil // 停下来就别让时钟继续空转
		}
		var cmd, toolCmd tea.Cmd
		m.spin, cmd = m.spin.Update(msg)
		m.toolSpin, toolCmd = m.toolSpin.Update(msg)
		return m, tea.Batch(cmd, toolCmd)

	case eventMsg:
		if m.state != stateRunning {
			return m, nil
		}
		return m.handleEvent(domain.Event(msg))

	case doneMsg:
		return m.finish(msg.err)
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

func (m model) handleKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	switch msg.String() {
	case "ctrl+c":
		// 跑着的时候先中断任务，闲着的时候才退出
		if m.state == stateRunning {
			if m.cancel != nil {
				m.status = "正在中断…"
				m.cancel()
			}
			return m, nil
		}
		m.quitting = true
		return m, tea.Quit

	case "ctrl+d":
		if m.state == stateIdle && m.input.Value() == "" {
			m.quitting = true
			return m, tea.Quit
		}
	}

	if msg.Code == tea.KeyEnter {
		if m.state == stateRunning {
			return m, nil // 运行中不接受新输入
		}
		if !key.Matches(msg, m.input.KeyMap.InsertNewline) {
			return m.submit() // 光按 Enter 是发送；Shift+Enter 之类落到下面交给输入框换行
		}
	}

	var cmd tea.Cmd
	m.input, cmd = m.input.Update(msg)
	return m, cmd
}

// toolCallLines 渲染一次工具调用的内容，不含最前面的圆圈（圆圈要按状态上色，
// 得等结果出来才知道）。Bash 会把意图和完整命令都摊开，
// 其余工具没有专门的格式，压成一行摘要就够了。
func (m model) toolCallLines(call domain.ToolCall) []string {
	lines, ok := bashCallLines(call)
	if ok {
		return lines
	}
	return []string{toolStyle.Render(call.Name) +
		hintStyle.Render(" "+firstLines(call.Arguments, toolArgRunes))}
}

// withMark 把圆圈贴到标题行最前面。标题行可能已经被着色，所以只在前面加一段，
// 不动原来的内容；也不改传进来的切片。
func withMark(lines []string, mark string) []string {
	if mark == "" || len(lines) == 0 {
		return lines
	}
	out := append([]string{}, lines...)
	out[0] = mark + " " + out[0]
	return out
}

// spinMark 是调用中圆圈的位置：转圈的当前帧
func (m model) spinMark() string {
	return m.toolSpin.View()
}

// doneMark 是调用结束后的圆圈：成功绿色、失败红色
func doneMark(status domain.ToolStatus) string {
	if status == domain.ToolStatusFailed {
		return errStyle.Render(toolMarkDone)
	}
	return toolOKStyle.Render(toolMarkDone)
}

// callResultSuffix 是贴在调用标题行末尾的结果：状态和耗时都在同一行，
// 失败时把原因也摊在同一行，太长的原因压成一行再截断。
func callResultSuffix(ev domain.Event) string {
	text := "  · " + ev.Status.Label() + " · 耗时 " + ev.ElapsedLabel()
	if ev.Status != domain.ToolStatusFailed {
		return hintStyle.Render(text)
	}
	reason := firstLines(ev.Err, toolErrRunes)
	if strings.TrimSpace(reason) == "" {
		reason = "未提供失败原因"
	}
	return hintStyle.Render(text+" · 原因: ") + errStyle.Render(reason)
}

// toolResultLines 渲染一次调用结束后的样子：圆圈按成败上色，耗时和失败原因
// 都跟在标题行后面，调用内容沿用调用中已经渲染好的那几行。
func (m model) toolResultLines(ev domain.Event) []string {
	lines := m.pending
	if lines == nil {
		lines = m.toolCallLines(ev.Call)
	}
	out := withMark(lines, doneMark(ev.Status))
	out[0] += callResultSuffix(ev)
	return out
}

// bashCallLines 把 Bash 调用渲染成"意图 + 完整命令"：命令不截断，
// 多行命令（&&、管道拆行写的）按原样保留。参数不完整时返回 false，让调用方退回摘要。
func bashCallLines(call domain.ToolCall) ([]string, bool) {
	if call.Name != (tool.BashTool{}).Name() {
		return nil, false
	}
	var args tool.BashArgs
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
		return nil, false
	}
	command := strings.TrimRight(args.Command, "\r\n")
	if strings.TrimSpace(command) == "" {
		return nil, false
	}

	title := toolStyle.Render(call.Name)
	if intent := strings.TrimSpace(args.Intent); intent != "" {
		title += hintStyle.Render("  " + intent)
	}
	out := []string{title}
	if wd := strings.TrimSpace(args.Workdir); wd != "" {
		out = append(out, hintStyle.Render("  目录 "+wd))
	}
	for i, line := range strings.Split(command, "\n") {
		if i == 0 {
			out = append(out, hintStyle.Render("  $ ")+strings.TrimRight(line, "\r"))
			continue
		}
		out = append(out, strings.Repeat(" ", 4)+strings.TrimRight(line, "\r"))
	}
	return out, true
}

// submit 处理一次回车：本地命令就地执行，普通输入交给模型
func (m model) submit() (tea.Model, tea.Cmd) {
	input := strings.TrimSpace(m.input.Value())
	if input == "" {
		return m, nil
	}
	m.input.Reset()

	switch parseCommand(input) {
	case cmdExit:
		m.quitting = true
		return m, tea.Quit
	case cmdHelp:
		return m, tea.Println(helpText())
	case cmdNew:
		m.cur = domain.NewSession(m.workdir)
		return m, tea.Println(bannerStyle.Render("新会话 "+m.cur.Id) +
			hintStyle.Render("，消息 "+fmt.Sprint(len(m.cur.Messages))+" 条"))
	case cmdSession:
		return m, tea.Println(sessionText(m.cur, m.store))
	}

	return m.startRun(input)
}

// startRun 把提问丢给 core.Loop，用 goroutine + channel 把过程事件送回 UI
func (m model) startRun(prompt string) (tea.Model, tea.Cmd) {
	p, err := buildProvider(m.cfg)
	if err != nil {
		return m, tea.Println(errStyle.Render("创建 Provider 失败: " + err.Error()))
	}

	ctx, cancel := context.WithCancel(context.Background())
	r := &runner{ch: make(chan domain.Event, 256)}

	m.cancel = cancel
	m.runner = r
	m.state = stateRunning
	m.status = "连接模型…"
	m.pending = nil
	m.reasoning.Reset()
	m.answer.Reset()
	m.reasoningShown = false

	cur, store, workdir := m.cur, m.store, m.workdir

	go func() {
		defer close(r.ch)
		r.err = core.Loop(domain.Ctx{
			Prompt:   prompt,
			Provider: p,
			Tools:    tool.Tools,
			Context:  ctx,
			WorkDir:  workdir,
			Shell:    tool.ShellName(),
			Session:  cur,
			Emit:     func(ev domain.Event) { r.ch <- ev },
		})
		// 出错也要落盘，前面几轮的工具调用不能白跑
		if saveErr := store.Save(cur); saveErr != nil && r.err == nil {
			r.err = saveErr
		}
	}()

	return m, tea.Sequence(
		// 把用户原话原样回显到历史区，多行输入也保留换行
		tea.Println(promptStyle.Render(prompt)),
		m.spin.Tick,
		m.toolSpin.Tick,
		waitEvent(r),
	)
}

// handleEvent 把 core 的事件翻译成界面上的一行行输出
func (m model) handleEvent(ev domain.Event) (tea.Model, tea.Cmd) {
	var out []string

	switch ev.Kind {
	case domain.EventStep:
		if ev.Step > 1 {
			m.status = fmt.Sprintf("第 %d 轮", ev.Step)
		}

	case domain.EventReasoning:
		m.reasoning.WriteString(ev.Text)
		m.status = "思考中…"

	case domain.EventContent:
		if !m.reasoningShown {
			out = append(out, m.block("思考", thinkStyle, m.reasoning.String(), printReasoningLines)...)
			m.reasoningShown = true
		}
		m.answer.WriteString(ev.Text)
		m.status = "回答中…"

	case domain.EventAnswerEnd:
		if !m.reasoningShown {
			out = append(out, m.block("思考", thinkStyle, m.reasoning.String(), printReasoningLines)...)
			m.reasoningShown = true
		}
		answer := m.answer.String()
		if strings.TrimSpace(answer) == "" {
			answer = ev.Text // provider 没走流式时用整段兜底
		}
		out = append(out, m.block("", answerStyle(), answer, 0)...)
		m.answer.Reset()
		m.reasoning.Reset()
		m.reasoningShown = false

	case domain.EventToolCall:
		// 内容先挂到底部转圈，结果回来才带状态色打进历史区
		m.status = "执行 " + ev.Call.Name + "…"
		m.pending = m.toolCallLines(ev.Call)

	case domain.EventToolResult:
		out = append(out, m.toolResultLines(ev)...)
		m.pending = nil
		out = append(out, m.block("", toolResStyle, ev.Text, printToolResultLines)...)
		m.status = "等模型继续…"
	}

	cmds := []tea.Cmd{}
	if len(out) > 0 {
		cmds = append(cmds, tea.Println(strings.Join(out, "\n")))
	}
	cmds = append(cmds, waitEvent(m.runner))
	return m, tea.Sequence(cmds...)
}

// finish 收尾：把没来得及打的内容补上，回到输入状态
func (m model) finish(err error) (tea.Model, tea.Cmd) {
	var out []string
	if !m.reasoningShown {
		out = append(out, m.block("思考", thinkStyle, m.reasoning.String(), printReasoningLines)...)
	}
	out = append(out, m.block("", answerStyle(), m.answer.String(), 0)...)

	// 工具跑到一半被打断，调用那几行还没打进历史区，补上并标成未完成
	if len(m.pending) > 0 {
		lines := withMark(m.pending, doneMark(domain.ToolStatusFailed))
		lines[0] += hintStyle.Render("  · 未完成")
		out = append(out, lines...)
		m.pending = nil
	}

	m.state = stateIdle
	m.cancel = nil
	m.runner = nil
	m.status = ""
	m.reasoning.Reset()
	m.answer.Reset()
	m.reasoningShown = false

	switch {
	case err == nil:
	case errors.Is(err, context.Canceled):
		out = append(out, hintStyle.Render("已中断"))
	default:
		out = append(out, errStyle.Render("出错: "+err.Error()))
	}

	cmds := []tea.Cmd{}
	if len(out) > 0 {
		cmds = append(cmds, tea.Println(strings.Join(out, "\n")))
	}
	cmds = append(cmds, textarea.Blink)
	return m, tea.Sequence(cmds...)
}

func (m model) View() tea.View {
	if m.quitting {
		return tea.NewView("")
	}
	var b strings.Builder
	if m.state == stateRunning {
		b.WriteString(m.runningView())
		b.WriteString("\n")
	}
	b.WriteString(boxStyle.Width(m.width - 4).Render(m.input.View()))
	b.WriteString("\n")
	if m.state == stateRunning {
		b.WriteString(hintStyle.Render("Ctrl+C 中断"))
	} else {
		b.WriteString(hintStyle.Render("Enter 发送 · Shift+Enter 换行 · Ctrl+C 退出"))
	}
	return tea.NewView(b.String())
}

// runningView 是跑任务时贴在底部的那几行：状态 + 思考和回答的尾巴。
// 有工具在跑的时候，第一行换成这次调用本身，圆圈的位置转圈。
func (m model) runningView() string {
	var b strings.Builder
	if len(m.pending) > 0 {
		b.WriteString(strings.Join(withMark(m.pending, m.spinMark()), "\n"))
	} else {
		b.WriteString(statusStyle.Render(m.spin.View() + " " + m.status))
	}

	if tail := tailLines(m.reasoning.String(), previewReasoningLines); tail != "" {
		b.WriteString("\n" + thinkStyle.Render(m.wrap("  "+tail)))
	}
	if tail := tailLines(m.answer.String(), previewAnswerLines); tail != "" {
		b.WriteString("\n" + m.wrap("  "+tail))
	}
	return b.String()
}

// block 把一段可能很长的文本整理成"标题 + 缩进正文"，maxLines 为 0 表示不截断。
// 返回的是几行已经着好色的字符串，交给 tea.Println 打到上方历史区。
func (m model) block(title string, titleStyle lipgloss.Style, text string, maxLines int) []string {
	// 工具结果（exit_code 那行 + 命令输出）和思考内容基本都以换行结尾。
	// 结尾的换行不是内容，得先去掉，否则 Split 之后会多出一个空串，
	// 被当成一行空行打到历史区，看着就像莫名多了个空行。
	text = strings.TrimRight(text, "\r\n")
	if strings.TrimSpace(text) == "" {
		return nil
	}
	body, cut := headLines(text, maxLines)

	var out []string
	if title != "" {
		out = append(out, titleStyle.Render(title))
	}
	for _, line := range strings.Split(body, "\n") {
		// Windows 下命令输出是 \r\n，行尾的 \r 留着会把光标拉回行首
		out = append(out, "  "+m.wrap(strings.TrimRight(line, "\r")))
	}
	if cut {
		out = append(out, hintStyle.Render(fmt.Sprintf(
			"  …(只显示前 %d 行，完整内容在会话文件里)", maxLines)))
	}
	return out
}

// wrap 按终端宽度折行，宽度未知（还没收到 WindowSizeMsg）时原样返回
func (m model) wrap(s string) string {
	if m.width <= 0 || lipgloss.Width(s) <= m.width-4 {
		return s
	}
	return lipgloss.NewStyle().Width(m.width - 4).Render(s)
}

func answerStyle() lipgloss.Style { return lipgloss.NewStyle() }

// waitEvent 读一个事件，channel 关了就把这一轮的结果交出去
func waitEvent(r *runner) tea.Cmd {
	return func() tea.Msg {
		ev, ok := <-r.ch
		if !ok {
			return doneMsg{err: r.err}
		}
		return eventMsg(ev)
	}
}

// buildProvider 按配置里声明的类型造 provider
func buildProvider(cfg config.GlobalConfig) (domain.Provider, error) {
	var probe config.ProbeConfig
	if err := cfg.Provider.Decode(&probe); err != nil {
		return nil, fmt.Errorf("provider 解析为 ProbeConfig 报错: %w", err)
	}
	switch probe.Type {
	case "DeepSeek":
		var dc config.DeepSeekConfig
		if err := cfg.Provider.Decode(&dc); err != nil {
			return nil, fmt.Errorf("provider 解析为 DeepSeekConfig 报错: %w", err)
		}
		p, err := provider.NewDeepSeek(dc)
		if err != nil {
			return nil, err
		}
		return p, nil
	default:
		return nil, fmt.Errorf("未支持的 provider 类型: %s", probe.Type)
	}
}

// headLines 取前 maxLines 行，返回是否被截断；maxLines <= 0 表示不截断
func headLines(s string, maxLines int) (string, bool) {
	lines := strings.Split(s, "\n")
	if maxLines <= 0 || len(lines) <= maxLines {
		return s, false
	}
	return strings.Join(lines[:maxLines], "\n"), true
}

// tailLines 取末尾 maxLines 行，用来在底部显示流式输出的最新进度
func tailLines(s string, maxLines int) string {
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if maxLines > 0 && len(lines) > maxLines {
		lines = lines[len(lines)-maxLines:]
	}
	return strings.Join(lines, "\n")
}

// firstLines 把文本压成一行并截断，用于工具参数这种长文本；
// maxRunes <= 0 表示不截断
func firstLines(s string, maxRunes int) string {
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if maxRunes > 0 && len(runes) > maxRunes {
		return string(runes[:maxRunes]) + "…"
	}
	return s
}
