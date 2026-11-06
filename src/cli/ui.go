package main

import (
	"context"
	"encoding/json"
	"errors"
	"flag"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"strconv"
	"strings"
	"sync"
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

// defaultCfgPath 是配置文件的默认位置：用户目录下的 .agent-dev/config.yaml，跟工作区无关。
// 取不到用户目录时退回当前目录的 config.yaml，好歹能启动。
//
// 跑测试时一律落到临时目录。这个值只有测试（newModel 造界面）会用到——主流程拿到
// 配置路径后会显式盖掉它——而一个忘了把 cfgPath 指到临时目录的用例撞上真实家目录，
// 就会把用户的真配置写坏。2026-11-06 就这么坏过一次：那次是在一个不含
// testmain_test.go 的干净检出里跑的测试，进程级的家目录防护根本不在。
func defaultCfgPath() string {
	if runningUnderTest() {
		return testCfgPath()
	}
	path, err := config.DefaultPath()
	if err != nil {
		return "config.yaml"
	}
	return path
}

// runningUnderTest 报告当前二进制是不是 go test 编出来的。
// 认标准库 testing 注册的那个 -test.v 标志：跨平台，不依赖文件名后缀
func runningUnderTest() bool {
	return flag.Lookup("test.v") != nil
}

// testCfgPath 是测试里用的配置路径，整个测试进程共用一份临时文件
// （和 TestMain 把家目录挪到临时目录的效果一样）。用 Once 是因为一个进程里
// 会造很多个界面，每次新建临时目录既浪费又互相看不到对方写的配置
var testCfgOnce struct {
	once sync.Once
	path string
}

func testCfgPath() string {
	testCfgOnce.once.Do(func() {
		dir, err := os.MkdirTemp("", "agent-dev-test-cfg")
		if err != nil {
			return
		}
		testCfgOnce.path = filepath.Join(dir, "config.yaml")
	})
	return testCfgOnce.path
}

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
	// maxActiveRows 是输入框上方那块会变的内容最多占几行：跑任务时是状态、工具调用
	// 和预览的尾巴，闲着时是补全候选或别的常驻信息。待办清单也从这块里分位置。
	//
	// 它是上限不是定值：实际占几行由内容自己决定（没内容就一行不占），
	// 再由 activeRows() 按终端高度压到更低——帧一旦高到快占满屏幕，inline 渲染的
	// 滚动账就算不对了，屏幕上会叠出好几份界面。
	// 10 行 = 待办清单（标题 + 5 条）再留 4 行给状态和预览。
	maxActiveRows = 10
	// minActiveRows 是再挤也要给这块留的行数，不然运行状态完全看不见
	minActiveRows = 1
	// maxTodoRows 是待办清单最多列几行（不含标题那一行），多出来的滚动
	maxTodoRows = 5
	// todoMinRest 是清单之外的内容至少留几行：运行中的状态行最要紧，不能全被清单吃掉
	todoMinRest = 2
	// chromeRows 是帧里除输入框上方那块以外固定占掉的行数：任务栏 + 附图 +
	// 输入框（上边框 + 3 行内容 + 下边框）+ 上下文占用 + 按键提示
	chromeRows = 9
	// reserveHistoryRows 是帧上方至少留给历史区的行数。这块不能是 0：print
	// 是靠"光标此刻在最后一屏的底部"来插行的，帧顶到终端顶上时这个前提就废了
	reserveHistoryRows = 3
)

// activeRows 是输入框上方那块内容最多能占几行（上限，不是固定高度）。
//
// 实际占几行由内容自己决定（见 activeView），这里只管住天花板：帧高一旦逼近屏幕
// 高度，每次 print 往上方插行时算出来的滚动量就偏了，屏幕上会留下好几份叠在一起的
// 界面（状态行、输入框边框、上下文占用各出现好几遍）。宁可少显示几行预览，
// 也不能让帧把屏幕占满。
func (m model) activeRows() int {
	if m.height <= 0 {
		return maxActiveRows
	}
	avail := m.height - chromeRows - reserveHistoryRows
	if avail < minActiveRows {
		return minActiveRows
	}
	if avail > maxActiveRows {
		return maxActiveRows
	}
	return avail
}

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
	// 排队那几条用黄色：它们是"刚敲进去、还没发出去"的东西，和灰底的提示、
	// 绿底的输入标记都不是一回事，扫一眼就能分清屏幕上哪块是等着的消息
	queueStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("220"))
	// queueSelStyle 是排队里被选中的那条：同样黄色，加粗，配前面的 ▸
	queueSelStyle = lipgloss.NewStyle().Bold(true).Foreground(lipgloss.Color("220"))
	boxStyle      = lipgloss.NewStyle().
			Border(lipgloss.RoundedBorder()).
			BorderForeground(lipgloss.Color("238")).
			Padding(0, 1)

	// boxOverhead 是 boxStyle 边框和内边距总共吃掉的宽度：
	// 左右各 1 格边框 + 左右各 1 格内边距 = 4 格。
	// 输入框内部能放字的宽度必须是"框总宽 - 4"，算错了字会提前折行、框被撑高。
	boxOverhead = 4

	// 上下文占用里三类来源各用一种颜色，扫一眼就知道是哪一块在涨
	usageSystemStyle = lipgloss.NewStyle().Foreground(lipgloss.Color("141"))
	usageToolStyle   = lipgloss.NewStyle().Foreground(lipgloss.Color("215"))
	usageMsgStyle    = lipgloss.NewStyle().Foreground(lipgloss.Color("114"))
)

// 从模型那边流回来的消息
type eventMsg domain.Event

// doneMsg 表示这一轮跑完了
type doneMsg struct{ err error }

// printedMsg 表示"刚往历史区打了一批内容"，用来把被插行弄丢的真光标放回输入框
type printedMsg struct{}

// cursorReleaseMsg 表示"重画完了，真光标可以交回去了"（见 cursorHold）
type cursorReleaseMsg struct{}

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

	// skills 是用户目录下装好的技能，启动时读一次。输入补全和 /技能名 手动调用
	// 都靠它；运行期不重扫，中途装的技能重启之后才出现
	skills []core.Skill

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
	// attachments 是贴进来、等着随下一条提问一起发出去的图片。
	// 发送、切换会话、按 Esc 都会清空
	attachments []imageAttachment

	// queue 是运行中排队等下一条的提问。一轮跑完（正常结束）会自动接上队头那条，
	// 不用等用户再敲一遍；中断或出错时留着，发不发由人决定
	queue []queuedPrompt
	// queueIdx 是队列里选中的那一条，-1 表示没选（键盘全归输入框）。
	// 只有运行中按 ↑↓ 会改它，一打字就复位
	queueIdx int

	// usage 是最后一次请求的上下文占用（总数来自响应里的 usage），
	// 贴在输入框下面常驻显示，新一轮开始时清零
	usage domain.ContextUsage
	// contextLimit 是配置里的上下文窗口大小，用来算已用百分比
	contextLimit int

	// cursorHold 为 true 表示这一小会儿别报真光标位置：终端刚改过尺寸，屏幕上
	// 内容被重排过，渲染器那本账要等整帧重画完才对得回来（见 WindowSizeMsg）
	cursorHold bool
	// printSeq 是"往历史区打了几批内容"的计数，只有一个用处：让帧内容每打一次就换
	// 个样子（见 printMark），逼渲染器重画一帧、把真光标摆回输入框。
	// 用指针是因为 model 到处按值复制，打在副本上的 print 也得让所有副本看到同一个数
	printSeq *int
	// height 是终端高度，用来算输入框上方那块最多能占几行（activeRows）
	height int
	// screenRows 是屏幕上此刻这一帧有几行（上一条消息处理完的样子）。帧高不是常数了：
	// 待办清单出现或收起、确认框顶掉输入框，都会让它变。往上插历史行时渲染器是按
	// 屏幕上那一帧的位置算的，所以插多少行要按"这两帧里高的那个"来限（见 printRows）
	screenRows int
	// started 表示开场白已经打过了：等第一次收到窗口尺寸再打，只打一次
	started bool
	// tasksTicking 表示"后台任务耗时"的定时刷新已经转起来了，避免重复起定时器
	tasksTicking bool

	// mode 是当前工作模式，决定给模型哪些工具、写操作要不要问
	mode domain.Mode
	// quiet 是精简显示：历史区只打正文，思考过程和工具输出的内容都不打
	// （工具调用那行标题还在，知道在跑什么）
	quiet bool
	// approval 是正在等用户确认的那项写操作，为 nil 表示没有在等
	approval *approvalState
	// picker 是打开着的"按键挑一个"列表（/resume、/model、/mode、/effort 不带参数时），
	// 为 nil 表示没开着。它只占输入框上方那块槽位，不换状态——输入框照旧能打字，
	// 一打字这块列表就收起（见 handlePickKey）
	picker *picker

	// pendingDelete 是上一次请求删掉的那套模型（/model del 名字）。
	// 再打一遍同样的命令才真删——删配置没有后悔药
	pendingDelete string

	// notice 是启动时想顺带说一句的事情（比如刚把老会话迁过来），空着就不显示
	notice string

	width    int
	quitting bool
}

// loadSkills 读用户目录下的技能清单。单独抽成一个变量是为了测试：
// 不抽的话每个用 newModel 的测试都得依赖本机装了什么技能
var loadSkills = core.Skills

