package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"sync"

	"hamusuta/agent-dev/src/config"
	"hamusuta/agent-dev/src/core"
	"hamusuta/agent-dev/src/domain"
	"hamusuta/agent-dev/src/provider"
	"hamusuta/agent-dev/src/session"
	"hamusuta/agent-dev/src/tool"
)

// 一轮跑不了的原因。前端按码说人话，不要靠解析错误文本
var (
	// ErrBusy 表示这一轮还没跑完，不能再起一轮
	ErrBusy = errors.New("上一轮还在跑")
	// ErrNoModel 表示配置里没有可用的模型
	ErrNoModel = errors.New("还没配模型")
)

// newProvider 按一套配置造 provider。抽成变量是为了测试能塞一个假的进来：
// 真跑一轮要么打接口、要么得有一整套工具环境，单元测试两样都不该依赖
var newProvider = provider.New

// Session 是一个正在用的会话：会话数据、正在跑的那一轮、以及连着的浏览器。
//
// 和 TUI 一样，同一时刻只跑一轮。多个浏览器标签页连同一个会话时，
// 事件广播给全部订阅者，界面各自跟上——谁发起的并不重要。
type Session struct {
	store *session.Store

	mu   sync.Mutex
	data *domain.Session
	// mode 是工作模式，跟着配置走；切模式会写回配置文件
	mode string

	// subs 是连着这条会话的浏览器。key 是订阅号，删除时按它摘掉
	subs    map[int]chan []byte
	nextSub int

	// seq 是事件序号，每条会话各算各的
	seq int

	// running / cancel 是正在跑的那一轮。cancel 用来中断
	running bool
	cancel  context.CancelFunc

	// approvals 是正等着用户点的确认，key 是推给前端的那个 id。
	// core 那边阻塞在 Reply 上，这里收到回答再写进去
	approvals    map[string]chan domain.ApprovalAnswer
	nextApproval int

	// outcome 是最近一次跑完的结果：outcomeDone / outcomeFailed / outcomeCancelled，
	// 空串表示还没跑过。列表上那个圆点的颜色就靠它（见 Status）
	outcome string
	// todos 是这条会话最近一次 TodoWrite 传上来的整份清单。
	// core 那边记的是进程级的一份，多个会话同时跑会串；这里按会话留一份自己的
	todos []tool.TodoItem
	// taskIds 是这条会话起的后台任务 id（按出现顺序）。
	// 后台任务本身是进程级的，界面上只显示属于这条会话的那几个
	taskIds []string
	// usage 是这条会话最近一次请求模型的上下文占用。它不是事件流的副产品而是存下来：
	// usage 事件只在跑的时候推一次，页面一刷新就再也收不到了，输入框上方那条
	// "上下文 303k / 1024k" 就整块消失（实测刷新后一片空白，看着像功能没了）。
	// 存一份，快照里带出去，刷新后照样看得见上次的占用量
	usage *usage
}

// 会话在列表上的四种状态。前三样对应界面上那个圆点：转圈 / 黄 / 绿
const (
	// statusRunning 是模型或工具正在跑
	statusRunning = "running"
	// statusWaiting 是等你介入：有确认要答，或者上一轮出错、被中断
	statusWaiting = "waiting"
	// statusDone 是最近一轮正常跑完了
	statusDone = "done"
	// statusIdle 是还没聊过
	statusIdle = "idle"
)

// 一轮跑完的三种结果
const (
	outcomeDone      = "done"
	outcomeFailed    = "failed"
	outcomeCancelled = "cancelled"
)

// Status 是这条会话这会儿在列表上该显示成什么样。
// 优先级：在跑 > 等你介入（有确认挂着）> 上一轮的结果
func (s *Session) Status() string {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.statusLocked()
}

// statusLocked 是 Status 的本体，调用方已经拿着锁（snapshot 要它，免得锁两遍）
func (s *Session) statusLocked() string {
	switch {
	case s.running:
		return statusRunning
	case len(s.approvals) > 0:
		return statusWaiting
	case s.outcome == outcomeDone:
		return statusDone
	case s.outcome == "":
		return statusIdle
	default:
		// 出错或被中断：都等着人决定下一步，一起算"等待提问"
		return statusWaiting
	}
}

