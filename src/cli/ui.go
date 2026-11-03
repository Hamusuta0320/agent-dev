package main

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"strconv"
	"strings"
	"time"

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
	"github.com/charmbracelet/x/ansi"
)

type uiState int

const (
	// stateIdle 是等用户输入的状态
	stateIdle uiState = iota
	// stateRunning 是模型或工具在跑的状态
	stateRunning
	// stateConfig 是 /config 打开的表单，此时键盘全归表单
	stateConfig
	// stateApproval 是有写操作在等用户确认，此时只有 y / a / n 三个键有效
	stateApproval
)

// 默认的配置文件路径，和 main 里 loadConfig 用的是同一个
const defaultCfgPath = "config.yaml"

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
	// maxTaskChips 是后台任务栏里最多列几个任务，再多就折成一个"还有 N 个"
	maxTaskChips = 3
	// maxResumeList 是 /resume 列表最多列几个会话，再多就折成一句"还有 N 个"
	maxResumeList = 10
	// activeRows 是输入框上方那块会变的内容固定占几行：跑任务时是状态、工具调用
	// 和预览的尾巴，闲着时是补全候选。行数必须恒定——Bubble Tea 在 inline 模式下
	// 遇到帧变矮，会把腾出来的行空在屏幕底部，输入框就离了屏幕底边；宁可在上面
	// 补空行。7 行正好放得下全部命令候选。
	activeRows = 7
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

	// 上下文占用里三类来源各用一种颜色，扫一眼就知道是哪一块在涨
	usageSystemStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("141"))
	usageToolStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("215"))
	usageMsgStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
)

// 从模型那边流回来的消息
type eventMsg domain.Event

// doneMsg 表示这一轮跑完了
type doneMsg struct{ err error }

// taskTickMsg 只用来让界面自己动一下：后台任务在跑的时候，耗时那栏每秒得刷新
type taskTickMsg struct{}

// runner 在后台跑一轮 core.Loop，把过程事件通过 channel 送给 UI。
// err 要在 close(ch) 之前写好，UI 读到 channel 关闭时才有确定的 err。
type runner struct {
	ch  chan domain.Event
	err error
}

// approvalState 是一项等用户点头的写操作：req 用来渲染确认框，
// reply 是 core 停在那儿等的那个 channel，回答完才能让这一轮继续。
type approvalState struct {
	req   domain.ApprovalRequest
	reply chan domain.ApprovalAnswer

	// choice 是选项列表里当前选中的那一项，进确认框时停在"允许一次"上，
	// 所以直接回车就等于同意
	choice int
	// typing 为 true 表示正在写"拒绝并说明"的那句话
	typing bool
	// feedback / cursor 是那句话的输入缓冲和光标位置
	feedback []rune
	cursor   int
}

// approvalOption 是确认框里的一个选项。keys 是能直接按的快捷键，
// 熟手不用挪手去按方向键。
type approvalOption struct {
	label string
	keys  string
	// answer 是这个选项对应的结论，feedback 由调用方补上
	answer domain.ApprovalAnswer
}

// approvalOptions 是确认框的选项列表，顺序就是上下键的顺序。
// 第一项是"允许一次"，所以默认选中、直接回车就是放行。
func approvalOptions(tool string) []approvalOption {
	return []approvalOption{
		{label: "允许一次", keys: "y / Enter", answer: domain.ApprovalAnswer{Allowed: true}},
		{label: "本回合都允许 " + tool, keys: "a", answer: domain.ApprovalAnswer{Allowed: true, Always: true}},
		{label: "拒绝", keys: "n / Esc", answer: domain.ApprovalAnswer{}},
	}
}

type model struct {
	cfg     config.GlobalConfig
	cfgPath string
	store   *session.Store
	workdir string
	cur     *domain.Session

	input  textarea.Model
	spin   spinner.Model
	state  uiState
	status string

	// suggestIdx 是补全候选里当前选中的那个，输入变了就失效（渲染时夹到范围内）
	suggestIdx int
	// form 是 /config 的模型配置表单
	form configForm

	// toolSpin 是工具调用标题行前面那个转圈的圆圈，和底部状态行的转圈各转各的
	toolSpin spinner.Model
	// pending 是正在执行、还没出结果的工具调用（渲染好的行，不含最前面的圆圈）。
	// 调用期间它顶在底部转圈，结果回来才带着状态色打进上方历史区——
	// 历史区打出去的行改不了，颜色得等结果才知道。
	// 同一轮里几个只读工具会并行跑，所以这儿是个列表，靠 id 把结果对回对应的调用
	pending []pendingCall

	cancel context.CancelFunc
	runner *runner

	// 流式输出先攒在这里，边攒边在底部预览，这一轮结束再整段打到上方。
	// 用指针而不是值：Bubble Tea 的 model 按值复制，值类型的 Builder 写过之后再被复制、写入会 panic
	// （illegal use of non-zero Builder copied by value）；指针复制后仍指向同一个 Builder，
	// 且只有 UI goroutine 会写它，不存在并发问题。
	reasoning      *strings.Builder
	answer         *strings.Builder
	reasoningShown bool
	// lastAnswer 是上一轮回答的原文：闲着时输入框上方那块槽位拿它的尾巴填上，
	// 免得空一大片。answer 每轮结束就 Reset 了，所以单独留一份
	lastAnswer string
	// attachments 是贴进来、等着随下一条提问一起发出去的图片。
	// 发送、切换会话、按 Esc 都会清空
	attachments []imageAttachment

	// usage 是最后一次请求的上下文占用（总数来自响应里的 usage），
	// 贴在输入框上方常驻显示，新一轮开始时清零
	usage domain.ContextUsage
	// contextLimit 是配置里的上下文窗口大小，用来算已用百分比
	contextLimit int

	// height 是终端高度，开场时靠它把输入框顶到屏幕底部
	height int
	// started 表示开场白已经打过了：得等知道窗口高度才能算要补多少空行
	started bool
	// tasksTicking 表示"后台任务耗时"的定时刷新已经转起来了，避免重复起定时器
	tasksTicking bool

	// mode 是当前工作模式，决定给模型哪些工具、写操作要不要问
	mode domain.Mode
	// approval 是正在等用户确认的那项写操作，为 nil 表示没有在等
	approval *approvalState

	// notice 是启动时想顺带说一句的事情（比如刚把老会话迁过来），空着就不显示
	notice string

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

	// 配置里的模式写错了不该拦住启动，退回默认模式，/mode 会立刻显示成什么
	mode, _ := domain.ParseMode(cfg.Mode)

	return model{
		cfg:          cfg,
		cfgPath:      defaultCfgPath,
		store:        store,
		workdir:      workdir,
		cur:          cur,
		input:        input,
		spin:         spin,
		toolSpin:     toolSpin,
		state:        stateIdle,
		form:         newConfigForm(deepSeekConfig(cfg)),
		contextLimit: deepSeekConfig(cfg).Limit(),
		mode:         mode,
		reasoning:    &strings.Builder{},
		answer:       &strings.Builder{},
		width:        80,
	}
}

func (m model) Init() tea.Cmd {
	// 开场白要等拿到窗口高度再打：顺手补一段空行，把输入框顶到屏幕底部，
	// 之后会话再长，输入框也一直贴着底边
	return textarea.Blink
}

// bannerLines 是开场那几行
func (m model) bannerLines() []string {
	lines := []string{
		bannerStyle.Render("agent-dev") + hintStyle.Render("  会话 "+m.cur.Id),
		hintStyle.Render("模式 ") + statusStyle.Render(m.mode.Label()) +
			hintStyle.Render(" · Enter 发送 · Shift+Enter 换行 · Ctrl+C 退出 · /help 看命令"),
	}
	if inVSCodeTerminal() {
		// VS Code 集成终端会把 Tab 拿去移动焦点，键根本到不了程序，先把后路写在脸上
		lines = append(lines, hintStyle.Render(
			"提示：VS Code 终端里 Tab 可能被接走，补全请用 Ctrl+Space 或 →"))
	}
	if m.notice != "" {
		lines = append(lines, hintStyle.Render(m.notice))
	}
	return lines
}