func newModel(cfg config.GlobalConfig, store *session.Store, workdir string, cur *domain.Session) model {
	input := textarea.New()
	input.Placeholder = "问点什么，/help 看命令"
	input.ShowLineNumbers = false
	// 高度固定 3 行：整块界面依赖"行数恒定"来对齐，输入框一旦跟着内容长高，
	// 下面的行就被挤出屏幕，inline 渲染会把顶上的边框裁掉，看着像输入框缺了一块。
	// 内容超过 3 行时 textarea 自己滚动。
	input.MaxHeight = 3
	input.SetHeight(3)
	input.Prompt = "" // 输入框前面不放箭头，边框已经够用了
	styles := input.Styles()
	styles.Focused.Prompt = promptStyle
	styles.Focused.CursorLine = lipgloss.NewStyle()
	styles.Blurred = styles.Focused
	input.SetStyles(styles)
	// Enter 留给"发送"，换行用 Shift+Enter / Ctrl+J。
	// Windows 上真正能用的只有 Ctrl+J：bubbletea 给 stdin 开了
	// ENABLE_VIRTUAL_TERMINAL_INPUT（见 bubbletea/v2 tty_windows.go 的 initInput），
	// ultraviolet 在这个分支里只把每个按键的字符交给解码器、把 ControlKeyState
	// 整个丢掉（terminal_reader_windows.go 的 serializeWin32InputRecords），
	// 于是 Shift+Enter 和 Enter 送到这里一模一样，都是 0x0D。Ctrl+J 是 0x0A，
	// 字节本来就不同，不靠修饰键也分得开。
	input.KeyMap.InsertNewline = key.NewBinding(key.WithKeys("shift+enter", "ctrl+j"))
	// 光标交给终端画：输入法（IME）的候选框跟着终端的真光标走，跟着画出来的方块走不了。
	// 关掉虚拟光标之后 textarea 的 Cursor() 才有值，View 里把它的位置报给终端
	input.SetVirtualCursor(false)
	// 窗口尺寸还不知道，先给个占位宽度；收到 WindowSizeMsg 会按真实宽度重设
	input.SetWidth(76 - boxOverhead)
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
	// 配置里可能写了好几套模型，用 Current 指的那套；解不出来就留空，界面照样能开
	current, _ := cfg.CurrentModel()

	return model{
		cfg:          cfg,
		cfgPath:      defaultCfgPath(),
		store:        store,
		workdir:      workdir,
		cur:          cur,
		skills:       loadSkills(),
		input:        input,
		spin:         spin,
		toolSpin:     toolSpin,
		state:        stateIdle,
		form:         newConfigForm(current, current.Label()),
		contextLimit: current.Limit(),
		mode:         mode,
		quiet:        cfg.QuietMode(),
		reasoning:    &strings.Builder{},
		answer:       &strings.Builder{},
		printSeq:     new(int),
		queueIdx:     -1,
		width:        80,
	}
}

func (m model) Init() tea.Cmd {
	// 开场白留到第一次收到窗口尺寸时再打（见 WindowSizeMsg），这里只管让光标闪起来
	return textarea.Blink
}

// newlineHint 是"换行按什么键"这条提示的文案。
//
// Windows 上 Shift+Enter 到不了程序：bubbletea 给 stdin 开了
// ENABLE_VIRTUAL_TERMINAL_INPUT，ultraviolet 在这个模式下只把每个按键的字符
// 交给解码器、把 ControlKeyState 整个丢掉（来路见 newModel 里 InsertNewline
// 那段注释），所以 Shift+Enter 进来就是一个普通回车。Ctrl+J 是另一个字节
// （0x0A，换行符本身），不靠修饰键也分得开，所以 Windows 上只承诺它。
func newlineHint() string {
	if runtime.GOOS == "windows" {
		return "Ctrl+J 换行"
	}
	return "Shift+Enter 换行"
}

