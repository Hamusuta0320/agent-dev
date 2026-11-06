package webui

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"regexp"
	"strings"
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

	// promptSaved 记着本轮提问有没有落过盘。snapshot 在运行中读磁盘那份，
	// 得保证磁盘上已经有这条提问
	promptSaved bool

	// outcome 是最近一次跑完的结果：outcomeDone / outcomeFailed / outcomeCancelled，
	// 空串表示还没跑过。列表上那个圆点的颜色就靠它（见 Status）
	outcome string
	// todos 是这条会话最近一次 TodoWrite 传上来的整份清单。
	// core 那边记的是进程级的一份，多个会话同时跑会串；这里按会话留一份自己的
	todos []tool.TodoItem
	// taskIds 是这条会话起的后台任务 id（按出现顺序）。
	// 后台任务本身是进程级的，界面上只显示属于这条会话的那几个
	taskIds []string
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

// SetMode 换工作模式，并写回配置文件
func (s *Session) SetMode(path string, mode domain.Mode) error {
	if err := config.SaveMode(path, mode.String()); err != nil {
		return err
	}
	s.mu.Lock()
	s.mode = mode.String()
	s.mu.Unlock()
	return nil
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
	// 待办清单：TodoWrite 每次传上来的都是整份，照着替换就行。
	// core 那边记的是进程级的一份，多开几个会话会串，所以这里按会话留一份
	if ev.Kind == domain.EventToolCall && ev.Call.Name == "TodoWrite" {
		s.todos = parseTodos(ev.Call.Arguments)
	}
	// 后台任务归属：Bash 用 background=true 起的任务，结果里带着 task_id=task_N
	if ev.Kind == domain.EventToolResult {
		for _, id := range taskIdsIn(ev.Text) {
			if !containsString(s.taskIds, id) {
				s.taskIds = append(s.taskIds, id)
			}
		}
	}
	// 第一轮开始时把"提问已经记进会话"这件事落到磁盘：core 在发第一个 EventStep 之前
	// 已经 AppendUser 过了。这样运行中刷新页面能看见本次提问（snapshot 读的是磁盘那份）。
	// 写文件放在锁外，别把订阅者挡在后面
	savePrompt := ev.Kind == domain.EventStep && ev.Step == 1 && !s.promptSaved
	if savePrompt {
		s.promptSaved = true
	}
	s.mu.Unlock()

	if savePrompt {
		// 这里就在 core 的 goroutine 里，data 此刻不会被别处写，序列化是安全的
		_ = s.store.Save(s.data)
	}
	s.broadcast(e)
}

// parseTodos 从 TodoWrite 的参数里取出整份清单。参数不是合法 JSON（模型偶尔会写坏）
// 时返回 nil 并且不动原来的清单——宁可显示旧的，也别把清单清空
func parseTodos(raw string) []tool.TodoItem {
	if strings.TrimSpace(raw) == "" {
		return nil
	}
	var args struct {
		Todos []tool.TodoItem `json:"todos"`
	}
	if err := json.Unmarshal([]byte(raw), &args); err != nil {
		return nil
	}
	return args.Todos
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
	s.promptSaved = false
	// 新一轮开始了，上一轮的结果作废：列表上这会儿该显示"进行中"
	s.outcome = ""
	s.approvals = make(map[string]chan domain.ApprovalAnswer)
	mode := parseMode(s.mode)
	data := s.data
	s.mu.Unlock()

	s.broadcast(startedEvent(0, prompt))

	go func() {
		defer cancel()
		runErr := core.Loop(domain.Ctx{
			Prompt:       prompt,
			Images:       images,
			Provider:     p,
			Tools:        tool.ForMode(mode),
			Context:      ctx,
			WorkDir:      data.WorkDir,
			Shell:        tool.ShellName(),
			Session:      data,
			ContextLimit: mc.Limit(),
			Mode:         mode,
			MaxSteps:     cfg.Steps(),
			Emit:         s.emit,
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
}

func (s *Session) snapshot() snapshot {
	s.mu.Lock()
	data, running, mode, store, status := s.data, s.running, s.mode, s.store, s.statusLocked()
	s.mu.Unlock()

	// 跑着的时候不读内存里那份：core 正在往它上面追加消息，读它就是在和数据竞争
	// （TUI 那边靠"运行中只读自己攒的流式缓冲"避开了这件事）。
	// 磁盘上是上一轮结束时的完整版本，而且 Start 里的第一次事件就把本次提问写进去了，
	// 所以运行中刷新页面能看到完整历史加这条提问；本轮正在产生的增量由实时事件流负责画
	view := data
	if running {
		if saved, err := store.Load(data.Id); err == nil {
			view = saved
		}
	}

	out := snapshot{
		Id:       view.Id,
		Title:    view.Title,
		WorkDir:  view.WorkDir,
		Running:  running,
		Status:   status,
		Mode:     mode,
		Messages: historyFromSession(view),
	}
	if !view.UpdatedAt.IsZero() {
		out.UpdatedAt = view.UpdatedAt.Format("2006-01-02 15:04:05")
	}
	return out
}
