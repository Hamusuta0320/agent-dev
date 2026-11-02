package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"hamusuta/agent-dev/src/domain"
)

type fakeProvider struct {
	calls [][]domain.AIChatMessage
	step  int
}

func (p *fakeProvider) Chat(ctx context.Context, messages []domain.AIChatMessage, tools []domain.ToolSpec, onDelta func(domain.StreamEvent)) (domain.ChatResult, error) {
	p.calls = append(p.calls, messages)
	p.step++
	if p.step == 1 {
		return domain.ChatResult{
			Reasoning: "先看看文件",
			ToolCalls: []domain.ToolCall{{Id: "call_1", Name: "echo", Arguments: `{"text":"hi"}`}},
		}, nil
	}
	return domain.ChatResult{Content: "改好了", Reasoning: "收尾"}, nil
}

type fakeTool struct {
	lastArgs string
	// mutating 为 true 时假装是会改动磁盘的工具，用来测授权流程
	mutating bool
}

func (t *fakeTool) Name() string           { return "echo" }
func (t *fakeTool) Description() string    { return "回显" }
func (t *fakeTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (t *fakeTool) Mutating() bool         { return t.mutating }
func (t *fakeTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	t.lastArgs = string(raw)
	return "ok", nil
}

// 一轮问答里的用户输入、思考、回答、工具调用和工具结果都要落到会话里
func TestLoopRecordsSession(t *testing.T) {
	p := &fakeProvider{}
	echo := &fakeTool{}
	session := domain.NewSession("/tmp/demo")

	err := Loop(domain.Ctx{
		Prompt:   "改个 bug",
		Provider: p,
		Tools:    map[string]domain.Tool{"echo": echo},
		Context:  context.Background(),
		WorkDir:  "/tmp/demo",
		Session:  session,
	})
	if err != nil {
		t.Fatalf("Loop 失败: %v", err)
	}
	if echo.lastArgs != `{"text":"hi"}` {
		t.Fatalf("工具没有拿到参数: %s", echo.lastArgs)
	}

	want := []string{"user", "assistant", "tool", "assistant"}
	if len(session.Messages) != len(want) {
		t.Fatalf("会话应该有 %d 条消息，实际 %d: %+v", len(want), len(session.Messages), session.Messages)
	}
	for i, role := range want {
		if session.Messages[i].Role != role {
			t.Fatalf("第 %d 条应该是 %s，实际 %+v", i+1, role, session.Messages[i])
		}
	}
	if session.Messages[0].Content != "改个 bug" {
		t.Fatalf("用户输入没记下来: %+v", session.Messages[0])
	}
	assistant := session.Messages[1]
	if assistant.Reasoning != "先看看文件" || len(assistant.ToolCalls) != 1 || assistant.ToolCalls[0].Name != "echo" {
		t.Fatalf("模型的思考/工具调用没记下来: %+v", assistant)
	}
	toolMsg := session.Messages[2]
	if toolMsg.Content != "ok" || toolMsg.ToolCallId != "call_1" || toolMsg.ToolName != "echo" {
		t.Fatalf("工具结果没记下来: %+v", toolMsg)
	}
	if session.Messages[3].Content != "改好了" || session.Messages[3].Reasoning != "收尾" {
		t.Fatalf("模型的回答没记下来: %+v", session.Messages[3])
	}

	// 第二次请求应该已经带上历史（system + 用户 + 第一轮 assistant + tool）
	if len(p.calls) != 2 {
		t.Fatalf("应该请求两次模型，实际 %d", len(p.calls))
	}
	if len(p.calls[1]) != 4 {
		t.Fatalf("第二次请求应该带 4 条消息，实际 %d: %+v", len(p.calls[1]), p.calls[1])
	}
	if p.calls[1][0].Role != domain.RoleSystem || p.calls[1][3].Role != domain.RoleTool {
		t.Fatalf("历史顺序不对: %+v", p.calls[1])
	}
}

type failingTool struct{}

func (failingTool) Name() string           { return "boom" }
func (failingTool) Description() string    { return "总是失败" }
func (failingTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (failingTool) Mutating() bool         { return false }
func (failingTool) Execute(context.Context, json.RawMessage) (string, error) {
	return "", errors.New("打开文件失败: a.go")
}

// multiCallProvider 第一轮一口气调三个工具：正常的、失败的、不存在的
type multiCallProvider struct{ step int }

func (p *multiCallProvider) Chat(ctx context.Context, messages []domain.AIChatMessage, tools []domain.ToolSpec, onDelta func(domain.StreamEvent)) (domain.ChatResult, error) {
	p.step++
	if p.step == 1 {
		return domain.ChatResult{ToolCalls: []domain.ToolCall{
			{Id: "call_1", Name: "echo", Arguments: `{}`},
			{Id: "call_2", Name: "boom", Arguments: `{}`},
			{Id: "call_3", Name: "missing", Arguments: `{}`},
		}}, nil
	}
	return domain.ChatResult{Content: "好了"}, nil
}

// 每次工具调用都要先报"调用中"再报结果：成功带耗时，失败要带原因
func TestToolCallStatus(t *testing.T) {
	var events []domain.Event
	session := domain.NewSession("/tmp/demo")

	_, err := Run(domain.Ctx{
		Prompt:   "随便改改",
		Provider: &multiCallProvider{},
		Tools: map[string]domain.Tool{
			"echo": &fakeTool{},
			"boom": failingTool{},
		},
		Context: context.Background(),
		WorkDir: "/tmp/demo",
		Session: session,
		Emit:    func(ev domain.Event) { events = append(events, ev) },
	}, session)
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}

	var calls, results []domain.Event
	for _, ev := range events {
		switch ev.Kind {
		case domain.EventToolCall:
			calls = append(calls, ev)
		case domain.EventToolResult:
			results = append(results, ev)
		}
	}
	if len(calls) != 3 || len(results) != 3 {
		t.Fatalf("应该各有 3 次调用和结果，实际 %d/%d: %+v", len(calls), len(results), events)
	}

	for i, ev := range calls {
		if ev.Status != domain.ToolStatusRunning {
			t.Fatalf("第 %d 次调用状态应该是调用中，实际 %q", i+1, ev.Status.Label())
		}
		if ev.Call.Id != results[i].Call.Id {
			t.Fatalf("调用和结果没对上: %+v / %+v", ev.Call, results[i].Call)
		}
		// 两个事件要共用同一个开始时间，界面才能把状态和时间对到一次调用上
		if !ev.StartedAt.Equal(results[i].StartedAt) || ev.StartedAt.IsZero() {
			t.Fatalf("第 %d 次调用的开始时间不对: %v / %v", i+1, ev.StartedAt, results[i].StartedAt)
		}
		if results[i].Elapsed < 0 || results[i].ElapsedLabel() == "" {
			t.Fatalf("第 %d 次调用缺少耗时: %v", i+1, results[i].Elapsed)
		}
	}

	if results[0].Status != domain.ToolStatusSuccess || results[0].Err != "" {
		t.Fatalf("正常工具应该调用成功: %+v", results[0])
	}
	if results[1].Status != domain.ToolStatusFailed || results[1].Err != "打开文件失败: a.go" {
		t.Fatalf("工具报错要记成失败并带上原因: %+v", results[1])
	}
	if !strings.Contains(results[1].Text, "打开文件失败: a.go") {
		t.Fatalf("失败原因也要给模型看: %q", results[1].Text)
	}
	if results[2].Status != domain.ToolStatusFailed || results[2].Err != "不存在的工具 missing" {
		t.Fatalf("不存在的工具要记成失败并带上原因: %+v", results[2])
	}

	// 状态只是事件，会话里仍然只记一份工具输出
	for _, msg := range session.Messages {
		if msg.Role == domain.RoleTool && msg.Content == "" {
			t.Fatalf("工具结果不该为空: %+v", msg)
		}
	}
}

// approveProvider 连着两轮调用同一个会改文件的工具，第三轮收尾
type approveProvider struct{ step int }

func (p *approveProvider) Chat(ctx context.Context, messages []domain.AIChatMessage, tools []domain.ToolSpec, onDelta func(domain.StreamEvent)) (domain.ChatResult, error) {
	p.step++
	if p.step <= 2 {
		return domain.ChatResult{
			ToolCalls: []domain.ToolCall{{Id: "call_" + string(rune('0'+p.step)), Name: "echo", Arguments: `{"text":"hi"}`}},
		}, nil
	}
	return domain.ChatResult{Content: "好了"}, nil
}

// "编辑需授权"模式下写类工具要先问：用户拒绝就不执行，模型拿到的是拒绝原因
func TestAskModeDeniesWrite(t *testing.T) {
	var (
		events    []domain.Event
		approvals int
	)
	echo := &fakeTool{mutating: true}
	session := domain.NewSession("/tmp/demo")

	_, err := Run(domain.Ctx{
		Prompt:   "改个 bug",
		Provider: &approveProvider{},
		Tools:    map[string]domain.Tool{"echo": echo},
		Context:  context.Background(),
		WorkDir:  "/tmp/demo",
		Session:  session,
		Mode:     domain.ModeAsk,
		Emit: func(ev domain.Event) {
			events = append(events, ev)
			if ev.Kind == domain.EventApproval {
				approvals++
				ev.Reply <- domain.ApprovalAnswer{} // 拒绝
			}
		},
	}, session)
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}

	if approvals != 2 {
		t.Fatalf("两次写调用都该先问一次，实际问了 %d 次", approvals)
	}
	if echo.lastArgs != "" {
		t.Fatalf("用户拒绝了就不该执行工具: %s", echo.lastArgs)
	}
	for _, ev := range events {
		if ev.Kind != domain.EventToolResult {
			continue
		}
		if ev.Status != domain.ToolStatusFailed || !strings.Contains(ev.Err, "拒绝") {
			t.Fatalf("拒绝要记成失败并说明原因: %+v", ev)
		}
	}
}