// bannerLines 是开场那几行
func (m model) bannerLines() []string {
	lines := []string{
		bannerStyle.Render("agent-dev") + hintStyle.Render("  会话 "+m.cur.Id),
		hintStyle.Render("模型 ") + statusStyle.Render(m.modelLabel()) +
			hintStyle.Render(" · 思考 ") + statusStyle.Render(m.effortLabel()) +
			hintStyle.Render(" · 模式 ") + statusStyle.Render(m.mode.Label()) +
			hintStyle.Render(" · Enter 发送 · "+newlineHint()+" · /help 看命令"),
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

// startupText 是开场白。只打 bannerLines 那几行，不再往上补空行：界面就跟在
// 开场白下面，会话长了自然往下滚，输入框不用贴底。
func (m model) startupText() string {
	return strings.Join(m.bannerLines(), "\n")
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
	// 这一帧马上就要画到屏幕上了，记下它有几行：下一条消息里的 print 要按它限额
	// （渲染器插行时算的是屏幕上那一帧的位置）
	mm.screenRows = mm.frameRows()
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

// cursorReleaseTime 是改尺寸之后连着藏几帧光标：渲染器要攒够一次重画（60fps，
// 一帧 16ms），留三帧余量
const cursorReleaseTime = 50 * time.Millisecond

// cursorRelease 是"再过一会儿把真光标交回去"
func cursorRelease() tea.Cmd {
	return tea.Tick(cursorReleaseTime, func(time.Time) tea.Msg { return cursorReleaseMsg{} })
}

func (m model) update(msg tea.Msg) (tea.Model, tea.Cmd) {
	switch msg := msg.(type) {
	case tea.WindowSizeMsg:
		prevWidth, prevHeight := m.width, m.height
		m.width = max(msg.Width, 24)
		m.height = max(msg.Height, 10)
		// 输入框能放字的宽度 = 框总宽(width-4) 减去边框内边距(boxOverhead)
		m.input.SetWidth(max(m.width-4-boxOverhead, 20))
		m.resizeForm()
		if !m.started {
			// 第一次知道窗口多大：这时候打开场白，界面排版已经就绪，只打这一次
			m.started = true
			return m, tea.Sequence(m.print(m.startupText()), textarea.Blink)
		}
		if m.width != prevWidth || m.height != prevHeight {
			// 尺寸一变，渲染器记的那本账（帧在第几行、光标在哪）就作废了：终端已经把
			// 屏幕上的内容重排过，而 inline 用的是相对光标，挪一格的落点全靠这本账。
			// 这里干脆把账本清零重来：先打一屏空行把老内容顶出去，再清屏重画。
			// 重画是逐行写出去的，写的同时账本也就对回来了。
			//
			// 光标这一小会儿先藏起来：账没对回来的时候报位置，它只会把光标摆错地方，
			// 接着按错位置去擦、去画，屏幕上就留下一截旧帧。
			m.cursorHold = true
			return m, tea.Sequence(
				m.print(strings.Repeat("\n", m.height)),
				m.rebuild(),
				cursorRelease(),
			)
		}
		return m, nil

	case taskTickMsg:
		// 这一拍用掉了，交给外层的 withTaskTicker 决定要不要再续一拍
		m.tasksTicking = false
		return m, nil

	case printedMsg:
		// 刚插过行，真光标被渲染器扔到帧顶上去了。换个帧标记让它重画一帧，
		// 重画时才会按 View.Cursor 把光标摆回输入框（见 printMark）
		*m.printSeq++
		return m, nil

	case cursorReleaseMsg:
		m.cursorHold = false
		return m, nil

	case modelsFetchedMsg:
		// 只认表单开着时候的结果：表单已经关了，这份列表就没用了
		if m.state != stateConfig {
			return m, nil
		}
		return m.applyModelList(msg), nil

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
	// 列表开着的时候键先归它：↑↓ 挪光标、Enter 定下来、Esc 收起。
	// 别的键它会把列表收起来再交回这里，所以下面那些处理一条都不用改
	if m.picker != nil {
		return m.handlePickKey(msg)
	}

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

	case "ctrl+l":
		// 屏幕上要是留下了上一帧的残影（拖过窗口、滚过滚轮之后容易出现），
		// 按这个键把整块内容按内存里的底账重打一遍，界面立刻恢复干净
		return m, m.rebuild()

	case "esc":
		// 选了排队里的某条就先取消选中，键盘还给输入框
		if m.queueIdx >= 0 {
			m.queueIdx = -1
			return m, nil
		}
		// 附了图想撤：闲着时 Esc 没别的用处，就用来清空
		if m.state == stateIdle && len(m.attachments) > 0 {
			m.attachments = nil
			return m, m.print(hintStyle.Render("已清空附图"))
		}

	// 排队消息的编辑和删除。队列空着就完全不拦，键照常给输入框。
	//
	// 这里用 Alt+E 而不是 Ctrl+E：textarea 默认把 ctrl+e 绑成"移到行尾"
	// （bubbles textarea 的 KeyMap.LineEnd），抢了就没人能在多行里一键跳到行尾
	case "alt+e":
		if len(m.queue) > 0 {
			return m.editQueued()
		}

	case "delete":
		// 只在明确选中了某条时才拦 Delete：输入框里 Delete 是"删光标后面那个字"，
		// 手一快就会把正在写的正文删掉
		if m.queueIdx >= 0 && len(m.queue) > 0 {
			return m.removeQueued()
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
			if specs := m.suggestAll(); len(specs) > 0 {
				return m.complete(specs)
			}
		}
		if !movingRight {
			return m, nil // 补全键吞掉；→ 没候选就照常交给输入框右移
		}
	}

	// 输入框上方有补全候选时，↑↓ 选、Enter 补全
	if m.state == stateIdle {
		if specs := m.suggestAll(); len(specs) > 0 {
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

	// Alt+↑ / Alt+↓ 挑排队里的某条。用带修饰键的版本是不想占裸的 ↑↓——
	// 输入框里写着多行内容时，裸 ↑↓ 得留给挪光标，那是写字时更常用的键
	if m.state == stateRunning && len(m.queue) > 0 {
		switch msg.String() {
		case "alt+up":
			m.moveQueueSel(-1)
			return m, nil
		case "alt+down":
			m.moveQueueSel(1)
			return m, nil
		}
	}

	// 没按 Alt 时，只有输入框空着（或已经选着某条）↑↓ 才归排队：
	// 空输入框里本来就没东西可挪光标。这么留着是给 Alt+方向键送不到程序的环境兜底
	// （有的终端会把它接走，见 takesQueueArrows）
	if m.takesQueueArrows() {
		switch msg.String() {
		case "up":
			m.moveQueueSel(-1)
			return m, nil
		case "down":
			m.moveQueueSel(1)
			return m, nil
		}
	}

	if msg.Code == tea.KeyEnter {
		if m.state == stateRunning {
			// 运行中按回车不再丢弃：排进队里，这一轮结束自动接着发
			if m.enqueue() {
				return m, nil
			}
			return m, m.print(hintStyle.Render("这一轮还在跑；想排队就先把消息写上"))
		}
		if !key.Matches(msg, m.input.KeyMap.InsertNewline) {
			return m.submit() // 光按 Enter 是发送；Shift+Enter 之类落到下面交给输入框换行
		}
	}

	// 一打字就取消排队里的选中：否则用户以为在往输入框写，实际焦点还挂在某条上
	if msg.Text != "" {
		m.queueIdx = -1
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
	case "TodoWrite":
		var args tool.TodoArgs
		if json.Unmarshal([]byte(call.Arguments), &args) != nil {
			return nil
		}
		if len(args.Todos) == 0 {
			return []string{hintStyle.Render("  清空清单")}
		}
		return []string{hintStyle.Render("  " + tool.TodoProgress(args.Todos))}
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

// suggestAll 是输入框上方的全部补全候选：内置命令排前面，技能接在后面。
// 内置命令永远在前，所以打一个 / 时第一项还是 /help，不会因为装了技能就换位
func (m model) suggestAll() []commandSpec {
	return append(suggestCommands(m.input.Value()), suggestSkills(m.input.Value(), m.skills)...)
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

// completionLines 是输入框上方的补全候选，选中的一行高亮。
// 候选可能上百个（技能一装就是几十个），这块槽位又只有 maxActiveRows 行，
// 所以只显示选中项附近的一段，另外用一行交代总共有多少。
func (m model) completionLines() []string {
	specs := m.suggestAll()
	if len(specs) == 0 {
		return nil
	}
	cur := m.curSuggest(specs)
	total := len(specs)
	// 槽位高度随终端缩，窗口也跟着它走：留一行给末尾那句说明
	rows := m.activeRows()
	if rows < 2 {
		rows = 2
	}
	start, end := 0, total
	overflow := total > rows
	if overflow {
		visible := rows - 1
		start = cur - visible/2
		if start < 0 {
			start = 0
		}
		if start > total-visible {
			start = total - visible
		}
		end = start + visible
	}
	// 技能名长短差很多（add-disease 这样的比 /mode 长得多），
	// 列宽按窗口里最长的那条算，免得名字和说明粘在一起
	width := 9
	for _, spec := range specs[start:end] {
		if n := len([]rune(spec.name)); n > width {
			width = n
		}
	}
	out := make([]string, 0, rows)
	for i := start; i < end; i++ {
		name := fmt.Sprintf("%-*s", width, specs[i].name)
		if i == cur {
			out = append(out, promptStyle.Render("▸ "+name)+hintStyle.Render(specs[i].desc))
			continue
		}
		out = append(out, hintStyle.Render("  "+name+specs[i].desc))
	}
	if overflow {
		out = append(out, hintStyle.Render(
			fmt.Sprintf("  …共 %d 个候选，↑↓ 选，继续打字缩小范围", total)))
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
// 发送、Esc 或者切会话之后消失。宽度不够时舍掉尾巴上的细节，保证只占一行。
func (m model) attachmentsLine() string {
	if len(m.attachments) == 0 {
		return ""
	}
	items := []string{hintStyle.Render("附图 ") + statusStyle.Render(fmt.Sprintf("%d 张", len(m.attachments)))}
	for _, a := range m.attachments {
		items = append(items, hintStyle.Render(fmt.Sprintf("%s %s", sizeText(int64(a.img.Size())), a.img.Kind())))
	}
	items = append(items, hintStyle.Render("Esc 清空 · Enter 发送"))
	return fitItems(items, m.width-4)
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
		prompt := imagePrompt(len(m.attachments))
		return m.startRun(prompt, prompt, m.takeAttachments())
	}

	switch parseCommand(input) {
	case cmdExit:
		m.quitting = true
		return m, tea.Quit
	case cmdHelp:
		return m, m.print(helpText())
	case cmdNew:
		m.cur = domain.NewSession(m.workdir)
		// 新会话还没起标题，窗口标题退回程序名：别继续挂着上一个会话的名字
		setWindowTitle(m.cur.Title)
		// 新会话，攒着的图不再属于谁，待办清单同理：它是上一个任务的进度表。
		// 排队消息也一样——那是排给上一段对话的下一步
		m.attachments = nil
		m.queue = nil
		m.queueIdx = -1
		tool.ClearTodos()
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
	case cmdModel:
		return m.switchModel(input)
	case cmdEffort:
		return m.adjustEffort(input)
	case cmdSkills:
		return m, m.print(skillsText(m.skills))
	case cmdTodo:
		return m, m.print(todoText(tool.CurrentTodos()))
	case cmdQuiet:
		return m.toggleQuiet(input)
	}

	return m.runSkill(input)
}

// runSkill 处理 "不是内置命令" 的那一类输入：
// 认得出是技能（/技能名 [补充说明]）就把那份 SKILL.md 整段并进这一条提问；
// 认不出但形状像条命令的，提示一句就停，别把半截 /zzz 当成提问发给模型
func (m model) runSkill(input string) (tea.Model, tea.Cmd) {
	if skill, rest, ok := parseSkillCall(input, m.skills); ok {
		// 历史区只回显用户敲的那一句：真把几百行说明打出来，屏幕就糊了
		return m.startRun(input, core.SkillPrompt(skill, rest), m.takeAttachments())
	}
	if name := firstField(input); !looksLikeCommand(name) {
		return m.startRun(input, input, m.takeAttachments())
	}
	return m, m.print(unknownCommandText(firstField(input), m.skills))
}

// toggleQuiet 处理 /quiet：切换"精简显示"，并写回配置文件，下次启动还是这个设置。
// 不带参数就取反；带 on/off 就直接设成那一个。
func (m model) toggleQuiet(input string) (tea.Model, tea.Cmd) {
	want := !m.quiet
	switch arg := strings.ToLower(strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(input), "/quiet"))); arg {
	case "on", "1", "true", "开":
		want = true
	case "off", "0", "false", "关":
		want = false
	}
	m.quiet = want

	out := bannerStyle.Render("精简显示 已开")
	if !want {
		out = bannerStyle.Render("精简显示 已关")
	}
	out += hintStyle.Render("  " + quietHint(want))
	// 写回配置失败不影响本次运行，说明一下就行
	if err := config.SaveQuiet(m.cfgPath, want); err != nil {
		out += "\n" + hintStyle.Render("（没能写回配置: "+err.Error()+"，本次运行仍然生效）")
	}
	return m, m.print(out)
}

// quietHint 是一句话说明精简显示开着/关着时历史区里有什么
func quietHint(quiet bool) string {
	if quiet {
		return "只打正文：思考过程和工具输出的内容都不显示，工具调用只留一行标题"
	}
	return "思考过程、工具输出都会打进历史区；/quiet 可以切回来"
}

// switchMode 处理 /mode：不带参数就报当前模式和可选项，带了就切过去并写回配置，
// 下次启动还是这个模式。
func (m model) switchMode(input string) (tea.Model, tea.Cmd) {
	arg := ""
	if fields := strings.Fields(input); len(fields) > 1 {
		arg = strings.Join(fields[1:], " ")
	}
	if arg == "" {
		// 不带参数就把三种模式列成能按键挑的列表，↑↓ 选、Enter 切过去
		m.picker = modePicker(m.mode)
		return m, nil
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

// switchModel 处理 /model：不带参数就列出手上配置好的几套模型，带了就切过去并写回配置，
// 下一轮请求就用新模型，不用重启。add 开新增表单，del 删掉一套
func (m model) switchModel(input string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(input)
	arg := ""
	if len(fields) > 1 {
		arg = strings.Join(fields[1:], " ")
	}
	// add / del 是保留字：优先当命令解释，模型名不会叫这两个
	verb, rest := "", ""
	if arg != "" {
		verb = strings.ToLower(fields[1])
		if len(fields) > 2 {
			rest = strings.Join(fields[2:], " ")
		}
	}
	switch verb {
	case "add":
		m.pendingDelete = ""
		return m.openConfigNew()
	case "del", "delete":
		return m.deleteModel(rest)
	}
	// 别的事干过之后，之前那句"要不要删"就作废了：
	// 再打一遍 /model del 得重新问一次，不能隔着一堆别的操作就真删
	m.pendingDelete = ""

	models := m.cfg.AvailableModels()
	if arg == "" {
		if len(models) == 0 {
			return m, m.print(bannerStyle.Render("模型") + "\n" + hintStyle.Render(
				"  还没配模型：/config 填一套；/model add 也能新增一套"))
		}
		// 不带参数就把这几套列成能按键挑的列表，↑↓ 选、Enter 切过去
		m.picker = modelPicker(models, m.cfg.Current)
		return m, nil
	}

	target, ok := pickModel(models, arg)
	if !ok {
		return m, m.print(errStyle.Render(fmt.Sprintf("没有叫 %q 的模型；/model 看看有哪些", arg)))
	}
	// 内存里也把名字补齐，和写回文件的那份保持一致
	for i := range models {
		if strings.TrimSpace(models[i].Name) == "" {
			models[i].Name = models[i].Label()
		}
	}
	m.cfg.Current = target.Label()
	m.cfg.Models = models
	// 上下文窗口跟着换，界面上的"已用 x%"才不会是按上一套模型算的
	m.contextLimit = target.Limit()

	out := bannerStyle.Render("已切到模型 ") + statusStyle.Render(target.Label()) +
		hintStyle.Render("  "+modelHint(target))
	// 写回配置失败不影响本次运行，说明一下就行
	if err := config.SaveModels(m.cfgPath, models, m.cfg.Current); err != nil {
		out += "\n" + hintStyle.Render("（没能写回配置: "+err.Error()+"，本次运行仍然生效）")
	}
	return m, m.print(out)
}

// deleteModel 删掉一套模型。名字不给就列出来让你挑；
// 名字给了先问一句，再打一遍同样的命令才真删——删配置没有后悔药
func (m model) deleteModel(name string) (tea.Model, tea.Cmd) {
	models := m.cfg.AvailableModels()
	if len(models) == 0 {
		return m, m.print(errStyle.Render("还没配模型，没得删"))
	}
	if len(models) == 1 {
		return m, m.print(errStyle.Render("只剩一套模型了，删不掉"))
	}
	if name == "" {
		// 不带名字：列出来让你挑
		m.picker = modelDeletePicker(models)
		return m, nil
	}
	target, ok := pickModel(models, name)
	if !ok {
		return m, m.print(errStyle.Render(fmt.Sprintf("没有叫 %q 的模型；/model 看看有哪些", name)))
	}
	// 上一次已经问过、这次是同一个名字：真删
	if m.pendingDelete == target.Label() {
		m.pendingDelete = ""
		return m.removeModel(target)
	}
	m.pendingDelete = target.Label()
	return m, m.print(
		bannerStyle.Render("删掉 "+target.Label()+"？") + "\n" +
			hintStyle.Render("  再打一遍 /model del "+target.Label()+" 就删"))
}

// removeModel 把删掉的那套从配置里去掉，写回文件并同步内存。
// 删掉的正是当前那套时改用剩下的第一套，上下文窗口也跟着换
func (m model) removeModel(target config.ModelConfig) (tea.Model, tea.Cmd) {
	models, current, err := config.DeleteModel(m.cfg.AvailableModels(), m.cfg.Current, target.Label())
	if err != nil {
		return m, m.print(errStyle.Render("删除失败: " + err.Error()))
	}
	if err := config.SaveModels(m.cfgPath, models, current); err != nil {
		return m, m.print(errStyle.Render("删除失败: " + err.Error()))
	}
	m.cfg.Models = models
	m.cfg.Current = current
	m.syncLegacyProvider()
	// 上下文窗口按现在真正在跑的那套算
	if cur, err := m.cfg.CurrentModel(); err == nil {
		m.contextLimit = cur.Limit()
	}
	return m, m.print(bannerStyle.Render("已删掉 "+target.Label()) +
		hintStyle.Render(fmt.Sprintf("  还剩 %d 套，当前用 %s", len(models), m.currentModel().Label())))
}

// adjustEffort 处理 /effort：不带参数就报当前档位和可选项，带了就改成那一档并写回配置，
// 下一轮请求就带上新的 reasoning_effort，不用重启。
// 改的是当前生效的那套模型（/model 切过去的那套），不是别的
func (m model) adjustEffort(input string) (tea.Model, tea.Cmd) {
	arg := ""
	if fields := strings.Fields(input); len(fields) > 1 {
		arg = strings.Join(fields[1:], " ")
	}
	cur, err := m.cfg.CurrentModel()
	if err != nil {
		return m, m.print(errStyle.Render("调整思考强度失败: " + err.Error()))
	}
	if arg == "" {
		// 不带参数就把几档列成能按键挑的列表，↑↓ 选、Enter 改成那一档
		m.picker = effortPicker(cur)
		return m, nil
	}

	level, ok := effortArg(arg)
	if !ok {
		return m, m.print(errStyle.Render(fmt.Sprintf(
			"认不出思考强度 %q；可选 %s（/effort 看看当前是什么）", arg, strings.Join(config.EffortLabels(), " / "))))
	}

	// 名字先补齐，和 SaveModels 写回时补的一致，两边才认得出是同一套；
	// 改的是当前这套（按名字找，找不到退回第一套）
	next, target, err := config.WithEffort(m.cfg, level)
	if err != nil {
		return m, m.print(errStyle.Render("调整思考强度失败: " + err.Error()))
	}
	m.cfg = next

	out := bannerStyle.Render("思考强度 ") + statusStyle.Render(config.EffortLabel(level)) +
		hintStyle.Render("  "+effortHint(level)+" · "+target.Label())
	// 写回配置失败不影响本次运行，说明一下就行
	if err := config.SaveModels(m.cfgPath, m.cfg.Models, m.cfg.Current); err != nil {
		out += "\n" + hintStyle.Render("（没能写回配置: "+err.Error()+"，本次运行仍然生效）")
	}
	return m, m.print(out)
}

// effortArg 把 /effort 后面那截认成档位。档位名本身之外，"默认/auto/off" 都表示不发这个参数；
// 认不出来返回 false，让界面提示可选值
func effortArg(arg string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "默认", "default", "auto", "off", "none":
		return "", true
	}
	level, err := config.ParseEffort(arg)
	if err != nil {
		return "", false
	}
	return level, true
}

// effortHint 是一句话说明某个档位是什么意思
func effortHint(level string) string {
	switch config.EffortLabel(level) {
	case "minimal":
		return "几乎不思考，最快，只有认这档的接口支持"
	case "low":
		return "想得少一点，快"
	case "medium":
		return "均衡"
	case "high":
		return "想得最久最深，最慢也最费 token"
	default:
		return "不发这个参数，用模型自己的默认"
	}
}

// pickModel 把 /model 的参数认成一套配置。匹配规则在 config 包里，
// 浏览器界面的 /model 用的是同一份
func pickModel(models []config.ModelConfig, arg string) (config.ModelConfig, bool) {
	return config.PickModel(models, arg)
}

// modelHint 是一句话说明某套模型配的是什么
func modelHint(mc config.ModelConfig) string {
	kind := strings.TrimSpace(mc.Type)
	if kind == "" {
		kind = "DeepSeek"
	}
	out := kind
	if model := strings.TrimSpace(mc.Model); model != "" {
		out += " · " + model
	}
	if url := strings.TrimSpace(mc.ApiUrl); url != "" {
		out += " · " + url
	}
	if effort := strings.TrimSpace(mc.ReasoningEffort); effort != "" {
		out += " · 思考 " + config.EffortLabel(effort)
	}
	return out + " · 上下文 " + domain.FormatTokens(mc.Limit())
}

// orDash 空串显示成破折号
func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

// todoText 是 /todo 的输出：这次任务的清单，一条都没有时说明怎么让它有
func todoText(items []tool.TodoItem) string {
	title := bannerStyle.Render("待办")
	if len(items) == 0 {
		return title + "\n" + hintStyle.Render(
			"  现在是空的：多步任务里模型会自己用 TodoWrite 列清单，输入框上方也能看到进度")
	}
	done, running := tool.TodoCounts(items)
	title += hintStyle.Render(fmt.Sprintf("  %d/%d 完成", done, len(items)))
	if running > 0 {
		title += hintStyle.Render(fmt.Sprintf(" · %d 项进行中", running))
	}
	lines := []string{title}
	for i, it := range items {
		mark, style := "[ ]", hintStyle
		switch it.Status {
		case tool.TodoCompleted:
			mark, style = "[x]", toolOKStyle
		case tool.TodoInProgress:
			mark, style = "[~]", promptStyle
		}
		lines = append(lines, style.Render(fmt.Sprintf("  %s %d. %s", mark, i+1, it.Content)))
	}
	return strings.Join(lines, "\n")
}

// resume 处理 /resume：不带参数列出现有会话，带参数就切过去。
// 参数可以是列表里的序号（1 是最新的），也可以是完整的会话 id。
// 切过去之后把这段会话重新打进历史区（见 historyLines），让人一眼看到聊到哪儿了；
// 下一句提问就接着这段历史往下聊，会话文件也接着往同一个 id 里写。
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
		if len(infos) == 0 {
			return m, m.print(bannerStyle.Render("会话列表") + "\n" +
				hintStyle.Render("  还没有存过的会话：跑完一轮就会自动存一份"))
		}
		// 不带参数就把会话列成一块能按键挑的列表，↑↓ 选、Enter 恢复。
		// 和手打 /resume 序号 走同一条路（见 pickApply）
		m.picker = sessionPicker(infos, m.cur)
		return m, nil
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
	// 换会话了，窗口标题跟着换成这段会话的名字；老会话没标题就退回程序名，
	// 总之不能继续挂着上一个会话的名字
	setWindowTitle(next.Title)
	// 换会话了，攒着的图不再属于谁；上下文占用那行也得清掉，它说的是上一个会话
	// 的用量，留着会让人以为这段历史已经占了那么多窗口。排队消息同理，那是排给上一段对话的
	m.attachments = nil
	m.queue = nil
	m.queueIdx = -1
	m.usage = domain.ContextUsage{}
	// 待办清单同理：那是上一个会话里的任务
	tool.ClearTodos()

	// 把这段会话重新打进历史区，和当时聊出来的样子一样
	return m, m.print(m.resumeText(next))
}

// resumeText 是恢复会话之后打进历史区的那一整段：先把这段历史重打一遍，
// 末尾跟一行说明（恢复成了哪个会话、共几条消息）。
// 说明放在最后是为了随时看得见——历史一长，顶上那行早就被顶出屏幕了。
func (m model) resumeText(next *domain.Session) string {
	out := m.historyLines(next)
	if len(out) > 0 {
		out = append(out, "")
	}
	out = append(out, bannerStyle.Render("已恢复会话 "+next.Id)+
		hintStyle.Render(fmt.Sprintf("，共 %d 条消息，目录 %s", len(next.Messages), m.store.Dir)))
	// 起过标题的会话把名字也带上，好和列表里那一行对得上
	if title := strings.TrimSpace(next.Title); title != "" {
		out = append(out, hintStyle.Render("  标题 "+title))
	}
	// 别的目录里开的会话也照样能接着聊，只是工具仍按当前目录跑，提一句免得误会
	if next.WorkDir != "" && next.WorkDir != m.workdir {
		out = append(out, hintStyle.Render("  这个会话是在 "+next.WorkDir+" 开的；继续聊时工具还是在当前目录 "+
			m.workdir+" 里跑"))
	}
	return strings.Join(out, "\n")
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

// historyLines 把一份已存的会话重新渲染成历史区的样子：提问、思考、回答、
// 工具调用和它的结果按原来的顺序排开，和当时这一轮跑完打出去的内容一致。
//
// 会话文件里只存了内容、调用参数和结果文本，没存工具状态和耗时，所以圆圈按结果
// 的开头认（core 失败时固定写成"错误: …"或"执行失败: …"），耗时不显示——
// 那是当时的过程信息，重打一遍历史也不必编一个出来。
// 精简显示（quiet）下思考过程和工具输出同样不打，和当时跑的规矩一致。
func (m model) historyLines(s *domain.Session) []string {
	if s == nil {
		return nil
	}
	// 工具结果按调用 id 对回去，好贴在对应那次调用的下面
	results := make(map[string]domain.SessionMessage, len(s.Messages))
	for _, msg := range s.Messages {
		if msg.Role == domain.RoleTool && msg.ToolCallId != "" {
			results[msg.ToolCallId] = msg
		}
	}
	// used 记下哪些结果已经跟着调用一起打过了，轮到那条 tool 消息时跳过
	used := make(map[string]bool, len(results))

	var out []string
	for _, msg := range s.Messages {
		switch msg.Role {
		case domain.RoleUser:
			// 只有图片没有文字的提问也要留着，那也是一条正经提问
			if msg.Content == "" && len(msg.Images) == 0 {
				continue
			}
			out = append(out, promptStyle.Render(m.fitWidth(msg.Content)))
			if len(msg.Images) > 0 {
				out = append(out, hintStyle.Render(fmt.Sprintf("  （附 %d 张图片）", len(msg.Images))))
			}
		case domain.RoleAssistant:
			if !m.quiet {
				out = append(out, m.block("思考", thinkStyle, msg.Reasoning, printReasoningLines)...)
			}
			out = append(out, m.block("", answerStyle(), msg.Content, 0)...)
			for _, call := range msg.ToolCalls {
				out = append(out, m.historyCallLines(call, results[call.Id])...)
				used[call.Id] = true
			}
		case domain.RoleTool:
			// 已经贴在调用下面的结果不再打第二遍
			if used[msg.ToolCallId] {
				continue
			}
			// 对不上调用的结果（比如会话是别的版本写的）也别丢，单独打一份
			if !m.quiet {
				out = append(out, m.block("", toolResStyle, msg.Content, printToolResultLines)...)
			}
		}
	}
	return out
}

// historyCallLines 渲染历史里的一次工具调用：圆圈 + 标题行 + 细节行，结果紧跟在后面。
// 没有结果说明那一轮当时被打断了，和 finish 里的处理一样标成未完成。
func (m model) historyCallLines(call domain.ToolCall, res domain.SessionMessage) []string {
	lines := m.toolCallLines(call)
	if res.Role == "" {
		out := withMark(lines, doneMark(domain.ToolStatusFailed))
		out[0] += hintStyle.Render("  · 未完成")
		return out
	}
	out := withMark(lines, doneMark(historyStatus(res.Content)))
	if !m.quiet {
		out = append(out, m.block("", toolResStyle, res.Content, printToolResultLines)...)
	}
	return out
}

// historyStatus 按工具结果的内容反推当时的状态：core 失败时一定以这两句开头，
// 成功的结果不会带这个前缀（见 core.executeTool）；命令自己退出码非零不算失败，
// 那种情况结果是 exit_code，本来也标成成功。
func historyStatus(content string) domain.ToolStatus {
	if strings.HasPrefix(content, "错误: ") || strings.HasPrefix(content, "执行失败: ") {
		return domain.ToolStatusFailed
	}
	return domain.ToolStatusSuccess
}

// startRun 把提问丢给 core.Loop，用 goroutine + channel 把过程事件送回 UI。
// display 是历史区回显的那段（通常就是用户的原话），prompt 是真正发给模型的——
// 手动调用技能时两者不一样：回显一行 "/add-disease 加个病种"，
// 发出去的却是展开后的整份说明。
// images 是随这条提问一起发出去的图片，会和文字一起记进会话
func (m model) startRun(display, prompt string, images []domain.Image) (tea.Model, tea.Cmd) {
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
	// 列表是"让你挑一个"的东西，已经开始跑了就没人再答它，收起来
	m.picker = nil
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
	echo := []string{promptStyle.Render(m.fitWidth(display))}
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
		out = append(out, m.reasoningBlock()...)
		m.answer.WriteString(ev.Text)
		m.status = "回答中…"

	// 连模型失败、正在重连：把原因和重试次数打进历史区，
	// 否则界面只会停在"连接模型…"，分不清是慢还是断了
	case domain.EventRetry:
		m.status = "连接失败，正在重试…"
		out = append(out, errStyle.Render("⚠ "+ev.Text))

	// 上下文压缩：压完会继续正常回答，这里只留一行说明，别打断用户的阅读
	case domain.EventCompact:
		out = append(out, hintStyle.Render("· "+ev.Text))

	// 会话起好标题了：说一声，往后 /resume 列表里认的就是这个名字。
	// 窗口标题也跟着换成它，缩到后台时靠窗口标题就能认出这个窗口在聊什么
	case domain.EventTitle:
		setWindowTitle(ev.Text)
		out = append(out, hintStyle.Render("· 会话标题 "+ev.Text))

	case domain.EventAnswerEnd:
		out = append(out, m.reasoningBlock()...)
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
		m.pending = append(m.pending, pendingCall{id: ev.Call.Id, lines: m.toolCallLines(ev.Call)})

	case domain.EventToolResult:
		out = append(out, m.toolResultLines(ev, m.takePending(ev.Call))...)
		// 精简显示下工具输出的内容不打进历史区，只留上面那行标题和状态
		if !m.quiet {
			out = append(out, m.block("", toolResStyle, ev.Text, printToolResultLines)...)
		}
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
	out = append(out, m.reasoningBlock()...)
	out = append(out, m.block("", answerStyle(), m.answer.String(), 0)...)

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
	// 这一轮正常跑完、队里还排着东西：直接接上，排队就是为了不用再敲一遍。
	// 中断或出错时不动队列——那时候该由人决定还要不要发
	if err == nil && len(m.queue) > 0 {
		next, cmd := m.dequeue()
		if cmd != nil {
			cmds = append(cmds, cmd)
		}
		if mm, ok := next.(model); ok {
			return mm, tea.Sequence(cmds...)
		}
	}
	cmds = append(cmds, textarea.Blink)
	return m, tea.Sequence(cmds...)
}

// usageLine 是输入框下面常驻的上下文占用（紧贴着输入框底边）：总数来自上一轮响应的 usage，
// 系统提示词/工具定义/对话消息三项是按本地估算的比例拆出来的，
// 所以显示成百分比，不假装是精确到个位的分项用量。
// 配了上下文窗口时还会带上"已用 x%"，让人一眼看出还剩多少余量。
// 这一行恒定只占一行：终端窄就整项丢掉尾巴，绝不折行——折出来的第二行会让界面
// 高度变来变去，inline 模式下屏幕就会错位、留下上一帧的残影。
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
	return fitItems(items, m.width-4)
}

// fitItems 用 " · " 把各项接成一行，宽度不够就从尾部整项丢掉。
// 界面这几行的高度必须恒定，所以宁可少显示一项也不折行——折出来的第二行
// 会让整个界面跟着错位。limit <= 0 表示不限宽。
func fitItems(items []string, limit int) string {
	if len(items) == 0 {
		return ""
	}
	sep := hintStyle.Render(" · ")
	out := items[0]
	for _, it := range items[1:] {
		next := out + sep + it
		if limit > 0 && lipgloss.Width(next) > limit {
			break
		}
		out = next
	}
	return out
}

// todoLines 是待办清单在这块槽位里占的那几行：标题一行，下面逐条列出来。
// 一条待办都没有时一行不占，整块位置让给别的内容。
//
// 行数由 todoBudget(rows) 决定——它必须留在 activeRows() 这块固定额度里，
// 不能自己撑高帧：帧高一变，inline 渲染的插行和重绘就按旧账算，屏幕上会叠出残影。
func (m model) todoLines(rows int) []string {
	return todoLines(tool.CurrentTodos(), m.width-4, todoBudget(rows))
}

// todoBudget 是清单最多列几条（不含标题行）。整块槽位就 rows 行，得给清单之外的
// 内容留出 todoMinRest 行：运行中的状态行、空闲时的补全候选都比清单更急
func todoBudget(rows int) int {
	return min(maxTodoRows, max(rows-todoMinRest, 0))
}

// todoLines 把待办清单渲染成"标题 + 最多 rows 条"，纯函数，方便直接拿造的数据测。
//
// 任务一多就滚动：窗口以手上正在做的那一项开头往后取 rows 条，后面不够就往前补，
// 所以进行中的那条永远在窗口里（15 条做到第 6 条，列的就是 6-10）。标题行末尾
// 交代窗口落在第几条到第几条，省得以为清单就这几项。
//
// 每一行都不超过 limit 宽：折行会把界面高度顶乱，宁可截断。
func todoLines(items []tool.TodoItem, limit, rows int) []string {
	if len(items) == 0 {
		return nil
	}
	done, running := tool.TodoCounts(items)
	title := hintStyle.Render("待办 ") + statusStyle.Render(fmt.Sprintf("%d/%d", done, len(items)))
	if running > 0 {
		title += hintStyle.Render(fmt.Sprintf(" · %d 项进行中", running))
	}
	if rows <= 0 {
		// 一行都匀不出来（终端太矮）：只留标题，进度总量还看得见
		return []string{clipLine(title, limit)}
	}

	start, end := todoWindow(items, rows)
	if start > 0 || end < len(items) {
		title += hintStyle.Render(fmt.Sprintf(" · 第 %d-%d 项", start+1, end))
	}
	out := []string{clipLine(title, limit)}

	for i := start; i < end; i++ {
		it := items[i]
		mark, style := "[ ]", hintStyle
		switch it.Status {
		case tool.TodoCompleted:
			mark, style = "[x]", toolOKStyle
		case tool.TodoInProgress:
			mark, style = "[~]", promptStyle
		}
		// 手上这项前面点一下，扫一眼就知道做到哪了
		head := "  "
		if it.Status == tool.TodoInProgress {
			head = "▸ "
		}
		out = append(out, clipLine(style.Render(fmt.Sprintf("%s%s %d. %s",
			head, mark, i+1, it.Content)), limit))
	}
	return out
}

// todoWindow 是滚动窗口的起止下标：以手上正在做的那一项为第一行往后取 rows 条，
// 后面不够就往前补（做到倒数第 2 条时列的是最后 rows 条）。
// 手上那一项：有进行中的就是它；没有就是第一条还没做的；全做完了停在最后。
func todoWindow(items []tool.TodoItem, rows int) (start, end int) {
	if rows <= 0 || len(items) <= rows {
		return 0, min(rows, len(items))
	}
	cur := -1
	for i, it := range items {
		if it.Status == tool.TodoInProgress {
			cur = i
			break
		}
	}
	if cur < 0 {
		for i, it := range items {
			if it.Status != tool.TodoCompleted {
				cur = i
				break
			}
		}
	}
	if cur < 0 {
		cur = len(items) - 1
	}
	// 窗口贴着末尾时往前补，保证凑满 rows 条
	cur = min(cur, len(items)-rows)
	return cur, cur + rows
}

// clipLine 把一行截到 limit 宽以内，limit <= 0 表示不限
func clipLine(line string, limit int) string {
	if limit <= 0 || lipgloss.Width(line) <= limit {
		return line
	}
	return ansi.Truncate(line, limit, "…")
}

// tasksLine 是输入框上方常驻的后台任务栏，
// 有任务就显示，没有就整行空着（行数恒定，见 View）。运行中的任务带上已跑时长，
// 一眼看出卡没卡住。
func (m model) tasksLine() string {
	return tasksLine(tool.TaskSnapshots(), m.width-4)
}

// tasksLine 把任务快照渲染成一行，纯函数，方便直接拿造的数据测。
// limit > 0 时保证不超过这么宽：列不下就少列几个任务，折行会把界面高度顶乱。
func tasksLine(snapshots []tool.TaskSnapshot, limit int) string {
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

	// 运行中的排前面，任务多的时候被折叠掉的总是已经结束的那些
	ordered := orderTasks(snapshots)
	if len(ordered) > maxTaskChips {
		ordered = ordered[:maxTaskChips]
	}
	chips := make([]string, 0, len(ordered))
	for _, s := range ordered {
		// 运行中的用状态色、结束的用暗色，扫一眼就知道哪个还在跑
		style := hintStyle
		if s.Running {
			style = toolStyle
		}
		chip := s.ID + " " + s.Status
		if s.Running {
			chip += " " + tool.FormatDuration(s.Elapsed)
		}
		chips = append(chips, style.Render(chip))
	}

	sep := hintStyle.Render(" · ")
	out := head
	shown := 0
	for _, chip := range chips {
		next := out + sep + chip
		// 给"还有 N 个"留出位置，免得到最后才发现交代不下
		tail := ""
		if rest := len(snapshots) - shown - 1; rest > 0 {
			tail = sep + hintStyle.Render(fmt.Sprintf("…还有 %d 个", rest))
		}
		if limit > 0 && lipgloss.Width(next+tail) > limit {
			break
		}
		out, shown = next, shown+1
	}
	if rest := len(snapshots) - shown; rest > 0 {
		tail := hintStyle.Render(fmt.Sprintf("…还有 %d 个", rest))
		if limit <= 0 || lipgloss.Width(out+sep+tail) <= limit {
			out += sep + tail
		}
	}
	return out
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
// 任务一多就分不清哪个是哪个。运行中的标明已跑了多久，结束的标明总耗时。
// 放不下的先折叠，再交给 activeView 裁。
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
		if s.Elapsed > 0 {
			label := "耗时 "
			if s.Running {
				label = "已跑 "
			}
			line += hintStyle.Render(" " + label + tool.FormatDuration(s.Elapsed))
		}
		if cmd := firstLines(s.Command, 48); cmd != "" {
			line += hintStyle.Render("  $ " + cmd)
		}
		out = append(out, line)
	}
	// 槽位只放得下这么多行，多出来的折成一句，别让人以为任务就这几个
	if rows := m.activeRows(); len(out) > rows {
		kept := append([]string{}, out[:rows-1]...)
		kept = append(kept, hintStyle.Render(fmt.Sprintf("…还有 %d 个任务", len(out)-(rows-1))))
		out = kept
	}
	return out
}

// footerLine 是输入框最下面那行按键提示，恒定占一行：终端窄就少显示几项，
// 折行会把整个界面的高度顶乱。
func (m model) footerLine() string {
	if m.state == stateRunning {
		return fitItems([]string{
			hintStyle.Render("Enter 排队下一条"),
			hintStyle.Render("Ctrl+C 中断"),
			hintStyle.Render("思考 ") + statusStyle.Render(m.effortLabel()),
		}, m.width-1)
	}
	return fitItems([]string{
		hintStyle.Render("Enter 发送"),
		hintStyle.Render(newlineHint()),
		hintStyle.Render("Ctrl+C 退出"),
		hintStyle.Render("模型 ") + statusStyle.Render(m.modelLabel()),
		hintStyle.Render("思考 ") + statusStyle.Render(m.effortLabel()),
		hintStyle.Render("模式 ") + statusStyle.Render(m.mode.Label()),
	}, m.width-1)
}

func (m model) View() tea.View {
	if m.quitting {
		return tea.NewView("")
	}
	v := tea.NewView(compose(m.frameLines(), m.width) + printMark(*m.printSeq))
	// 告诉终端真光标该待在哪儿。输入法打字的候选框是跟着终端光标走的，不报位置的话
	// 光标就停在渲染器最后一次落笔的地方——界面右下角那行按键提示后面，候选框跟着飞过去
	v.Cursor = m.cursorTarget()
	return v
}

// frameLines 按当前状态把这一帧的行拼出来。
// 三种状态（配置表单、确认框、主界面）都从这一个口子出，最后统一交给 compose 收口，
// 这样"逐行裁宽"这条不变量不必在每个状态里各写一遍、也不会漏掉哪个分支。
func (m model) frameLines() []string {
	switch m.state {
	case stateConfig:
		return m.configFrameLines()
	case stateApproval:
		return m.approvalFrameLines()
	}
	return m.mainFrameLines()
}

// configFrameLines 是 /config 表单：表单本体 + 一行按键提示
func (m model) configFrameLines() []string {
	lines := m.fieldLines()
	return append(lines, hintStyle.Render(m.configHint()))
}

// configHint 是表单底部那行按键提示：候选列表展开时键位不一样，得跟着说
func (m model) configHint() string {
	f := &m.form
	switch {
	case f.picking():
		return "↑↓ 选模型 · Enter 采用 · Esc 收起列表"
	case f.fetching:
		return "正在拉取模型列表…"
	}
	return "↑↓ 选字段 · Enter 编辑 · Ctrl+L 拉取模型列表 · Ctrl+S 保存 · Esc 返回"
}

// approvalFrameLines 是确认框：它顶替输入框，这一刻只能回答 y / a / n。
// 清单照旧从槽位额度里分，不额外撑高这一帧
func (m model) approvalFrameLines() []string {
	var lines []string
	for _, line := range m.todoLines(m.activeRows()) {
		lines = append(lines, m.wrap(line))
	}
	if line := m.tasksLine(); line != "" {
		lines = append(lines, m.wrap(line))
	}
	lines = append(lines, m.approvalView())
	// 上下文占用照旧挂在下面，确认框顶掉输入框时也不让它跳到别处
	if line := m.usageLine(); line != "" {
		lines = append(lines, m.wrap(line))
	}
	return append(lines,
		hintStyle.Render("↑↓ 或 j k 选择 · Enter 确认 · 直接打字可以拒绝并说明"))
}

// mainFrameLines 是平常那一屏。
//
// 输入框上方那块（见 activeView）有几行内容就占几行，没有就不占——界面紧贴着历史区，
// 不为对齐留空白。帧高因此会随内容变，这是允许的：帧高变化由渲染器自己兜住
// （变矮时从新帧末行往下擦、变高时整帧重绘，都不碰帧上方的历史区），真正要守的
// 是"一次往历史区插的行数不超过帧顶上方空着的行数"，那条由 printRows 管。
func (m model) mainFrameLines() []string {
	lines := append(m.mainPrefixLines(), m.inputBox())
	// 还没请求过模型时没有上下文占用，这行就不占位置（空字符串会变成一行空白）
	for _, line := range []string{m.usageLine(), m.footerLine()} {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// mainPrefixLines 是输入框上面的那几块，从上到下：槽位（清单+进度）、任务栏、附图。
// 每块没内容就不占行——空字符串塞进行长列表里会变成一行空白。
func (m model) mainPrefixLines() []string {
	var lines []string
	for _, line := range []string{m.activeView(), m.tasksLine(), m.attachmentsLine()} {
		if line != "" {
			lines = append(lines, line)
		}
	}
	return lines
}

// inputBox 是贴在最下面的输入框：一圈圆角框套着三行 textarea
func (m model) inputBox() string {
	return boxStyle.Width(m.width - 4).Render(m.input.View())
}

// cursorTarget 是这一帧里真光标该落在哪儿（坐标从帧左上角算起），nil 表示藏起来。
//
// 打字的地方都得给：输入法的候选框跟的是终端光标，不是我们在屏幕上画的那个方块。
// 不报位置时终端光标留在渲染器最后落笔的地方（界面最下面那行提示的末尾），
// 打拼音时候选框就飞到输入框外面去了。
func (m model) cursorTarget() *tea.Cursor {
	if m.cursorHold {
		// 刚改过尺寸，渲染器那本账还没对回来，这会儿报位置只会把光标摆错地方
		return nil
	}
	switch m.state {
	case stateIdle, stateRunning:
		return m.inputCursor()
	case stateConfig:
		return m.formCursor()
	}
	// 确认框里那句话的光标是手画的竖线，还没接管真光标
	return nil
}

// inputCursor 是输入框里那个光标的位置：textarea 给的是相对自己的坐标，
// 加上外面那圈圆角框的偏移（左边 1 格边框 + 1 格内边距，上面 1 行上边框）
func (m model) inputCursor() *tea.Cursor {
	c := m.input.Cursor()
	if c == nil {
		return nil
	}
	return m.placedCursor(c, c.X+boxOverhead/2, rowsOf(m.mainPrefixLines())+1+c.Y)
}

// placedCursor 照着组件给的光标做一份，只把位置换掉
func (m model) placedCursor(from *tea.Cursor, x, y int) *tea.Cursor {
	out := *from // 形状、闪不闪跟着组件走
	// 颜色留空：让终端用它自己的光标色。终端配色深浅不一，写死一个颜色反而可能看不见
	out.Color = nil
	out.X, out.Y = m.clampCursor(x, y)
	return &out
}

// clampCursor 把光标夹在帧里面：帧外的地方不存在，报出去终端会跑到屏幕别处去
func (m model) clampCursor(x, y int) (int, int) {
	if rows := m.frameRows(); rows > 0 {
		y = min(max(y, 0), rows-1)
	} else {
		y = max(y, 0)
	}
	if m.width > 1 {
		x = min(max(x, 0), m.width-1)
	} else {
		x = max(x, 0)
	}
	return x, y
}

// rowsOf 数一串渲染好的行在屏幕上到底占几行。不能数元素个数：有的元素本身就带换行
// （输入框上方那块、折过行的预览），按个数数会数少好几行
func rowsOf(lines []string) int {
	n := 0
	for _, line := range lines {
		n += strings.Count(line, "\n") + 1
	}
	return n
}

// printMarkStates 是帧标记最多能换多少种样子，够一连串打印在同一个重绘间隔里用
const printMarkStates = 24

// printMark 是贴在帧末尾的一小串零宽转义，每打一批历史就换一个样子（内容上多一个
// 0;）。它一个像素都不上屏，唯一的作用是让帧内容"变了"：
//
// 渲染器对内容没变的帧是直接跳过的，跳过时连光标都不会去摆。而往历史区插行的时候
// 它又把自认为的光标位置写成了帧的左上角（cursedRenderer.insertAbove 里那句
// SetPosition(0,0)），所以刚打完历史行又不重画的话，真光标就留在帧顶上——
// 输入法打字的候选框是跟着真光标走的，于是跑到界面左上角去。
//
// 用 SGR 复位（0;0;…m）当填充是因为它不产生任何字符：终端只当一次"样式归零"，
// 屏幕上什么都不变。
func printMark(seq int) string {
	return "\x1b[" + strings.Repeat("0;", seq%printMarkStates) + "0m"
}

// compose 把一帧的行收成最终画面，是整块界面唯一的出口。
//
// 这里只做一件事，但它是不变量：每一行都必须严格窄于终端宽度。行宽一旦超了，
// 终端会自己折行，程序记的帧高就和屏幕实际用掉的行数对不上，从那行起整个界面错位，
// 屏幕上留下上一帧的残影。
//
// 用截断而不是折行：折行会让帧高跟着内容变，而这块界面的所有区域高度都是算好的，
// 多出来一行就把输入框顶出屏幕。宁可截掉尾巴，也不让行数失控。
func compose(lines []string, width int) string {
	limit := width - 1
	if limit <= 0 {
		return strings.Join(lines, "\n")
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		line = strings.TrimRight(line, "\r")
		if lipgloss.Width(line) > limit {
			line = ansi.Truncate(line, limit, "")
		}
		out = append(out, line)
	}
	return strings.Join(out, "\n")
}

// activeLines 是输入框上方那块的全部内容，最多 activeRows() 行（实际几行看内容）：
// 待办清单排在最前面（最多 todoBudget 条），后面是跑任务时的状态/预览，
// 或者闲着时的补全候选、后台任务详情、命令提示。
func (m model) activeLines() []string {
	rows := m.activeRows()
	// 挑东西的列表要整块露出来：这一刻它是唯一在等人回答的东西，
	// 所以压过待办清单和排队那几行，自己按槽位额度排版（见 pickerLines）
	if m.picker != nil {
		return m.pickerLines()
	}
	out := m.todoLines(rows)
	// 排队那几行紧跟在清单后面：它是用户刚敲进去、马上要发的东西，
	// 排在预览之前，槽位不够时裁掉的是预览而不是它
	out = append(out, m.queueLines()...)
	if m.state == stateRunning {
		return append(out, strings.Split(m.runningView(), "\n")...)
	}
	return append(out, m.idleLines()...)
}

// activeTodoRows 是清单这一帧占了几行（含标题），没有清单就是 0。
// activeView 裁内容时要给清单留出前面这几行，不能当成可以随便砍的预览
func (m model) activeTodoRows() int {
	return len(m.todoLines(m.activeRows()))
}

// queueRows 是排队那块这一帧占了几行（含标题行），空队列就是 0。
// 和 activeTodoRows 一样，裁内容时这几行要留住
func (m model) queueRows() int {
	return len(m.queueLines())
}

// idleLines 是闲着时这块槽位放什么。没有内容就返回空——槽位不占行（见 activeView），
// 界面紧贴着历史区往下排，不为了对齐留一片空白。
// 有东西时按"最有用"的顺序挑一样：补全候选最要紧，其次是把后台任务摊开看，
// 都没有就在还没聊过的会话里摆一份提示。
func (m model) idleLines() []string {
	if lines := m.completionLines(); len(lines) > 0 {
		return lines
	}
	if lines := m.taskDetailLines(); len(lines) > 0 {
		return lines
	}
	if m.cur == nil || len(m.cur.Messages) == 0 {
		return m.hintLines()
	}
	return nil
}

// hintLines 是槽位实在没别的东西可放时的提示：有哪些命令、键盘怎么用。
// 工具清单不在这儿占地方，真要查就看 /help。
// 开场那会儿槽位本来是空的，宁可摆这几行，也别留一大片空白。
func (m model) hintLines() []string {
	raw := []string{
		"命令  /help 全部说明 · /config 配这套模型 · /model 换模型",
		"      /mode 切换模式 · /resume 恢复会话 · /todo 看清单 · /new 新会话",
	}
	// 一个技能都没装时，这一行只会让人以为有东西没显示出来
	if len(m.skills) > 0 {
		raw = append(raw, "技能  /skills 看装了哪些 · 直接 /技能名 就能点名用它")
	}
	raw = append(raw, "", "输入  Enter 发送 · "+newlineHint()+" · Tab 补全 · Ctrl+V 贴图")
	// 窄终端里长行要折，折出来多余的行由 activeView 裁掉，槽位高度仍然恒定
	limit := m.width - 3
	out := make([]string, 0, len(raw))
	for _, line := range raw {
		if strings.TrimSpace(line) == "" {
			out = append(out, "")
			continue
		}
		if limit > 0 {
			for _, part := range strings.Split(ansi.Wrap(line, limit, ""), "\n") {
				out = append(out, hintStyle.Render(part))
			}
			continue
		}
		out = append(out, hintStyle.Render(line))
	}
	return out
}

// activeView 把输入框上方那块内容收成几行，行数就是内容自己的行数：
// 没内容就是 0 行，界面直接贴着历史区；内容多了才裁到 activeRows() 这个上限。
// 跑任务时裁法是先砍中间（留下清单和状态/工具标题，以及最新的几行预览），
// 闲着时按顺序留着前面的内容。
//
// 这块的行数会随内容变，帧高也就跟着变。这是可以的：inline 渲染器自己处理帧高
// 变化（变矮时从新帧末行往下擦，见 ultraviolet 的 partialClear；变高时整帧重绘），
// 两条路都只动帧自己的行、不碰帧上方的历史区。真正怕的是"一次往历史区插的行数
// 超过帧顶上方空着的行数"，那个由 printRows 按当前帧高管着，不靠这里凑固定高度。
// 单行也不能比终端还宽——超了终端会自己折行，实际用的行数就比这里记的多，
// 屏幕从那一行起全错位。
func (m model) activeView() string {
	rows := m.activeRows()
	lines := m.foldLines(m.activeLines())
	if len(lines) > rows {
		if m.state == stateRunning {
			// 清单和排队那几行不能被砍：它是这一屏最要紧的信息，砍了等于没有
			keep := min(m.activeTodoRows()+1+m.queueRows(), rows)
			kept := make([]string, 0, rows)
			kept = append(kept, lines[:keep]...)
			kept = append(kept, lines[len(lines)-(rows-keep):]...)
			lines = kept
		} else {
			lines = lines[:rows]
		}
	}
	return strings.Join(lines, "\n")
}

// foldLines 把超宽的行就地折成多行，宽度未知时原样返回。
// 折出来的行数变化由 activeView 裁到固定行数兜住。
func (m model) foldLines(lines []string) []string {
	limit := m.width - 1
	if limit <= 0 {
		return lines
	}
	out := make([]string, 0, len(lines))
	for _, line := range lines {
		if lipgloss.Width(line) <= limit {
			out = append(out, line)
			continue
		}
		out = append(out, strings.Split(
			ansi.Wrap(strings.TrimRight(line, "\r"), limit, ""), "\n")...)
	}
	return out
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

	if tail := tailLines(m.reasoning.String(), previewReasoningLines); tail != "" && !m.quiet {
		b.WriteString("\n" + thinkStyle.Render(m.wrap("  "+tail)))
	}
	if tail := tailLines(m.answer.String(), previewAnswerLines); tail != "" {
		b.WriteString("\n" + m.wrap("  "+tail))
	}
	return b.String()
}

// reasoningBlock 是这一轮攒下来的思考内容，只在第一次被叫到时给出（之后 reasoningShown
// 记成已显示，免得回答开头、结尾各打一遍）。精简显示下思考整段不打，但仍然标记成
// 已显示——不然后面每来一段回答都会再判断一次。
func (m *model) reasoningBlock() []string {
	if m.reasoningShown {
		return nil
	}
	m.reasoningShown = true
	if m.quiet {
		return nil
	}
	return m.block("思考", thinkStyle, m.reasoning.String(), printReasoningLines)
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
//
// 交出去之前必须先按终端宽度折好：行宽一旦超过终端，终端会自己折行，程序记的行数
// 就和屏幕实际用的行数对不上，之后每一行都会错位，最先看出来的是贴在最下面的
// 输入框——上下边框被挤掉、界面糊成一片。
//
// 折完还要按"一次最多插几行"切开，分几次打（见 printRows）：一次插的行数超过帧顶
// 上方空着的行数，帧就被顶出屏幕，屏幕上留下半截擦不掉的旧帧——看着就是两份输入框、
// 两行上下文占用叠在一起。
//
// 最后还要跟一个 printed：插行的时候渲染器以为光标就在帧的左上角（见
// cursedRenderer.insertAbove 里的 SetPosition(0,0)），真光标就被扔在那儿了。
func (m *model) print(text string) tea.Cmd {
	lines := strings.Split(m.fitWidth(text), "\n")
	rows := m.printRows()
	if rows <= 0 || len(lines) <= rows {
		return tea.Sequence(printChunk(strings.Join(lines, "\n")), printed())
	}
	cmds := make([]tea.Cmd, 0, len(lines)/rows+2)
	for len(lines) > 0 {
		n := min(rows, len(lines))
		cmds = append(cmds, printChunk(strings.Join(lines[:n], "\n")))
		lines = lines[n:]
	}
	return tea.Sequence(append(cmds, printed())...)
}

// printChunk 把一块内容包成一次"打到历史区"。
//
// 整块只有一个空行时塞一个空格进去：渲染器对空字符串的插行是原地返回的
// （cursedRenderer.insertAbove 里 len(str)==0 就直接 return），一行都插不进去。
// 而"打一屏空行把老内容顶出去"这招正是靠插空行干活的（见 WindowSizeMsg），
// 顶不出去，改完尺寸屏幕上就会留下旧帧。这一个空格会被紧接着的擦行指令擦掉，
// 屏幕上看到的仍然是一行空白。
func printChunk(text string) tea.Cmd {
	if text == "" {
		text = " "
	}
	return tea.Println(text)
}

// printed 是"这批历史行已经打出去了"，紧跟在 Println 后面到。
//
// 它只干一件事：把帧内容换一个样子（见 printMark），逼渲染器重画一帧。渲染器对
// "内容没变"的帧是直接跳过的，跳过的同时也就不会去摆光标——刚打完历史行时不重画，
// 真光标就留在帧左上角，输入法打字的候选框跟着跑到界面顶上。
func printed() tea.Cmd {
	return func() tea.Msg { return printedMsg{} }
}

// printRows 是一次 tea.Println 最多能带几行；<= 0 表示不限（还不知道终端多高）。
//
// 这个上限不是"留白好看"，是渲染器插行算法的硬约束：插行时它先按帧顶在屏幕上的
// 位置往上数 offset 行去插，offset 一旦超过帧顶那一行，光标就从屏幕顶上顶出去了，
// 插行的落点全错，旧帧的下半截留在屏幕上再也不会被擦（两份输入框、两行上下文占用）。
//
// 帧顶上方能空出几行 = 终端高 - 帧高 - 1 行余量。帧高取屏幕上那一帧和即将画上去
// 的这一帧里高的那个：待办清单刚出现时新帧更高，清单刚收起时屏幕上那一帧更高，
// 只有两个都算上，插行的落点才总是落在屏幕里面。
func (m model) printRows() int {
	if m.height <= 0 {
		return 0
	}
	rows := max(m.frameRows(), m.screenRows)
	room := m.height - rows - 1
	if room < 1 {
		return 1
	}
	return room
}

// frameRows 是这一帧在屏幕上实际占几行。不能数 frameLines 的元素个数——输入框上方
// 那块本身就是一个多行字符串，数元素会把帧高数少好几行，插行就又算错。
func (m model) frameRows() int {
	return len(strings.Split(compose(m.frameLines(), m.width), "\n"))
}

// rebuild 把屏幕清掉重画，让界面回到干净状态。
//
// 为什么用它来兜底：inline 模式用的是相对光标，程序认为自己在第几行全靠自己累计
// 的那本账。用户滚过滚轮、拖过窗口边框，或者终端自己把内容重排了，那本账就作废，
// 而且没有绝对定位可以退回去（见 ultraviolet 里讲相对光标无法恢复的注释）。清屏是
// 唯一能把这个账本清零的手段：它会让渲染器丢掉上一帧的记忆、强制整屏重绘。
//
// 试过但走不通的路，记在这儿免得以后再踩：
//   - 清屏之后再把历史重打一遍：渲染器清屏时把光标记在左上角，接着画帧就从顶上
//     往下画，会把刚打出来的历史盖掉。
//   - 打一屏空行把旧内容顶出去：帧的内容没变，渲染器判定"无需重绘"直接跳过，
//     结果帧跟着旧内容一起被顶出屏幕，屏幕上只剩空行。
func (m *model) rebuild() tea.Cmd {
	if m.width <= 0 || m.height <= 0 {
		return nil
	}
	return tea.ClearScreen
}

// fitWidth 把每一行折到终端宽度以内。留一列余量，避免正好写满最后一列，
// 有的终端写满后会把光标挂在行尾，下一个字符就多折出一行。
// 空行原样保留，行数不变——打出去几行就是几行，终端那边的账才对得上。
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

// currentModel 是当前生效的那套模型配置。配置坏了（比如 Current 指向不存在的模型）时，
// 返回零值，让界面能开、表单能打开，把问题留到真正发请求的时候再报
func (m model) currentModel() config.ModelConfig {
	mc, err := m.cfg.CurrentModel()
	if err != nil {
		return config.ModelConfig{}
	}
	return mc
}

// modelLabel 是当前模型的显示名，一套都没配时给一句提示，别让界面空着
func (m model) modelLabel() string {
	if label := m.currentModel().Label(); label != "" {
		return label
	}
	return "未配置"
}

// effortLabel 是当前生效的思考强度档位，没配（不发这个参数）时显示"默认"。
// 界面上和模型名摆在一起：同一套模型换个档位，跑出来的东西差别很大，得看得见
func (m model) effortLabel() string {
	return config.EffortLabel(m.currentModel().ReasoningEffort)
}

// deepSeekConfig 从配置里解出当前这套模型。解不出来时返回零值，
// 让表单能打开、从头填，而不是直接报错退出界面。
func deepSeekConfig(cfg config.GlobalConfig) config.ModelConfig {
	mc, err := cfg.CurrentModel()
	if err != nil {
		return config.ModelConfig{}
	}
	return mc
}

// buildProvider 按当前选中的那套模型配置造 provider
func buildProvider(cfg config.GlobalConfig) (domain.Provider, error) {
	mc, err := cfg.CurrentModel()
	if err != nil {
		return nil, err
	}
	return provider.New(mc)
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
