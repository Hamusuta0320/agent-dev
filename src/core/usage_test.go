package core

import (
	"context"
	"testing"

	"hamusuta/agent-dev/src/domain"
)

// usageProvider 只回一条回答，但带上响应里该有的 usage
type usageProvider struct{}

func (usageProvider) Chat(ctx context.Context, messages []domain.AIChatMessage, tools []domain.ToolSpec, onDelta func(domain.StreamEvent)) (domain.ChatResult, error) {
	return domain.ChatResult{
		Content: "看完了",
		Usage:   domain.Usage{PromptTokens: 4321, CompletionTokens: 321, TotalTokens: 4642},
	}, nil
}

// 响应里报了用量时，上下文占用要以响应里的 prompt tokens 为准，
// 系统提示词/工具定义/对话消息三项之和要正好等于它
func TestUsageComesFromResponse(t *testing.T) {
	var events []domain.Event
	session := domain.NewSession("/tmp/demo")

	_, err := Run(domain.Ctx{
		Prompt:       "看看这个项目",
		Provider:     usageProvider{},
		Tools:        map[string]domain.Tool{"echo": &fakeTool{}},
		Context:      context.Background(),
		WorkDir:      "/tmp/demo",
		Session:      session,
		ContextLimit: 100000,
		Emit:         func(ev domain.Event) { events = append(events, ev) },
	}, session)
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}

	var got domain.ContextUsage
	found := false
	for _, ev := range events {
		if ev.Kind == domain.EventUsage {
			got, found = ev.Usage, true
		}
	}
	if !found {
		t.Fatalf("没收到上下文占用事件: %+v", events)
	}
	if got.PromptTokens != 4321 || got.CompletionTokens != 321 {
		t.Fatalf("总数应该照抄响应的 usage: %+v", got)
	}
	if sum := got.SystemTokens + got.ToolTokens + got.MessageTokens; sum != got.PromptTokens {
		t.Fatalf("三项分项之和 %d 应该等于 prompt tokens %d: %+v", sum, got.PromptTokens, got)
	}
	// 系统提示词比一句提问长得多，拆出来的占比也该更大
	if got.SystemTokens <= got.MessageTokens {
		t.Fatalf("系统提示词的占比应该大于一句提问: %+v", got)
	}
	if got.Total() != 4642 {
		t.Fatalf("总量应该是 prompt + 回答: %d", got.Total())
	}
	// 上下文上限要带出来，已用百分比按 prompt / 上限算
	if got.ContextLimit != 100000 {
		t.Fatalf("上下文上限没带出来: %+v", got)
	}
	if p := got.LimitPercent(); p != 4 {
		t.Fatalf("4321/100000 的已用百分比应该是 4%%，实际 %d%%", p)
	}
}

// 响应里没有 usage 时退化成纯估算：三项之和还算得出来，只是没有回答的用量
func TestUsageFallsBackToEstimate(t *testing.T) {
	var events []domain.Event
	session := domain.NewSession("/tmp/demo")

	_, err := Run(domain.Ctx{
		Prompt:   "随便看看",
		Provider: &multiCallProvider{},
		Tools:    map[string]domain.Tool{"echo": &fakeTool{}},
		Context:  context.Background(),
		WorkDir:  "/tmp/demo",
		Session:  session,
		Emit:     func(ev domain.Event) { events = append(events, ev) },
	}, session)
	if err != nil {
		t.Fatalf("Run 失败: %v", err)
	}

	for _, ev := range events {
		if ev.Kind != domain.EventUsage {
			continue
		}
		if ev.Usage.PromptTokens <= 0 {
			t.Fatalf("估算不出 prompt tokens: %+v", ev.Usage)
		}
		if sum := ev.Usage.SystemTokens + ev.Usage.ToolTokens + ev.Usage.MessageTokens; sum != ev.Usage.PromptTokens {
			t.Fatalf("三项分项之和 %d 应该等于 %d", sum, ev.Usage.PromptTokens)
		}
		return
	}
	t.Fatal("没收到上下文占用事件")
}

// 增量维护的消息估算必须和"把历史整段数一遍"对得上，
// 否则聊得越久，界面上那个上下文占用就越偏
func TestUsageTrackerMatchesFullEstimate(t *testing.T) {
	session := domain.NewSession("/tmp/demo")
	session.AppendUser("看看代码")
	specs := []domain.ToolSpec{{Name: "Read", Description: "读文件", Parameters: map[string]any{"type": "object"}}}
	tracker := newUsageTracker(specs, session)

	call := domain.ToolCall{Id: "call_1", Name: "Read", Arguments: `{"path":"a.go","intent":"读一下"}`}
	session.AppendAssistant("我先看看", "思考一下", []domain.ToolCall{call})
	tracker.trackAssistant("我先看看", []domain.ToolCall{call})

	session.AppendToolResult(call, "package a\n")
	tracker.trackToolResult(call, "package a\n")

	// 既没正文也没工具调用的回答不会发给模型，两边都该跳过
	session.AppendAssistant("", "", nil)
	tracker.trackAssistant("", nil)

	if want := estimateMessages(session.History()); tracker.messageTokens != want {
		t.Fatalf("增量估算是 %d，整段重算是 %d，对不上", tracker.messageTokens, want)
	}

	// 工具定义只算一次，和当场重算的结果也要一致
	if want := estimateToolSpecs(specs); tracker.toolTokens != want {
		t.Fatalf("工具定义估算是 %d，重算是 %d", tracker.toolTokens, want)
	}
}

// splitByWeight 要把零头分干净，各份加起来正好是总数
func TestSplitByWeight(t *testing.T) {
	cases := []struct {
		total   int
		weights []int
	}{
		{100, []int{1, 1, 1}},
		{7, []int{10, 20, 30}},
		{4321, []int{137, 264, 12}},
		// 权重全是 0（比如工具列表为空、消息也空）时只能全给 0
		{5, []int{0, 0, 0}},
	}
	for _, c := range cases {
		parts := splitByWeight(c.total, c.weights...)
		sum := 0
		for _, p := range parts {
			if p < 0 {
				t.Fatalf("%v 拆出了负数: %v", c, parts)
			}
			sum += p
		}
		if c.total > 0 && c.weights[0]+c.weights[1]+c.weights[2] > 0 && sum != c.total {
			t.Fatalf("%d 按 %v 拆完是 %d，对不上", c.total, c.weights, sum)
		}
	}
}