// startupText 是开场白加上一段空行。空行的数量按"开场白 + 界面 + 空行 = 终端高度"算，
// 这样界面正好落在屏幕最后几行，输入框就贴底了。
func (m model) startupText() string {
	lines := m.bannerLines()
	text := strings.Join(lines, "\n")
	// 界面本身占几行（输入框边框、上下文占用、提示行……），不足就往上补空行
	if pad := m.height - len(lines) - m.viewLines(); pad > 0 {
		text += strings.Repeat("\n", pad)
	}
	return text
}

// viewLines 数当前界面占几行，用来算开场要补多少空行
func (m model) viewLines() int {
	return strings.Count(m.View().Content, "\n") + 1
}

// inVSCodeTerminal 判断是不是跑在 VS Code 的集成终端里
func inVSCodeTerminal() bool {
	return os.Getenv("TERM_PROGRAM") == "vscode"
}

func (m model) Update(msg tea.Msg) (tea.Model, tea.Cmd) {
	next, cmd := m.update(msg)
	// 后台任务在跑的时候，耗时要自己走起来；停下来之后就别再空转
	mm, ok := next.(model)
	if !ok {
		return next, cmd
	}
	return mm.withTaskTicker(cmd)
}

// withTaskTicker 保证"有任务在跑就有定时器、没任务就没有"，避免重复起定时器
func (m model) withTaskTicker(cmd tea.Cmd) (tea.Model, tea.Cmd) {
	running := tool.RunningTaskCount() > 0
	if !running {
		m.tasksTicking = false
		return m, cmd
	}
	if !m.tasksTicking {
		m.tasksTicking = true
		return m, tea.Batch(cmd, taskTick())
	}
	return m, cmd
}

// taskTick 每秒叫醒一次界面，好让后台任务的耗时那栏跟着变
func taskTick() tea.Cmd {
	return tea.Tick(time.Second, func(time.Time) tea.Msg { return taskTickMsg{} })
}