// 用户选"本回合都允许"之后，同样的工具不再打扰；完全授权模式从头就不问
func TestAskModeAlwaysAllow(t *testing.T) {
	var (
		events    []domain.Event
		approvals int
	)
	echo := &fakeTool{mutating: true}
	session := domain.NewSession("/tmp/demo")

	_, err := Run(domain.Ctx{
		Prompt:   "改个 bug",
		Provider: &approveProvider{},
		Tools:    map[string]domain.Tool{"echo": echo},
		Context:  context.Background(),
		WorkDir:  "/tmp/demo",
		Session:  session,
		Mode:     domain.ModeAsk,
		Emit: func(ev domain.Event) {
			events = append(events, ev)
			if ev.Kind == domain.EventApproval {
				approvals++
				ev.Reply <- domain.ApprovalAnswer{Allowed: true, Always: true}
			}
		},
	}, session)
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if approvals != 1 {
		t.Fatalf("选了总是允许之后不该再问，实际问了 %d 次", approvals)
	}
	if echo.lastArgs == "" {
		t.Fatal("允许之后工具应该真的执行了")
	}

	// 完全授权模式下 mutating 工具直接跑，一个确认事件都没有
	events = nil
	echo2 := &fakeTool{mutating: true}
	if _, err := Run(domain.Ctx{
		Prompt:   "改个 bug",
		Provider: &approveProvider{},
		Tools:    map[string]domain.Tool{"echo": echo2},
		Context:  context.Background(),
		WorkDir:  "/tmp/demo",
		Session:  domain.NewSession("/tmp/demo"),
		Mode:     domain.ModeFull,
		Emit:     func(ev domain.Event) { events = append(events, ev) },
	}, domain.NewSession("/tmp/demo")); err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	for _, ev := range events {
		if ev.Kind == domain.EventApproval {
			t.Fatal("完全授权模式不该发确认事件")
		}
	}
	if echo2.lastArgs == "" {
		t.Fatal("完全授权模式下工具应该直接执行")
	}
}