// Todos 是这条会话的待办清单快照，节点界面直接照着画
func (s *Session) Todos() []tool.TodoItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.todos) == 0 {
		return nil
	}
	out := make([]tool.TodoItem, len(s.todos))
	copy(out, s.todos)
	return out
}

// TaskIds 是这条会话起的后台任务 id。后台任务注册表是进程级的，
// 界面拿这些 id 去过滤，只显示属于这条会话的
func (s *Session) TaskIds() []string {
	s.mu.Lock()
	defer s.mu.Unlock()
	if len(s.taskIds) == 0 {
		return nil
	}
	out := make([]string, len(s.taskIds))
	copy(out, s.taskIds)
	return out
}

// newSession 把一个已经读出来的会话包成运行时。不落盘、不加载，
// 由 Manager 决定是新建还是从存储里读
func newSession(store *session.Store, data *domain.Session, mode string) *Session {
	return &Session{
		store:     store,
		data:      data,
		mode:      mode,
		subs:      make(map[int]chan []byte),
		approvals: make(map[string]chan domain.ApprovalAnswer),
	}
}

// Data 返回会话数据本身。调用方拿到的是指针，别在锁外改它
func (s *Session) Data() *domain.Session {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.data
}

// Running 说明这一轮在不在跑
func (s *Session) Running() bool {
	s.mu.Lock()
	defer s.mu.Unlock()
	return s.running
}

// Mode 是当前工作模式
func (s *Session) Mode() domain.Mode {
	s.mu.Lock()
	defer s.mu.Unlock()
	return parseMode(s.mode)
}

// parseMode 把配置里的字符串认成模式，认不出按默认的"编辑需授权"
func parseMode(s string) domain.Mode {
	mode, err := domain.ParseMode(s)
	if err != nil {
		return domain.ModeAsk
	}
	return mode
}

// Subscribe 挂一个订阅者，返回收事件的 channel 和摘除它的函数。
// 缓冲给足一点：工具输出一条可能几十 KB，前端渲染慢一拍不该把这一轮卡住
func (s *Session) Subscribe() (<-chan []byte, func()) {
	ch := make(chan []byte, 256)
	s.mu.Lock()
	id := s.nextSub
	s.nextSub++
	s.subs[id] = ch
	s.mu.Unlock()

	return ch, func() {
		s.mu.Lock()
		if c, ok := s.subs[id]; ok {
			delete(s.subs, id)
			close(c)
		}
		s.mu.Unlock()
	}
}

// broadcast 把一条事件推给所有订阅者。跟不上（channel 满了）的那个直接丢这一条：
// SSE 断了前端会重连，重新拉一次历史就补上了，卡住整轮才是真麻烦。
//
// 发送在锁内做：摘订阅者时要 close channel，发送和关闭必须互斥，否则会撞上
// "send on closed channel"。channel 带缓冲、这里又是非阻塞发送，持锁只是几次 select，不碍事
func (s *Session) broadcast(e *event) {
	if e == nil {
		return
	}
	data, err := json.Marshal(e)
	if err != nil {
		return
	}
	s.mu.Lock()
	defer s.mu.Unlock()
	for _, ch := range s.subs {
		select {
		case ch <- data:
		default:
		}
	}
}