func (m model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		m.width = max(msg.Width, 24)
		m.height = max(msg.Height, 10)
		m.input.SetWidth(m.width - 4)
		m.resizeForm()
		if !m.started {
			// 第一次知道窗口多大：这时候打开场白，才算得准要补多少空行
			m.started = true
			return m, tea.Sequence(m.print(m.startupText()), textarea.Blink)
		}
		return m, nil

	case taskTickMsg:
		// 这一拍用掉了，交给外层的 withTaskTicker 决定要不要再续一拍
		m.tasksTicking = false
		return m, nil

	case tea.KeyPressMsg:
		if m.state == stateConfig {
			return m.handleConfigKey(msg)
		}
		if m.state == stateApproval {
			return m.handleApprovalKey(msg)
		}
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

	case tea.PasteMsg:
		// 确认框里粘贴：当成"拒绝并说明"的内容，和打字是一回事
		if m.state == stateApproval && m.approval != nil {
			m.approval.typing = true
			m.approval.insert([]rune(msg.Content)...)
			return m, nil
		}
		// 拖图片进终端 / 粘贴图片路径：认出来的话挂到下一条提问上，别当文字塞进输入框
		if m.state == stateIdle {
			atts, problem := imagesFromPaste(msg.Content)
			if len(atts) > 0 || problem != "" {
				next, cmd := m.attachImages(atts)
				if problem == "" {
					return next, cmd
				}
				return next, tea.Sequence(cmd, m.print(errStyle.Render(problem)))
			}
		}

	case clipboardPasteMsg:
		if msg.err != nil {
			if errors.Is(msg.err, errNoClipboardImage) {
				return m, m.print(hintStyle.Render(
					"剪贴板里没有图片也没有文字：截图后按 Ctrl+V，或者把图片文件拖进来"))
			}
			return m, m.print(errStyle.Render("读剪贴板失败: " + msg.err.Error()))
		}
		// 文字就和终端自己粘贴一样，插到光标处；确认框里贴的是"拒绝并说明"
		if msg.text != "" {
			if m.state == stateApproval && m.approval != nil {
				m.approval.typing = true
				m.approval.insert([]rune(msg.text)...)
				return m, nil
			}
			if m.state == stateIdle {
				m.input.InsertString(msg.text)
			}
			return m, nil
		}
		if m.state == stateIdle {
			return m.attachImages([]imageAttachment{{label: "剪贴板", img: msg.img}})
		}
		return m, nil
	}

	// 表单里非按键的消息（光标闪烁）只交给正在编辑的那一格
	if m.state == stateConfig {
		if !m.form.editing {
			return m, nil
		}
		var cmd tea.Cmd
		m.form.inputs[m.form.cur], cmd = m.form.inputs[m.form.cur].Update(msg)
		return m, cmd
	}

	// 确认框期间输入框是藏起来的，别让粘贴、光标闪烁之类的消息悄悄改到它
	if m.state == stateApproval {
		return m, nil
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

	case "ctrl+v", "alt+v":
		// 多数终端把 Ctrl+V 当成"我自己贴一段文字"，这个键根本到不了这儿；终端肯放过来时
		// 就由我们去剪贴板取一次：有文字贴文字，没有文字才当成图。Alt+V 是备用入口
		// （Windows Terminal、VS Code 集成终端默认会接走 Ctrl+V）
		if m.state == stateIdle {
			return m, pasteFromClipboard()
		}

	case "esc":
		// 附了图想撤：闲着时 Esc 没别的用处，就用来清空
		if m.state == stateIdle && len(m.attachments) > 0 {
			m.attachments = nil
			return m, m.print(hintStyle.Render("已清空附图"))
		}
	}

	// 补全键全归我们管：有候选就补全，没候选也直接吞掉，不再往下漏给输入框。
	// （输入框本身没绑 Tab，漏下去什么都不会发生；真正的麻烦是 VS Code 集成终端
	// 会把 Tab 当成"移动焦点"直接接走，那种情况 Tab 根本到不了这里，
	// 所以补全还留了两条不经过 Tab 的路：Ctrl+Space，和光标停在行尾时的 →。
	// 光标已经在行尾时 → 本来就无处可去，拿来接受补全正好。）
	movingRight := msg.Code == tea.KeyRight && m.cursorAtEnd()
	if isCompleteKey(msg) || movingRight {
		if m.state == stateIdle {
			if specs := suggestCommands(m.input.Value()); len(specs) > 0 {
				return m.complete(specs)
			}
		}
		if !movingRight {
			return m, nil // 补全键吞掉；→ 没候选就照常交给输入框右移
		}
	}

	// 输入框上方有补全候选时，↑↓ 选、Enter 补全
	if m.state == stateIdle {
		if specs := suggestCommands(m.input.Value()); len(specs) > 0 {
			switch msg.String() {
			case "up":
				m.suggestIdx = (m.curSuggest(specs) - 1 + len(specs)) % len(specs)
				return m, nil
			case "down":
				m.suggestIdx = (m.curSuggest(specs) + 1) % len(specs)
				return m, nil
			case "enter":
				// 命令还没打完，回车先当成补全，别把半截 "/co" 发给模型
				return m.complete(specs)
			}
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

// handleApprovalKey 处理确认框上的按键：
//   - ↑↓ / j k 换选项，Enter 确认当前选项（默认是"允许一次"）
//   - y 允许一次、a 本回合都不再问、n / Esc 拒绝，熟手直接按单键
//   - 直接打字就进"拒绝并说明"：写完回车，这句话会转给模型
func (m model) handleApprovalKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	pending := m.approval
	if pending == nil {
		// 理论上不会发生：没有待确认的请求就回到正常状态，别把界面卡在确认框上
		if m.runner == nil {
			return m.finish(nil)
		}
		m.state = stateRunning
		return m, waitEvent(m.runner)
	}

	options := approvalOptions(pending.req.Tool)
	key := msg.String()

	// 和输入框里一样：终端肯把 Ctrl+V 放过来时，由我们去剪贴板取一次，
	// 贴进来的文字就当"拒绝并说明"
	if key == "ctrl+v" || key == "alt+v" {
		return m, pasteFromClipboard()
	}

	if pending.typing {
		switch {
		case key == "enter":
			// 写了话就当成"拒绝并说明"，光标下面是空的就还是普通拒绝
			ans := domain.ApprovalAnswer{Feedback: string(pending.feedback)}
			return m.finishApproval(pending, ans, withFeedback("已拒绝并说明", ans.Feedback))
		case key == "esc":
			pending.typing = false
			return m, nil
		case key == "ctrl+c":
			return m.finishApproval(pending, domain.ApprovalAnswer{}, "已拒绝")
		case key == "backspace":
			pending.backspace()
			return m, nil
		case key == "delete":
			pending.deleteForward()
			return m, nil
		case key == "left":
			pending.moveCursor(-1)
			return m, nil
		case key == "right":
			pending.moveCursor(1)
			return m, nil
		}
		// 其余可打印字符进缓冲区；ctrl+ 组合键之类不是内容，忽略掉
		if msg.Text != "" {
			pending.insert([]rune(msg.Text)...)
		}
		return m, nil
	}

	switch key {
	case "up", "k":
		pending.choice = (pending.choice - 1 + len(options)) % len(options)
		return m, nil
	case "down", "j":
		pending.choice = (pending.choice + 1) % len(options)
		return m, nil
	case "enter":
		opt := options[pending.choice]
		return m.finishApproval(pending, opt.answer, decisionText(pending.req.Tool, opt))
	case "y":
		return m.finishApproval(pending, options[0].answer, decisionText(pending.req.Tool, options[0]))
	case "a":
		return m.finishApproval(pending, options[1].answer, decisionText(pending.req.Tool, options[1]))
	case "n", "esc", "ctrl+c":
		return m.finishApproval(pending, options[2].answer, decisionText(pending.req.Tool, options[2]))
	}

	// 开始打字就是"拒绝，顺便说明要怎么改"
	if msg.Text != "" {
		pending.typing = true
		pending.insert([]rune(msg.Text)...)
	}
	return m, nil
}

// finishApproval 把回答送回 core，收起确认框，在历史区留一条记录
func (m model) finishApproval(pending *approvalState, ans domain.ApprovalAnswer, decision string) (tea.Model, tea.Cmd) {
	// channel 带一个缓冲，就算 core 已经走了（这一轮被中断）也不会卡住界面
	pending.reply <- ans

	m.approval = nil
	m.state = stateRunning
	switch {
	case ans.Allowed && ans.Always:
		m.status = "已允许，本回合不再问 " + pending.req.Tool + "…"
	case ans.Allowed:
		m.status = "已允许，执行 " + pending.req.Tool + "…"
	default:
		m.status = "已拒绝，等模型继续…"
	}

	// 确认框只出现在底部，打进历史区留个记录，事后能看出哪次改动是被同意的
	out := append(m.approvalLines(pending.req), hintStyle.Render("  · "+decision))
	return m, tea.Sequence(m.print(strings.Join(out, "\n")), waitEvent(m.runner))
}

// decisionText 是选项被选中后写进历史区的那句话
func decisionText(tool string, opt approvalOption) string {
	switch {
	case opt.answer.Allowed && opt.answer.Always:
		return "已允许，本回合不再问 " + tool
	case opt.answer.Allowed:
		return "已允许"
	default:
		return "已拒绝"
	}
}

// withFeedback 在结论后面附上用户写的那句话，太长就截一下
func withFeedback(base, feedback string) string {
	feedback = strings.TrimSpace(feedback)
	if feedback == "" {
		return base
	}
	return base + "：" + firstLines(feedback, toolErrRunes)
}

// insert 把字符插到光标处，缓冲区每次都重建，免得 model 按值复制后共享同一段底层数组。
// 换行和制表符换成空格、其它控制字符丢掉——这行只有一个显示行。
func (s *approvalState) insert(rs ...rune) {
	buf := make([]rune, 0, len(s.feedback)+len(rs))
	buf = append(buf, s.feedback[:s.cursor]...)
	added := 0
	for _, r := range rs {
		switch {
		case r == '\r':
			continue
		case r == '\n' || r == '\t':
			r = ' '
		case r < 32 || r == 127:
			continue
		}
		buf = append(buf, r)
		added++
	}
	buf = append(buf, s.feedback[s.cursor:]...)
	s.feedback = buf
	s.cursor += added
}

// backspace 删掉光标前一个字
func (s *approvalState) backspace() {
	if s.cursor == 0 {
		return
	}
	buf := make([]rune, 0, len(s.feedback))
	buf = append(buf, s.feedback[:s.cursor-1]...)
	buf = append(buf, s.feedback[s.cursor:]...)
	s.feedback = buf
	s.cursor--
}

// deleteForward 删掉光标处那一个字
func (s *approvalState) deleteForward() {
	if s.cursor >= len(s.feedback) {
		return
	}
	buf := make([]rune, 0, len(s.feedback))
	buf = append(buf, s.feedback[:s.cursor]...)
	buf = append(buf, s.feedback[s.cursor+1:]...)
	s.feedback = buf
}

// moveCursor 左右移光标，夹在缓冲区范围内
func (s *approvalState) moveCursor(delta int) {
	pos := s.cursor + delta
	if pos < 0 {
		pos = 0
	}
	if pos > len(s.feedback) {
		pos = len(s.feedback)
	}
	s.cursor = pos
}

// approvalLines 渲染一次待确认的写操作：标题 + 目标 + 补充说明 + 改动预览。
// 命令类（Bash）按多行摊开、每行前面带 $，和工具调用那几行保持同一种样子。
func (m model) approvalLines(req domain.ApprovalRequest) []string {
	out := []string{bannerStyle.Render("需要确认") + hintStyle.Render("  "+req.Tool)}
	switch req.Kind {
	case domain.ApprovalRunCommand:
		for i, line := range strings.Split(req.Target, "\n") {
			line = strings.TrimRight(line, "\r")
			if i == 0 {
				out = append(out, hintStyle.Render("  $ ")+line)
				continue
			}
			out = append(out, "    "+line)
		}
	default:
		out = append(out, "  "+statusStyle.Render(req.Target))
	}
	if detail := strings.TrimSpace(req.Detail); detail != "" {
		for _, line := range strings.Split(detail, "\n") {
			out = append(out, hintStyle.Render("  "+strings.TrimRight(line, "\r")))
		}
	}
	out = append(out, m.diffLines(req.Diff)...)
	return out
}

// diffLines 把改动预览画成 - 旧行 / + 新行，删的标红、加的标绿，扫一眼就知道动了什么
func (m model) diffLines(diff *domain.DiffPreview) []string {
	if diff == nil || diff.Empty() {
		return nil
	}
	const maxRunes = 200
	out := make([]string, 0, len(diff.Removed)+len(diff.Added)+1)
	for _, line := range diff.Removed {
		out = append(out, errStyle.Render("  - "+firstLines(line, maxRunes)))
	}
	for _, line := range diff.Added {
		out = append(out, toolOKStyle.Render("  + "+firstLines(line, maxRunes)))
	}
	if diff.Truncated {
		out = append(out, hintStyle.Render("  …(预览只显示开头几行)"))
	}
	return out
}

// approvalView 是等确认时顶替输入框的那个框：上面是这次要做什么，下面是选项。
// 正在写"拒绝并说明"时，选项换成那行输入框。
func (m model) approvalView() string {
	if m.approval == nil {
		return ""
	}
	lines := m.approvalLines(m.approval.req)
	lines = append(lines, "")
	if m.approval.typing {
		lines = append(lines,
			hintStyle.Render("  拒绝并说明：")+m.feedbackLine(),
			hintStyle.Render("  Enter 连同这句话拒绝 · Esc 回到选项"),
		)
		return boxStyle.Width(m.width - 4).Render(strings.Join(lines, "\n"))
	}

	for i, opt := range approvalOptions(m.approval.req.Tool) {
		text := fmt.Sprintf("%d. %s", i+1, opt.label)
		if i == m.approval.choice {
			lines = append(lines, promptStyle.Render("  ▸ "+text)+hintStyle.Render("  "+opt.keys))
			continue
		}
		lines = append(lines, hintStyle.Render("    "+text)+hintStyle.Render("  "+opt.keys))
	}
	lines = append(lines, "", hintStyle.Render(
		"  ↑↓ 选择 · Enter 确认 · 直接打字可以拒绝并说明要怎么改"))
	return boxStyle.Width(m.width - 4).Render(strings.Join(lines, "\n"))
}

// feedbackLine 是"拒绝并说明"那行输入框，光标位置用一条竖线标出来
func (m model) feedbackLine() string {
	buf := m.approval.feedback
	pos := m.approval.cursor
	if pos < 0 || pos > len(buf) {
		pos = len(buf)
	}
	return string(buf[:pos]) + statusStyle.Render("▏") + string(buf[pos:])
}

// pendingCall 是一次正在执行、还没出结果的工具调用。
// id 用来把结果对回对应的调用（同一轮里可能有几个只读工具同时在跑），
// lines 是已经渲染好的调用内容，不含最前面的圆圈。
type pendingCall struct {
	id    string
	lines []string
}

// toolCallLines 渲染一次工具调用的内容，不含最前面的圆圈（圆圈要按状态上色，
// 得等结果出来才知道）。标题行统一是"工具名 + 意图"，下面按工具补一行关键细节，
// 这样不用看 JSON 也知道这次在改哪个文件、搜什么词。
func (m model) toolCallLines(call domain.ToolCall) []string {
	title := toolStyle.Render(call.Name)
	if intent := tool.CallIntent(call.Arguments); intent != "" {
		title += hintStyle.Render("  " + intent)
	}
	if badge := callBadge(call); badge != "" {
		title += hintStyle.Render("  · " + badge)
	}
	return append([]string{title}, callDetailLines(call)...)
}

// callBadge 是标题行末尾的小标记，目前只有"后台运行的 Bash"需要
func callBadge(call domain.ToolCall) string {
	if call.Name != (tool.BashTool{}).Name() {
		return ""
	}
	var args tool.BashArgs
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
		return ""
	}
	if args.Background {
		return "后台"
	}
	return ""
}