// 只读工具在需授权模式下不用问，别为了看文件也弹确认
func TestAskModeSkipsReadOnlyTool(t *testing.T) {
	var approvals int
	echo := &fakeTool{} // mutating 为 false
	session := domain.NewSession("/tmp/demo")

	if _, err := Run(domain.Ctx{
		Prompt:   "看看代码",
		Provider: &approveProvider{},
		Tools:    map[string]domain.Tool{"echo": echo},
		Context:  context.Background(),
		WorkDir:  "/tmp/demo",
		Session:  session,
		Mode:     domain.ModeAsk,
		Emit: func(ev domain.Event) {
			if ev.Kind == domain.EventApproval {
				approvals++
				ev.Reply <- domain.ApprovalAnswer{}
			}
		},
	}, session); err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if approvals != 0 {
		t.Fatalf("只读工具不该要授权，实际问了 %d 次", approvals)
	}
	if echo.lastArgs == "" {
		t.Fatal("只读工具应该直接执行")
	}
}

// 只读模式下就算写类工具漏进了工具表，也不能真的执行
func TestReadOnlyModeBlocksWrite(t *testing.T) {
	echo := &fakeTool{mutating: true}
	session := domain.NewSession("/tmp/demo")

	_, err := Run(domain.Ctx{
		Prompt:   "改个 bug",
		Provider: &approveProvider{},
		Tools:    map[string]domain.Tool{"echo": echo},
		Context:  context.Background(),
		WorkDir:  "/tmp/demo",
		Session:  session,
		Mode:     domain.ModeReadOnly,
		Emit:     func(domain.Event) {},
	}, session)
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if echo.lastArgs != "" {
		t.Fatalf("只读模式不该执行写类工具: %s", echo.lastArgs)
	}
}