// emit 是交给 core 的事件出口。它可能被多个 goroutine 同时调用
// （一批只读工具是并发跑的，执行前的确认就在各自的 goroutine 里发），所以整段加锁。
//
// 确认事件只登记、不等待：core 自己会阻塞在 Reply 上等人点，
// 这里要是也阻塞，就没人能去处理那个 POST 了
func (s *Session) emit(ev domain.Event) {
	s.mu.Lock()
	s.seq++
	seq := s.seq
	e := eventFromDomain(seq, ev)
	if ev.Kind == domain.EventApproval && e != nil && e.Approval != nil {
		id := fmt.Sprintf("ap-%d", s.nextApproval)
		s.nextApproval++
		e.Approval.ID = id
		if ev.Reply != nil {
			s.approvals[id] = ev.Reply
		}
	}
	// 上下文占用：这个事件一轮只推一次，页面刷过之后就再也收不到了
	// （EventSource 不会补发历史事件），所以照着留一份，快照里带出去。
	// 内存那份只活到这个进程结束——重启之后页面上那条用量会整块消失，
	// 看着像功能坏了。所以同时记进会话，跟着每步落盘一起存下来
	if ev.Kind == domain.EventUsage && e != nil && e.Usage != nil {
		u := *e.Usage
		s.usage = &u
		if s.data != nil {
			cu := ev.Usage
			s.data.LastUsage = &cu
		}
	}
	// 待办清单：TodoWrite 每次传上来的都是整份，照着替换就行。
	// core 那边记的是进程级的一份，多开几个会话会串，所以这里按会话留一份
	if ev.Kind == domain.EventToolCall && ev.Call.Name == "TodoWrite" {
		// 参数写坏了（模型偶尔只传个 intent）就保留原来那份：
		// 静默清空会让界面上的进度凭空消失，像是活白干了
		if items, ok := tool.ParseTodoArgs(ev.Call.Arguments); ok {
			s.todos = items
		}
	}
	// 后台任务归属：Bash 用 background=true 起的任务，结果里带着 task_id=task_N
	if ev.Kind == domain.EventToolResult {
		for _, id := range taskIdsIn(ev.Text) {
			if !containsString(s.taskIds, id) {
				s.taskIds = append(s.taskIds, id)
			}
		}
	}
	// 跑一步就落一步盘。界面上刷新页面、切走再切回来，读的都是磁盘那份
	// （见 snapshot：内存里这份 core 正在追加，读它会撞上并发写），
	// 所以只有落到盘上才看得见。挑这三个事件是因为它们都发生在"某段内容刚刚记进会话"之后
	// （core 里 Append 排在 emit 前面）：
	//   EventStep       —— 新一步的开头，上一步的 assistant 和工具结果都齐了
	//   EventAnswerEnd  —— 这一轮模型回答刚记进会话
	//   EventToolResult —— 一次工具结果刚记进会话
	// 流式的 EventContent / EventReasoning 一个 token 一条，绝不能落盘
	//
	// 写文件放在锁外，别把订阅者挡在后面
	save := shouldSave(ev.Kind)
	s.mu.Unlock()

	if save {
		// 这里就在 core 的 goroutine 里，data 此刻不会被别处写，序列化是安全的
		_ = s.store.Save(s.data)
	}
	s.broadcast(e)
}

// notifyMeta 把顶栏那几项全局开关的现状推给连着的浏览器。
// 工作模式是全局的：一个标签页切了，别的标签页、别的会话里开着的页面也得当场变过来，
// 不然那边的下拉框还停在旧值上，看着就像"切了没生效"
func (s *Session) notifyMeta(m meta) {
	s.mu.Lock()
	s.seq++
	seq := s.seq
	s.mu.Unlock()
	s.broadcast(&event{Seq: seq, Kind: kindMeta, Meta: &m})
}

// shouldSave 判断这个事件要不要顺手把会话落盘。
// 只在这个回合"刚有一条消息进会话"的边界上存，不按 token 存
func shouldSave(kind domain.EventKind) bool {
	switch kind {
	case domain.EventStep, domain.EventAnswerEnd, domain.EventToolResult:
		return true
	default:
		return false
	}
}

// taskIdRe 从工具结果里认出后台任务的 id：Bash 用 background=true 启动时
// 结果固定带着 "task_id=task_N"
var taskIdRe = regexp.MustCompile(`task_id=(task_\d+)`)

// taskIdsIn 挑出结果文本里提到的后台任务 id
func taskIdsIn(text string) []string {
	matches := taskIdRe.FindAllStringSubmatch(text, -1)
	if len(matches) == 0 {
		return nil
	}
	out := make([]string, 0, len(matches))
	for _, m := range matches {
		out = append(out, m[1])
	}
	return out
}

// containsString 是给上面那个小列表用的
func containsString(list []string, want string) bool {
	for _, item := range list {
		if item == want {
			return true
		}
	}
	return false
}

// Answer 回答一次确认。id 对不上（已经答过、或者这一轮被中断清空了）时返回 false，
// 前端据此提示"这项已经不需要确认了"
func (s *Session) Answer(id string, ans domain.ApprovalAnswer) bool {
	s.mu.Lock()
	reply, ok := s.approvals[id]
	delete(s.approvals, id)
	s.mu.Unlock()
	if !ok {
		return false
	}
	// 带缓冲的 channel，core 那边一定收得到；真没人收了也别卡住这个请求
	select {
	case reply <- ans:
		return true
	default:
		return false
	}
}