// callDetailLines 按工具给出"这次到底做什么"的细节行（已经缩进好、上好色）。
// 参数解析不出来就不给细节，至少标题行还在，不至于把界面搞崩。
func callDetailLines(call domain.ToolCall) []string {
	switch call.Name {
	case "Bash":
		return bashDetailLines(call)
	case "Read":
		var args tool.ReadArgs
		if json.Unmarshal([]byte(call.Arguments), &args) != nil || strings.TrimSpace(args.Path) == "" {
			return nil
		}
		return []string{hintStyle.Render("  " + readRangeLabel(args))}
	case "Write":
		var args tool.WriteArgs
		if json.Unmarshal([]byte(call.Arguments), &args) != nil || strings.TrimSpace(args.Path) == "" {
			return nil
		}
		detail := args.Path
		if args.Content != nil {
			detail += fmt.Sprintf(" · 写入 %d 字节", len(*args.Content))
		}
		return []string{hintStyle.Render("  " + detail)}
	case "Edit":
		var args tool.EditArgs
		if json.Unmarshal([]byte(call.Arguments), &args) != nil || strings.TrimSpace(args.Path) == "" {
			return nil
		}
		detail := args.Path
		if args.ReplaceAll {
			detail += " · 替换全部匹配"
		}
		return []string{hintStyle.Render("  " + detail)}
	case "Grep":
		var args tool.GrepArgs
		if json.Unmarshal([]byte(call.Arguments), &args) != nil {
			return nil
		}
		return []string{hintStyle.Render("  " + grepLabel(args))}
	case "Glob":
		var args tool.GlobArgs
		if json.Unmarshal([]byte(call.Arguments), &args) != nil {
			return nil
		}
		return []string{hintStyle.Render("  " + globLabel(args))}
	case "Task":
		var args tool.TaskArgs
		if json.Unmarshal([]byte(call.Arguments), &args) != nil {
			return nil
		}
		return []string{hintStyle.Render("  " + taskLabel(args))}
	}
	// 认不出的工具（将来加的）退回参数摘要，总比什么都不显示强
	return []string{hintStyle.Render(" " + firstLines(call.Arguments, toolArgRunes))}
}

// readRangeLabel 是 Read 的细节：读哪个文件、读哪几行
func readRangeLabel(args tool.ReadArgs) string {
	label := args.Path
	offset := args.Offset
	if offset <= 0 {
		offset = 1
	}
	if offset > 1 || args.Lines > 0 {
		label += fmt.Sprintf(" · 第 %d 行起", offset)
		if args.Lines > 0 {
			label += fmt.Sprintf("，最多 %d 行", args.Lines)
		}
	}
	if args.LineNumbers != nil && !*args.LineNumbers {
		label += " · 不带行号"
	}
	return label
}

// grepLabel 是 Grep 的细节：搜什么词、限定哪些文件、要不要忽略大小写
func grepLabel(args tool.GrepArgs) string {
	pattern := strings.TrimSpace(args.Pattern)
	if pattern == "" {
		pattern = "（空）"
	}
	label := "搜索 " + truncateGrepLabel(pattern)
	if glob := strings.TrimSpace(args.Glob); glob != "" {
		label += " · " + glob
	}
	if args.Path != "" && args.Path != "." {
		label += " · 在 " + args.Path
	}
	if args.CaseInsensitive {
		label += " · 忽略大小写"
	}
	return label
}

// globLabel 是 Glob 的细节：找什么通配符、从哪个目录起
func globLabel(args tool.GlobArgs) string {
	pattern := strings.TrimSpace(args.Pattern)
	if pattern == "" {
		pattern = "*"
	}
	label := "列出 " + truncateGrepLabel(pattern)
	if path := strings.TrimSpace(args.Path); path != "" && path != "." {
		label += " · 在 " + path
	}
	return label
}

// taskLabel 是 Task 的细节：对哪个任务做什么
func taskLabel(args tool.TaskArgs) string {
	label := strings.TrimSpace(args.Action)
	if label == "" {
		label = "list"
	}
	if id := strings.TrimSpace(args.TaskId); id != "" {
		label += " " + id
	}
	return label
}