// 用户拒绝时写下的说明要原样转给模型，模型才知道该往哪个方向改
func TestAskModeDenyWithFeedback(t *testing.T) {
	echo := &fakeTool{mutating: true}
	session := domain.NewSession("/tmp/demo")
	var reasons []string

	_, err := Run(domain.Ctx{
		Prompt:   "改个 bug",
		Provider: &approveProvider{},
		Tools:    map[string]domain.Tool{"echo": echo},
		Context:  context.Background(),
		WorkDir:  "/tmp/demo",
		Session:  session,
		Mode:     domain.ModeAsk,
		Emit: func(ev domain.Event) {
			switch ev.Kind {
			case domain.EventApproval:
				ev.Reply <- domain.ApprovalAnswer{Feedback: "别动这个文件，改 config 就行"}
			case domain.EventToolResult:
				reasons = append(reasons, ev.Text)
			}
		},
	}, session)
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if len(reasons) == 0 {
		t.Fatal("应该有拒绝的工具结果")
	}
	for _, text := range reasons {
		for _, want := range []string{"用户拒绝了这次操作", "别动这个文件，改 config 就行"} {
			if !strings.Contains(text, want) {
				t.Fatalf("拒绝原因里少了 %q: %q", want, text)
			}
		}
	}
	// 说明也要落到会话里，事后能看出来为什么被拒
	found := false
	for _, msg := range session.Messages {
		if strings.Contains(msg.Content, "改 config 就行") {
			found = true
		}
	}
	if !found {
		t.Fatal("拒绝原因没记进会话")
	}
}

// slowTool 执行到一半就把这一轮取消掉，模拟用户中途按 Ctrl+C
type slowTool struct {
	cancel context.CancelFunc
}