// Cancel 中断这一轮。没在跑时什么都不做
func (s *Session) Cancel() bool {
	s.mu.Lock()
	cancel := s.cancel
	running := s.running
	s.mu.Unlock()
	if !running || cancel == nil {
		return false
	}
	cancel()
	return true
}

// Compact 手动压缩（/compact）：把较早的对话整理成摘要，腾出上下文。
//
// 它要请求一次模型，所以和跑一轮一样占着 running：这会儿前端再发消息会收到 ErrBusy，
// 免得压缩改会话的同时另一轮在写同一个 Messages。压完广播 done，前端那边圆点和
// 排队都按"这一轮结束"处理。
func (s *Session) Compact(cfg config.GlobalConfig) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return ErrBusy
	}
	mc, err := cfg.CurrentModel()
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("%w: %v", ErrNoModel, err)
	}
	p, err := newProvider(mc)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.running = true
	s.cancel = cancel
	s.approvals = make(map[string]chan domain.ApprovalAnswer)
	data := s.data
	s.mu.Unlock()

	go func() {
		defer cancel()
		core.CompactNow(domain.Ctx{
			Provider:     p,
			Context:      ctx,
			WorkDir:      data.WorkDir,
			Shell:        tool.ShellName(),
			Session:      data,
			ContextLimit: mc.Limit(),
			Emit:         s.emit,
		}, data)
		// 压缩改的是摘要和压缩标记，压完就落盘；被中断也存，前面那部分改动同样有效
		saveErr := s.store.Save(data)

		s.mu.Lock()
		s.running = false
		s.cancel = nil
		s.approvals = make(map[string]chan domain.ApprovalAnswer)
		s.outcome = outcomeDone
		s.seq++
		seq := s.seq
		s.mu.Unlock()

		switch {
		case saveErr != nil:
			s.broadcast(errorEvent(seq, saveErr, false))
		default:
			s.broadcast(doneEvent(seq, ""))
		}
	}()
	return nil
}

// Start 起一轮：把提问记进会话，然后跑 core.Loop。
// 一轮还没跑完时返回 ErrBusy（前端自己把消息排起来，等 done 再发）。
func (s *Session) Start(cfg config.GlobalConfig, prompt string, images []domain.Image) error {
	s.mu.Lock()
	if s.running {
		s.mu.Unlock()
		return ErrBusy
	}
	mc, err := cfg.CurrentModel()
	if err != nil {
		s.mu.Unlock()
		return fmt.Errorf("%w: %v", ErrNoModel, err)
	}
	p, err := newProvider(mc)
	if err != nil {
		s.mu.Unlock()
		return err
	}
	ctx, cancel := context.WithCancel(context.Background())
	s.running = true
	s.cancel = cancel
	s.seq = 0
	// 新一轮开始了，上一轮的结果作废：列表上这会儿该显示"进行中"
	s.outcome = ""
	s.approvals = make(map[string]chan domain.ApprovalAnswer)
	mode := parseMode(s.mode)
	data := s.data
	s.mu.Unlock()

	s.broadcast(startedEvent(0, prompt, images))

	go func() {
		defer cancel()
		runErr := core.Loop(domain.Ctx{
			Prompt:   prompt,
			Images:   images,
			Provider: p,
			// 递全套工具，让 core 按"此刻"的模式现筛：顶栏那个下拉框在这一轮跑着的
			// 时候也能点，切到只读要当场把写类工具收回去，切到完全授权要当场放开
			Tools:        tool.All(),
			Context:      ctx,
			WorkDir:      data.WorkDir,
			Shell:        tool.ShellName(),
			Session:      data,
			ContextLimit: mc.Limit(),
			Mode:         mode,
			// 模式随时可能被切（/api/mode 会改 s.mode），core 每一步都回来问一次
			ModeLive: s.Mode,
			MaxSteps: cfg.Steps(),
			Emit:     s.emit,
		})
		// 出错也要落盘：前面几轮的工具调用不能白跑
		saveErr := s.store.Save(data)

		s.mu.Lock()
		s.running = false
		s.cancel = nil
		// 这一轮结束了，还挂着的确认没人会来点了，清掉免得越攒越多
		s.approvals = make(map[string]chan domain.ApprovalAnswer)
		// 记下这一轮的结果：列表上的圆点靠它决定是黄还是绿
		switch {
		case runErr == nil && saveErr == nil:
			s.outcome = outcomeDone
		case runErr != nil && errors.Is(runErr, context.Canceled):
			s.outcome = outcomeCancelled
		default:
			s.outcome = outcomeFailed
		}
		s.seq++
		seq := s.seq
		s.mu.Unlock()

		switch {
		case runErr == nil && saveErr != nil:
			s.broadcast(errorEvent(seq, saveErr, false))
		case runErr == nil:
			s.broadcast(doneEvent(seq, data.LastAssistant()))
		default:
			s.broadcast(errorEvent(seq, runErr, errors.Is(runErr, context.Canceled)))
		}
	}()
	return nil
}