// truncateGrepLabel 把可能很长的一行（正则、通配符）压短，免得把界面撑爆
func truncateGrepLabel(s string) string {
	return firstLines(s, toolArgRunes)
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
// 都跟在标题行后面，调用内容用调用中已经渲染好的那几行（lines 为 nil 时现场重渲染）。
func (m model) toolResultLines(ev domain.Event, lines []string) []string {
	if lines == nil {
		lines = m.toolCallLines(ev.Call)
	}
	out := withMark(lines, doneMark(ev.Status))
	out[0] += callResultSuffix(ev)
	return out
}

// takePending 把这次调用的那几行从"调用中"列表里摘出来，还没出结果的调用保持不动
func (m *model) takePending(call domain.ToolCall) []string {
	for i, p := range m.pending {
		if p.id == call.Id {
			m.pending = append(m.pending[:i], m.pending[i+1:]...)
			return p.lines
		}
	}
	return nil
}

// pendingLines 把所有还在跑的调用拼成一串行，底部一起转圈
func (m model) pendingLines() []string {
	var out []string
	for _, p := range m.pending {
		out = append(out, p.lines...)
	}
	return out
}

// bashDetailLines 把 Bash 调用渲染成"目录 + 完整命令"：命令不截断，
// 多行命令（&&、管道拆行写的）按原样保留。参数不完整时返回 nil，只留标题行。
func bashDetailLines(call domain.ToolCall) []string {
	if call.Name != (tool.BashTool{}).Name() {
		return nil
	}
	var args tool.BashArgs
	if err := json.Unmarshal([]byte(call.Arguments), &args); err != nil {
		return nil
	}
	command := strings.TrimRight(args.Command, "\r\n")
	if strings.TrimSpace(command) == "" {
		return nil
	}

	var out []string
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
	return out
}

// curSuggest 把选中项夹进候选范围：输入变过之后旧下标可能已经越界
func (m model) curSuggest(specs []commandSpec) int {
	if m.suggestIdx < 0 || m.suggestIdx >= len(specs) {
		return 0
	}
	return m.suggestIdx
}

// complete 把选中的命令填进输入框，末尾留一个空格，方便直接接参数
func (m model) complete(specs []commandSpec) (tea.Model, tea.Cmd) {
	m.input.SetValue(specs[m.curSuggest(specs)].name + " ")
	m.suggestIdx = 0
	return m, nil
}

// isCompleteKey 判断这个键算不算"接受补全"。
// Tab 和 Ctrl+I 是同一个字节，有的终端配置下 Tab 会被解码成 ctrl+i 而不是 tab，
// 两种都得认；Ctrl+Space（以及同一字节的 Ctrl+@）是 Tab 被终端接走时的备选键。
func isCompleteKey(msg tea.KeyPressMsg) bool {
	if msg.Code == tea.KeyTab {
		return true
	}
	switch msg.String() {
	case "ctrl+i", "ctrl+space", "ctrl+@":
		return true
	}
	return false
}

// cursorAtEnd 判断光标是不是停在输入的末尾。只有这时 → 才算"接受补全"，
// 光标停在中间时 → 还是照常右移一格。
func (m model) cursorAtEnd() bool {
	lines := strings.Split(m.input.Value(), "\n")
	last := lines[len(lines)-1]
	return m.input.Line() == len(lines)-1 && m.input.Column() == len([]rune(last))
}

// completionLines 是输入框上方的补全候选，选中的一行高亮
func (m model) completionLines() []string {
	specs := suggestCommands(m.input.Value())
	if len(specs) == 0 {
		return nil
	}
	cur := m.curSuggest(specs)
	out := make([]string, 0, len(specs))
	for i, spec := range specs {
		name := fmt.Sprintf("%-9s", spec.name)
		if i == cur {
			out = append(out, promptStyle.Render("▸ "+name)+hintStyle.Render(spec.desc))
			continue
		}
		out = append(out, hintStyle.Render("  "+name+spec.desc))
	}
	return out
}

// attachImages 把几张图片挂到下一条提问上，在历史区说一声加了什么。
// 到上限就先提示一句，别默默攒着——攒下的每一张都要跟着会话存、每一轮发出去
func (m model) attachImages(atts []imageAttachment) (tea.Model, tea.Cmd) {
	if len(atts) == 0 {
		return m, nil
	}
	var lines []string
	for _, a := range atts {
		if len(m.attachments) >= maxAttachments {
			lines = append(lines, errStyle.Render(fmt.Sprintf(
				"一条提问最多 %d 张图，这几张先不收了（Esc 清空后再贴）", maxAttachments)))
			break
		}
		m.attachments = append(m.attachments, a)
		lines = append(lines, hintStyle.Render(fmt.Sprintf(
			"+ 附图 %s（%s %s）", a.label, sizeText(int64(a.img.Size())), a.img.Kind())))
	}
	return m, m.print(strings.Join(lines, "\n"))
}

// takeAttachments 把攒着的图片取走交给这一轮；取走之后输入框上方那行就空了
func (m *model) takeAttachments() []domain.Image {
	if len(m.attachments) == 0 {
		return nil
	}
	imgs := make([]domain.Image, 0, len(m.attachments))
	for _, a := range m.attachments {
		imgs = append(imgs, a.img)
	}
	m.attachments = nil
	return imgs
}

// attachmentsLine 是输入框上方那行"这条提问带了哪些图"：贴进来就看得见，
// 发送、Esc 或者切会话之后消失
func (m model) attachmentsLine() string {
	if len(m.attachments) == 0 {
		return ""
	}
	items := []string{hintStyle.Render("附图 ") + statusStyle.Render(fmt.Sprintf("%d 张", len(m.attachments)))}
	for _, a := range m.attachments {
		items = append(items, hintStyle.Render(fmt.Sprintf("%s %s", sizeText(int64(a.img.Size())), a.img.Kind())))
	}
	items = append(items, hintStyle.Render("· Esc 清空 · Enter 发送"))
	return strings.Join(items, hintStyle.Render(" · "))
}

// imagePrompt 是只贴了图片、一个字没写时替用户说的话：请求里总得有一段文字，
// 光秃秃一个图片数组有的接口会不高兴
func imagePrompt(n int) string {
	if n <= 1 {
		return "看看这张图"
	}
	return fmt.Sprintf("看看这 %d 张图", n)
}

// submit 处理一次回车：本地命令就地执行，普通输入交给模型
func (m model) submit() (tea.Model, tea.Cmd) {
	input := strings.TrimSpace(m.input.Value())
	// 只附了图片、没写字也是一次正经提问，不用拦
	if input == "" && len(m.attachments) == 0 {
		return m, nil
	}
	m.input.Reset()
	m.suggestIdx = 0

	// 一个字没写就替用户说一句"看看这几张图"，纯图片的提问也能发出去
	if input == "" {
		return m.startRun(imagePrompt(len(m.attachments)), m.takeAttachments())
	}

	switch parseCommand(input) {
	case cmdExit:
		m.quitting = true
		return m, tea.Quit
	case cmdHelp:
		return m, m.print(helpText())
	case cmdNew:
		m.cur = domain.NewSession(m.workdir)
		// 新会话，槽位里不该还挂着上个会话的回答，攒着的图也不再属于谁
		m.lastAnswer = ""
		m.attachments = nil
		return m, m.print(bannerStyle.Render("新会话 "+m.cur.Id) +
			hintStyle.Render("，消息 "+fmt.Sprint(len(m.cur.Messages))+" 条"))
	case cmdResume:
		return m.resume(input)
	case cmdSession:
		return m, m.print(sessionText(m.cur, m.workdir, m.store))
	case cmdConfig:
		return m.openConfig()
	case cmdMode:
		return m.switchMode(input)
	}

	return m.startRun(input, m.takeAttachments())
}

// switchMode 处理 /mode：不带参数就报当前模式和可选项，带了就切过去并写回配置，
// 下次启动还是这个模式。
func (m model) switchMode(input string) (tea.Model, tea.Cmd) {
	arg := ""
	if fields := strings.Fields(input); len(fields) > 1 {
		arg = strings.Join(fields[1:], " ")
	}
	if arg == "" {
		return m, m.print(modeText(m.mode))
	}

	mode, err := domain.ParseMode(arg)
	if err != nil {
		return m, m.print(errStyle.Render("切换模式失败: " + err.Error()))
	}
	m.mode = mode

	out := bannerStyle.Render("工作模式 ") + statusStyle.Render(mode.Label()) +
		hintStyle.Render("  "+modeHint(mode))
	// 写回配置失败不影响本次运行，说明一下就行
	if err := config.SaveMode(m.cfgPath, mode.String()); err != nil {
		out += "\n" + hintStyle.Render("（没能写回配置: "+err.Error()+"，本次运行仍然生效）")
	}
	return m, m.print(out)
}

// resume 处理 /resume：不带参数列出现有会话，带参数就切过去。
// 参数可以是列表里的序号（1 是最新的），也可以是完整的会话 id。
// 恢复只换 m.cur，下一句提问就接着这段历史往下聊，会话文件也接着往同一个 id 里写。
func (m model) resume(input string) (tea.Model, tea.Cmd) {
	arg := ""
	if fields := strings.Fields(input); len(fields) > 1 {
		arg = strings.Join(fields[1:], " ")
	}
	if m.store == nil {
		return m, m.print(errStyle.Render("恢复会话失败: 没有会话存储目录"))
	}
	infos, err := m.store.ListInfo()
	if err != nil {
		return m, m.print(errStyle.Render("恢复会话失败: " + err.Error()))
	}
	if arg == "" {
		return m, m.print(resumeListText(infos, m.cur))
	}

	id, err := pickSession(infos, arg)
	if err != nil {
		return m, m.print(errStyle.Render("恢复会话失败: " + err.Error()))
	}
	next, err := m.store.Load(id)
	if err != nil {
		return m, m.print(errStyle.Render("恢复会话失败: " + err.Error()))
	}
	if next.Id == "" {
		next.Id = id
	}
	m.cur = next
	// 换会话了，旧回答的尾巴不该还挂在槽位里，攒着的图也不再属于谁
	m.lastAnswer = ""
	m.attachments = nil
	out := bannerStyle.Render("已恢复会话 "+next.Id) +
		hintStyle.Render(fmt.Sprintf("，共 %d 条消息，目录 %s", len(next.Messages), m.store.Dir))
	// 别的目录里开的会话也照样能接着聊，只是工具仍按当前目录跑，提一句免得误会
	if next.WorkDir != "" && next.WorkDir != m.workdir {
		out += "\n" + hintStyle.Render("  这个会话是在 "+next.WorkDir+" 开的；继续聊时工具还是在当前目录 "+
			m.workdir+" 里跑")
	}
	return m, m.print(out)
}

// pickSession 把 /resume 的参数认成一个会话 id：纯数字按列表序号算，其余按 id 精确匹配
func pickSession(infos []session.Info, arg string) (string, error) {
	if n, err := strconv.Atoi(arg); err == nil {
		if n < 1 || n > len(infos) {
			return "", fmt.Errorf("序号 %d 超出范围（1-%d），先 /resume 看列表", n, len(infos))
		}
		return infos[n-1].Id, nil
	}
	for _, info := range infos {
		if info.Id == arg {
			return info.Id, nil
		}
	}
	return "", fmt.Errorf("没找到会话 %q，先 /resume 看列表", arg)
}

// resumeListText 是 /resume 不带参数时的会话列表：最近的在最前，当前会话标出来
func resumeListText(infos []session.Info, cur *domain.Session) string {
	title := bannerStyle.Render("会话列表") +
		hintStyle.Render(fmt.Sprintf("  共 %d 个，最近的在最前", len(infos)))
	if len(infos) == 0 {
		return title + "\n" + hintStyle.Render("  还没有存过的会话：跑完一轮就会自动存一份")
	}

	shown := infos
	if len(shown) > maxResumeList {
		shown = shown[:maxResumeList]
	}
	lines := []string{title}
	for i, info := range shown {
		mark := "  "
		style := hintStyle
		if cur != nil && info.Id == cur.Id {
			mark, style = "▸ ", promptStyle
		}
		when := "—"
		if !info.ModTime.IsZero() {
			when = info.ModTime.Format("01-02 15:04")
		}
		lines = append(lines, style.Render(fmt.Sprintf("%s%d. %-24s %s  %s",
			mark, i+1, info.Id, when, sizeText(info.Size))))
	}
	if len(infos) > len(shown) {
		lines = append(lines, hintStyle.Render(fmt.Sprintf("  …还有 %d 个更早的", len(infos)-len(shown))))
	}
	lines = append(lines, "", hintStyle.Render("  恢复：/resume 序号 或 /resume 会话 id"))
	return strings.Join(lines, "\n")
}

// sizeText 把字节数说成人话，用来粗略估计一个会话有多长
func sizeText(n int64) string {
	switch {
	case n >= 1024*1024:
		return fmt.Sprintf("%.1fM", float64(n)/(1024*1024))
	case n >= 1024:
		return fmt.Sprintf("%.0fK", float64(n)/1024)
	default:
		return fmt.Sprintf("%dB", n)
	}
}

// startRun 把提问丢给 core.Loop，用 goroutine + channel 把过程事件送回 UI。
// images 是随这条提问一起发出去的图片，会和文字一起记进会话
func (m model) startRun(prompt string, images []domain.Image) (tea.Model, tea.Cmd) {
	p, err := buildProvider(m.cfg)
	if err != nil {
		return m, m.print(errStyle.Render("创建 Provider 失败: " + err.Error()))
	}

	ctx, cancel := context.WithCancel(context.Background())
	r := &runner{ch: make(chan domain.Event, 256)}

	m.cancel = cancel
	m.runner = r
	m.state = stateRunning
	m.status = "连接模型…"
	m.pending = nil
	m.approval = nil
	m.usage = domain.ContextUsage{}
	m.reasoning.Reset()
	m.answer.Reset()
	m.reasoningShown = false

	cur, store, workdir := m.cur, m.store, m.workdir
	limit := m.contextLimit
	mode := m.mode
	maxSteps := m.cfg.Steps()

	go func() {
		defer close(r.ch)
		r.err = core.Loop(domain.Ctx{
			Prompt:       prompt,
			Images:       images,
			Provider:     p,
			Tools:        tool.ForMode(mode),
			Context:      ctx,
			WorkDir:      workdir,
			Shell:        tool.ShellName(),
			Session:      cur,
			ContextLimit: limit,
			Mode:         mode,
			MaxSteps:     maxSteps,
			// 被中断之后界面可能已经不再读这个 channel 了，
			// 所以发事件也要认 ctx，不然这一轮会卡在发送上收不了尾
			Emit: func(ev domain.Event) {
				select {
				case r.ch <- ev:
				case <-ctx.Done():
				}
			},
		})
		// 出错也要落盘，前面几轮的工具调用不能白跑
		if saveErr := store.Save(cur); saveErr != nil && r.err == nil {
			r.err = saveErr
		}
	}()

	// 把用户原话原样回显到历史区，多行输入也保留换行。
	// 先折行再着色：lipgloss 是逐行套样式的，反过来（先着色再折行）的话
	// 折出来的第二行起就丢了颜色
	echo := []string{promptStyle.Render(m.fitWidth(prompt))}
	if len(images) > 0 {
		// 图片本身在上面看不到，得说清楚这条提问带了几张，事后翻历史才知道当时给了什么
		echo = append(echo, hintStyle.Render(fmt.Sprintf("  （附 %d 张图片）", len(images))))
	}
	return m, tea.Sequence(
		m.print(strings.Join(echo, "\n")),
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

	case domain.EventUsage:
		m.usage = ev.Usage
		// 事件里没带上限时（比如 core 没拿到配置）用界面自己知道的那个补上
		if m.usage.ContextLimit <= 0 {
			m.usage.ContextLimit = m.contextLimit
		}

	case domain.EventApproval:
		// core 正停在这儿等回答，所以不能再 waitEvent，否则会一直读不到东西
		m.status = "等待确认…"
		m.state = stateApproval
		m.approval = nil
		if ev.Approval != nil {
			m.approval = &approvalState{req: *ev.Approval, reply: ev.Reply}
		}
		return m, nil

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

	// 连模型失败、正在重连：把原因和重试次数打进历史区，
	// 否则界面只会停在"连接模型…"，分不清是慢还是断了
	case domain.EventRetry:
		m.status = "连接失败，正在重试…"
		out = append(out, errStyle.Render("⚠ "+ev.Text))

	case domain.EventAnswerEnd:
		if !m.reasoningShown {
			out = append(out, m.block("思考", thinkStyle, m.reasoning.String(), printReasoningLines)...)
			m.reasoningShown = true
		}
		answer := m.answer.String()
		if strings.TrimSpace(answer) == "" {
			answer = ev.Text // provider 没走流式时用整段兜底
		}
		if strings.TrimSpace(answer) != "" {
			m.lastAnswer = answer
		}
		out = append(out, m.block("", answerStyle(), answer, 0)...)
		m.answer.Reset()
		m.reasoning.Reset()
		m.reasoningShown = false

	case domain.EventToolCall:
		// 内容先挂到底部转圈，结果回来才带状态色打进历史区
		m.status = "执行 " + ev.Call.Name + "…"
		m.pending = append(m.pending, pendingCall{id: ev.Call.Id, lines: m.toolCallLines(ev.Call)})

	case domain.EventToolResult:
		out = append(out, m.toolResultLines(ev, m.takePending(ev.Call))...)
		out = append(out, m.block("", toolResStyle, ev.Text, printToolResultLines)...)
		if len(m.pending) > 0 {
			m.status = fmt.Sprintf("还有 %d 个工具在跑…", len(m.pending))
		} else {
			m.status = "等模型继续…"
		}
	}

	cmds := []tea.Cmd{}
	if len(out) > 0 {
		cmds = append(cmds, m.print(strings.Join(out, "\n")))
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
	if text := m.answer.String(); strings.TrimSpace(text) != "" {
		// 打断得早的话 EventAnswerEnd 还没来过，这里补记一份给空闲槽位用
		m.lastAnswer = text
	}

	// 工具跑到一半被打断，调用那几行还没打进历史区，补上并标成未完成
	if len(m.pending) > 0 {
		lines := withMark(m.pendingLines(), doneMark(domain.ToolStatusFailed))
		lines[0] += hintStyle.Render("  · 未完成")
		out = append(out, lines...)
		m.pending = nil
	}

	m.state = stateIdle
	m.cancel = nil
	m.runner = nil
	m.approval = nil
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
		cmds = append(cmds, m.print(strings.Join(out, "\n")))
	}
	cmds = append(cmds, textarea.Blink)
	return m, tea.Sequence(cmds...)
}

// usageLine 是输入框上方常驻的上下文占用：总数来自上一轮响应的 usage，
// 系统提示词/工具定义/对话消息三项是按本地估算的比例拆出来的，
// 所以显示成百分比，不假装是精确到个位的分项用量。
// 配了上下文窗口时还会带上"已用 x%"，让人一眼看出还剩多少余量。
func (m model) usageLine() string {
	u := m.usage
	if u.PromptTokens <= 0 {
		return ""
	}
	parts := []struct {
		name  string
		token int
		style lipgloss.Style
	}{
		{"系统提示词", u.SystemTokens, usageSystemStyle},
		{"工具定义", u.ToolTokens, usageToolStyle},
		{"对话消息", u.MessageTokens, usageMsgStyle},
	}
	items := make([]string, 0, len(parts)+2)
	total := hintStyle.Render("上下文 ") + statusStyle.Render(domain.FormatTokens(u.PromptTokens))
	if limit := u.ContextLimit; limit > 0 {
		total += hintStyle.Render(" / " + domain.FormatTokens(limit))
	}
	total += hintStyle.Render(" tokens")
	if u.ContextLimit > 0 {
		// 用超过八成就变色，免得一路跑到爆窗口才发现
		used, style := u.LimitPercent(), statusStyle
		if used >= 80 {
			style = errStyle
		}
		total += hintStyle.Render(" · 已用 ") + style.Render(fmt.Sprintf("%d%%", used))
	}
	items = append(items, total)
	for _, p := range parts {
		items = append(items, p.style.Render(fmt.Sprintf("%s %s %d%%",
			p.name, domain.FormatTokens(p.token), u.Percent(p.token))))
	}
	if u.CompletionTokens > 0 {
		items = append(items, hintStyle.Render("本轮回复 "+domain.FormatTokens(u.CompletionTokens)))
	}
	return strings.Join(items, hintStyle.Render(" · "))
}

// tasksLine 是输入框上方常驻的后台任务栏：和上下文占用并排，
// 有任务就显示，没有就整行不出现。运行中的任务带上已跑时长，一眼看出卡没卡住。
func (m model) tasksLine() string {
	return tasksLine(tool.TaskSnapshots())
}

// tasksLine 把任务快照渲染成一行，纯函数，方便直接拿造的数据测
func tasksLine(snapshots []tool.TaskSnapshot) string {
	if len(snapshots) == 0 {
		return ""
	}
	running := 0
	for _, s := range snapshots {
		if s.Running {
			running++
		}
	}

	head := hintStyle.Render("后台任务 ")
	if running > 0 {
		head += statusStyle.Render(fmt.Sprintf("%d 个运行中", running))
		if done := len(snapshots) - running; done > 0 {
			head += hintStyle.Render(fmt.Sprintf(" · %d 个已结束", done))
		}
	} else {
		head += hintStyle.Render(fmt.Sprintf("%d 个已结束", len(snapshots)))
	}

	items := []string{head}
	// 运行中的排前面，任务多的时候被"还有 N 个"折叠掉的总是已经结束的那些
	ordered := orderTasks(snapshots)
	shown := ordered
	if len(shown) > maxTaskChips {
		shown = shown[:maxTaskChips]
	}
	for _, s := range shown {
		// 运行中的用状态色、结束的用暗色，扫一眼就知道哪个还在跑
		style := hintStyle
		if s.Running {
			style = toolStyle
		}
		chip := s.ID + " " + s.Status
		if s.Running {
			chip += " " + tool.FormatDuration(s.Elapsed)
		}
		items = append(items, style.Render(chip))
	}
	if rest := len(snapshots) - len(shown); rest > 0 {
		items = append(items, hintStyle.Render(fmt.Sprintf("…还有 %d 个", rest)))
	}
	return strings.Join(items, hintStyle.Render(" · "))
}

// orderTasks 把运行中的任务排到前面，任务多的时候被折叠掉的总是已经结束的那些
func orderTasks(snapshots []tool.TaskSnapshot) []tool.TaskSnapshot {
	out := make([]tool.TaskSnapshot, 0, len(snapshots))
	for _, s := range snapshots {
		if s.Running {
			out = append(out, s)
		}
	}
	for _, s := range snapshots {
		if !s.Running {
			out = append(out, s)
		}
	}
	return out
}

// taskDetailLines 把后台任务摊成一行一个，带上完整命令——上面那行标签只有 id 和状态，
// 任务一多就分不清哪个是哪个。放不下的先折叠，再交给 activeView 裁。
func (m model) taskDetailLines() []string {
	snapshots := tool.TaskSnapshots()
	if len(snapshots) == 0 {
		return nil
	}
	ordered := orderTasks(snapshots)
	out := make([]string, 0, len(ordered))
	for _, s := range ordered {
		style := hintStyle
		mark := "○"
		if s.Running {
			style, mark = toolStyle, "●"
		}
		line := style.Render(mark+" "+s.ID) + hintStyle.Render(" "+s.Status)
		if s.Running {
			line += hintStyle.Render(" " + tool.FormatDuration(s.Elapsed))
		}
		if cmd := firstLines(s.Command, 48); cmd != "" {
			line += hintStyle.Render("  $ " + cmd)
		}
		out = append(out, line)
	}
	// 槽位只放得下 activeRows 行，多出来的折成一句，别让人以为任务就这几个
	if len(out) > activeRows {
		kept := append([]string{}, out[:activeRows-1]...)
		kept = append(kept, hintStyle.Render(fmt.Sprintf("…还有 %d 个任务", len(out)-(activeRows-1))))
		out = kept
	}
	return out
}

// lastAnswerLines 把上一轮回答的尾巴放进这块槽位：历史区里的回答会越推越高，
// 想再看一眼刚才说到哪，扫这儿最快。用暗色，跟真正的回答区分开。
func (m model) lastAnswerLines() []string {
	text := strings.TrimSpace(m.lastAnswer)
	if text == "" {
		return nil
	}
	lines := strings.Split(text, "\n")
	// 先折行再取尾巴，免得一条超长行折出来把槽位撑破
	if limit := m.width - 3; limit > 0 {
		wrapped := make([]string, 0, len(lines))
		for _, line := range lines {
			wrapped = append(wrapped, strings.Split(
				ansi.Wrap(strings.TrimRight(line, "\r"), limit, ""), "\n")...)
		}
		lines = wrapped
	}
	for len(lines) > 0 && strings.TrimSpace(lines[len(lines)-1]) == "" {
		lines = lines[:len(lines)-1]
	}
	if len(lines) == 0 {
		return nil
	}

	title := "上一轮回答"
	if body := activeRows - 1; len(lines) > body {
		lines = lines[len(lines)-body:]
		title = "上一轮回答（结尾）"
	}
	out := make([]string, 0, len(lines)+1)
	out = append(out, hintStyle.Render(title))
	for _, line := range lines {
		out = append(out, hintStyle.Render("  "+line))
	}
	return out
}

func (m model) View() tea.View {
	if m.quitting {
		return tea.NewView("")
	}
	var b strings.Builder
	if m.state == stateConfig {
		b.WriteString(strings.Join(m.fieldLines(), "\n"))
		b.WriteString("\n")
		b.WriteString(hintStyle.Render("↑↓ 选字段 · Enter 编辑 · Ctrl+S 保存 · Esc 返回"))
		return tea.NewView(b.String())
	}
	if m.state == stateApproval {
		// 确认框顶替输入框：这一刻只能回答 y / a / n
		if line := m.tasksLine(); line != "" {
			b.WriteString(m.wrap(line))
			b.WriteString("\n")
		}
		if line := m.usageLine(); line != "" {
			b.WriteString(m.wrap(line))
			b.WriteString("\n")
		}
		b.WriteString(m.approvalView())
		b.WriteString("\n")
		b.WriteString(hintStyle.Render("↑↓ 或 j k 选择 · Enter 确认 · 直接打字可以拒绝并说明"))
		return tea.NewView(b.String())
	}
	// 后台任务和上下文占用常驻在输入框上方，运行中也一直看得到
	if line := m.tasksLine(); line != "" {
		b.WriteString(m.wrap(line))
		b.WriteString("\n")
	}
	if line := m.usageLine(); line != "" {
		b.WriteString(m.wrap(line))
		b.WriteString("\n")
	}
	// 运行进度和补全候选共用这块固定行数的位置，紧挨着输入框
	b.WriteString(m.activeView())
	b.WriteString("\n")
	// 附图那行贴着输入框：贴进来、挂在下一条提问上的图就在这儿，一眼看得见
	if line := m.attachmentsLine(); line != "" {
		b.WriteString(m.wrap(line))
		b.WriteString("\n")
	}
	b.WriteString(boxStyle.Width(m.width - 4).Render(m.input.View()))
	b.WriteString("\n")
	if m.state == stateRunning {
		b.WriteString(hintStyle.Render("Ctrl+C 中断"))
	} else {
		b.WriteString(hintStyle.Render("Enter 发送 · Shift+Enter 换行 · Ctrl+C 退出 · 模式 "))
		b.WriteString(statusStyle.Render(m.mode.Label()))
	}
	return tea.NewView(b.String())
}

// activeLines 是输入框上方那块会变的内容：跑任务时是状态、工具调用和预览尾巴，
// 闲着时是补全候选或别的常驻信息。行数不定，由 activeView 整理成固定行数。
func (m model) activeLines() []string {
	if m.state == stateRunning {
		return strings.Split(m.runningView(), "\n")
	}
	return m.idleLines()
}

// idleLines 是闲着时这块槽位放什么。这块高度恒定（见 activeRows）却又不能空着，
// 就按"最有用"的顺序挑一样填：补全候选最要紧，其次是把后台任务摊开看，
// 都没有就把上一轮回答的尾巴挂着，随时能瞄一眼刚才说到哪。
func (m model) idleLines() []string {
	if lines := m.completionLines(); len(lines) > 0 {
		return lines
	}
	if lines := m.taskDetailLines(); len(lines) > 0 {
		return lines
	}
	return m.lastAnswerLines()
}

// activeView 把这块内容整理成固定的 activeRows 行，始终贴着输入框：
// 跑任务时内容超了就先砍中间（留下第一行的状态或工具标题，以及最新的几行预览），
// 闲着时按顺序留着前面的候选；都不够就在顶上补空行。
// 这块的高度一旦跟着内容上下变，Bubble Tea 就会在屏幕底部留白，输入框立刻离地，
// 所以这里必须恒定。
func (m model) activeView() string {
	lines := m.activeLines()
	if len(lines) > activeRows {
		if m.state == stateRunning {
			kept := make([]string, 0, activeRows)
			kept = append(kept, lines[0])
			kept = append(kept, lines[len(lines)-(activeRows-1):]...)
			lines = kept
		} else {
			lines = lines[:activeRows]
		}
	}
	pad := make([]string, activeRows-len(lines))
	return strings.Join(append(pad, lines...), "\n")
}

// runningView 是跑任务时贴在底部的那几行：状态 + 思考和回答的尾巴。
// 有工具在跑的时候，第一行换成这次调用本身，圆圈的位置转圈。
// 这里不限制行数，超了由 activeView 裁到固定槽位里。
func (m model) runningView() string {
	var b strings.Builder
	if len(m.pending) > 0 {
		b.WriteString(strings.Join(withMark(m.pendingLines(), m.spinMark()), "\n"))
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

// print 把一段文本打进上方的历史区。
// tea.Println 是绕过 Bubble Tea 的屏幕缓冲区、直接往终端写的：行宽一旦超过终端，
// 终端会自己折行，程序记的行数就和屏幕实际用的行数对不上，之后每一行都会错位，
// 最先看出来的是贴在最下面的输入框——上下边框被挤掉、界面糊成一片。
// 所以所有打印都得先按终端宽度折好再交出去。
func (m model) print(text string) tea.Cmd {
	return tea.Println(m.fitWidth(text))
}

// fitWidth 把每一行折到终端宽度以内。留一列余量，避免正好写满最后一列，
// 有的终端写满后会把光标挂在行尾，下一个字符就多折出一行。
// 空行原样保留，行数不变——开场白靠空行把输入框顶到屏幕底部。
func (m model) fitWidth(text string) string {
	limit := m.width - 1
	if limit <= 0 {
		return text
	}
	lines := strings.Split(text, "\n")
	for i, line := range lines {
		// Windows 下的输出是 \r\n，行尾的 \r 留着会把光标拉回行首
		lines[i] = ansi.Wrap(strings.TrimRight(line, "\r"), limit, "")
	}
	return strings.Join(lines, "\n")
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

// deepSeekConfig 从配置里解出 DeepSeek 这一段。解不出来时返回零值，
// 让表单能打开、从头填，而不是直接报错退出界面。
func deepSeekConfig(cfg config.GlobalConfig) config.DeepSeekConfig {
	var dc config.DeepSeekConfig
	if err := cfg.Provider.Decode(&dc); err != nil {
		return config.DeepSeekConfig{}
	}
	return dc
}

// buildProvider 按配置里声明的类型造 provider
func buildProvider(cfg config.GlobalConfig) (domain.Provider, error) {
	var probe config.ProbeConfig
	if err := cfg.Provider.Decode(&probe); err != nil {
		return nil, fmt.Errorf("provider 解析为 ProbeConfig 报错: %w", err)
	}
	switch probe.Type {
	case "DeepSeek":
		p, err := provider.NewDeepSeek(deepSeekConfig(cfg))
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