func (t *slowTool) Name() string           { return "slow" }
func (t *slowTool) Description() string    { return "跑到一半被中断" }
func (t *slowTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (t *slowTool) Mutating() bool         { return false }
func (t *slowTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	t.cancel()
	<-ctx.Done()
	return "", ctx.Err()
}

// tripleCallProvider 第一轮一口气调三次同一个工具
type tripleCallProvider struct{ step int }

func (p *tripleCallProvider) Chat(ctx context.Context, messages []domain.AIChatMessage, tools []domain.ToolSpec, onDelta func(domain.StreamEvent)) (domain.ChatResult, error) {
	p.step++
	if p.step == 1 {
		return domain.ChatResult{ToolCalls: []domain.ToolCall{
			{Id: "call_1", Name: "slow", Arguments: `{}`},
			{Id: "call_2", Name: "slow", Arguments: `{}`},
			{Id: "call_3", Name: "slow", Arguments: `{}`},
		}}, nil
	}
	return domain.ChatResult{Content: "好了"}, nil
}

// 中断要当场把这一轮结束掉（不再要下一轮），并且给这一轮剩下的工具调用补上结果，
// 否则会话里留着没有结果的调用，下次请求会被模型接口拒掉
func TestCancelStopsRunAndFillsToolResults(t *testing.T) {
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	p := &tripleCallProvider{}
	session := domain.NewSession("/tmp/demo")
	_, err := Run(domain.Ctx{
		Prompt:   "跑个长任务",
		Provider: p,
		Tools:    map[string]domain.Tool{"slow": &slowTool{cancel: cancel}},
		Context:  ctx,
		WorkDir:  "/tmp/demo",
		Session:  session,
		Emit:     func(domain.Event) {},
	}, session)

	if !errors.Is(err, context.Canceled) {
		t.Fatalf("中断应该原样报 context.Canceled，实际 %v", err)
	}
	if p.step != 1 {
		t.Fatalf("中断之后不该再请求模型，实际请求了 %d 次", p.step)
	}
	// Run 不会记用户输入（那是 Loop 的事），这里第一条就是模型那条
	var assistant domain.SessionMessage
	for _, msg := range session.Messages {
		if msg.Role == domain.RoleAssistant {
			assistant = msg
			break
		}
	}
	if len(assistant.ToolCalls) != 3 {
		t.Fatalf("会话里应该记着 3 个工具调用: %+v", assistant)
	}
	results := 0
	for _, msg := range session.Messages {
		if msg.Role != domain.RoleTool {
			continue
		}
		results++
		if !strings.Contains(msg.Content, "中断") {
			t.Fatalf("中断的工具结果要说明原因: %q", msg.Content)
		}
	}
	if results != 3 {
		t.Fatalf("3 个工具调用都要有结果，实际 %d 条", results)
	}
}

// sleepTool 睡一会儿再返回，用来观察同一批调用是不是并行跑的
type sleepTool struct {
	name string
	d    time.Duration
}

func (t *sleepTool) Name() string           { return t.name }
func (t *sleepTool) Description() string    { return "睡一会儿" }
func (t *sleepTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (t *sleepTool) Mutating() bool         { return false }
func (t *sleepTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	time.Sleep(t.d)
	return t.name + " done", nil
}

// twoCallProvider 第一轮同时要两个只读工具，第二轮收尾
type twoCallProvider struct{ step int }

func (p *twoCallProvider) Chat(ctx context.Context, messages []domain.AIChatMessage, tools []domain.ToolSpec, onDelta func(domain.StreamEvent)) (domain.ChatResult, error) {
	p.step++
	if p.step == 1 {
		return domain.ChatResult{ToolCalls: []domain.ToolCall{
			{Id: "call_1", Name: "sleep_a", Arguments: `{}`},
			{Id: "call_2", Name: "sleep_b", Arguments: `{}`},
		}}, nil
	}
	return domain.ChatResult{Content: "好了"}, nil
}

// 同一轮里的只读工具可以并行跑，不该一个等一个
func TestReadOnlyToolsRunInParallel(t *testing.T) {
	const d = 120 * time.Millisecond
	session := domain.NewSession("/tmp/demo")

	start := time.Now()
	if _, err := Run(domain.Ctx{
		Prompt:   "看看两个文件",
		Provider: &twoCallProvider{},
		Tools: map[string]domain.Tool{
			"sleep_a": &sleepTool{name: "sleep_a", d: d},
			"sleep_b": &sleepTool{name: "sleep_b", d: d},
		},
		Context: context.Background(),
		WorkDir: "/tmp/demo",
		Session: session,
		Emit:    func(domain.Event) {},
	}, session); err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	elapsed := time.Since(start)
	if elapsed > d+d/2 {
		t.Fatalf("两个 %s 的只读工具应该并行跑，实际花了 %s", d, elapsed)
	}
}

// busyTool 记录自己有没有和别的调用重叠，mutating 由字段决定
type busyTool struct {
	name     string
	mutating bool
	busy     atomic.Int32
	overlap  atomic.Bool
}

func (t *busyTool) Name() string           { return t.name }
func (t *busyTool) Description() string    { return "看看会不会和别人撞上" }
func (t *busyTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (t *busyTool) Mutating() bool         { return t.mutating }
func (t *busyTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	if t.busy.Add(1) > 1 {
		t.overlap.Store(true)
	}
	defer t.busy.Add(-1)
	time.Sleep(30 * time.Millisecond)
	return "ok", nil
}

// 同一轮里的两个写操作必须一个一个来，不能同时改磁盘
func TestMutatingToolsRunOneByOne(t *testing.T) {
	tool := &busyTool{name: "write_something", mutating: true}
	session := domain.NewSession("/tmp/demo")

	p := &multiSameProvider{}
	if _, err := Run(domain.Ctx{
		Prompt:   "改两个地方",
		Provider: p,
		Tools:    map[string]domain.Tool{tool.Name(): tool},
		Context:  context.Background(),
		WorkDir:  "/tmp/demo",
		Session:  session,
		Mode:     domain.ModeFull,
		Emit:     func(domain.Event) {},
	}, session); err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	if tool.overlap.Load() {
		t.Fatal("写类工具不该并行执行")
	}
}

// multiSameProvider 第一轮连调两次同一个工具，第二轮收尾
type multiSameProvider struct{ step int }

func (p *multiSameProvider) Chat(ctx context.Context, messages []domain.AIChatMessage, tools []domain.ToolSpec, onDelta func(domain.StreamEvent)) (domain.ChatResult, error) {
	p.step++
	if p.step == 1 {
		return domain.ChatResult{ToolCalls: []domain.ToolCall{
			{Id: "call_1", Name: "write_something", Arguments: `{}`},
			{Id: "call_2", Name: "write_something", Arguments: `{}`},
		}}, nil
	}
	return domain.ChatResult{Content: "好了"}, nil
}