// Snapshot 是给前端的一份会话现状：历史 + 跑没跑着 + 模式
type snapshot struct {
	Id      string `json:"id"`
	Title   string `json:"title,omitempty"`
	WorkDir string `json:"work_dir,omitempty"`
	Running bool   `json:"running"`
	// Status 是 running / waiting / done / idle，列表上的圆点按它上色
	Status    string    `json:"status"`
	Mode      string    `json:"mode"`
	Messages  []message `json:"messages"`
	UpdatedAt string    `json:"updated_at,omitempty"`
	// HasMore 说明上面还有没发过来的更早内容（被 limit 裁掉了）。
	// 不给条数：原始消息数和显示块数不是一个单位，给个数字反而会骗人
	HasMore bool `json:"has_more,omitempty"`
	// Usage 是最近一次请求模型的上下文占用。带上它，页面刷新之后输入框上方那条
	// "上下文 xxx / xxx" 还在——usage 事件只在跑的时候推一次，不补发历史事件，
	// 只靠事件流的话一刷新就整块空白
	Usage *usage `json:"usage,omitempty"`
}

// snapshot 默认给全量历史、详细内容（终端界面、测试都走这条）
func (s *Session) snapshot() snapshot {
	return s.snapshotWith(historyOptions{})
}

// snapshotWith 按 opt 裁剪历史。浏览器切会话时传 limit：一次渲染几百条的
// markdown 要 160ms，白屏卡在那儿，体感就是"切个会话怎么这么慢"
func (s *Session) snapshotWith(opt historyOptions) snapshot {
	s.mu.Lock()
	data, running, mode, store, status := s.data, s.running, s.mode, s.store, s.statusLocked()
	// 用量是一份只读快照，拷出来再往外给：外面拿着它渲染时，下一轮可能正在改
	var lastUsage *usage
	if s.usage != nil {
		u := *s.usage
		lastUsage = &u
	}
	s.mu.Unlock()

	// 内存里没有（刚重启，这条会话是从磁盘读出来的）：退回会话里存的那份。
	// 换了个进程不该让"上次请求占了多少"这条信息凭空消失
	if lastUsage == nil && data.LastUsage != nil {
		lastUsage = usageFromContext(*data.LastUsage)
	}

	// 跑着的时候不读内存里那份：core 正在往它上面追加消息，读它就是在和数据竞争
	// （TUI 那边靠"运行中只读自己攒的流式缓冲"避开了这件事）。
	//
	// 磁盘那份现在是每走一步就同步过的（见 emit 里的 shouldSave）：提问、每一步的
	// 回答、每一次工具结果，一进会话就落盘。所以运行中刷新页面、切走再切回来，
	// 能看到已经跑完的那些步，而不是只剩一句提问。
	// 只有"这一轮正在流式输出、还没结束的那半句"要靠实时事件流画——那半句本来
	// 就还没记进会话，丢了也不算丢历史
	view := data
	if running {
		if saved, err := store.Load(data.Id); err == nil {
			view = saved
		}
	}

	msgs, hasMore := historyFromSessionOpts(view, opt)
	out := snapshot{
		Id:       view.Id,
		Title:    view.Title,
		WorkDir:  view.WorkDir,
		Running:  running,
		Status:   status,
		Mode:     mode,
		Messages: msgs,
		HasMore:  hasMore,
		Usage:    lastUsage,
	}
	if !view.UpdatedAt.IsZero() {
		out.UpdatedAt = view.UpdatedAt.Format("2006-01-02 15:04:05")
	}
	return out
}
