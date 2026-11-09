package core

import (
	"context"
	"errors"
	"fmt"
	"strings"
	"testing"

	"hamusuta/agent-dev/src/domain"
)

func TestNeedCompact(t *testing.T) {
	tests := []struct {
		limit, used int
		want        bool
	}{
		{0, 99999, false}, // 不知道窗口大小，不压
		{1000, 0, false},
		{1000, compactThresholdPercent*10 - 1, false},
		{1000, compactThresholdPercent * 10, true}, // 正好到阈值就开始压
		{1000, 900, true},
	}
	for _, c := range tests {
		if got := needCompact(c.limit, c.used); got != c.want {
			t.Fatalf("needCompact(%d, %d) = %v，期望 %v", c.limit, c.used, got, c.want)
		}
	}
}

// compactDialog 造一段老对话：pairs 组「模型调用工具 + 工具结果」，最后跟一条用户提问
func compactDialog(pairs int, toolContent string) *domain.Session {
	s := &domain.Session{}
	for i := 0; i < pairs; i++ {
		id := fmt.Sprintf("call_%d", i)
		s.Messages = append(s.Messages,
			domain.SessionMessage{
				Role:      domain.RoleAssistant,
				ToolCalls: []domain.ToolCall{{Id: id, Name: "echo", Arguments: `{}`}},
			},
			domain.SessionMessage{
				Role:       domain.RoleTool,
				Content:    toolContent,
				ToolCallId: id,
				ToolName:   "echo",
			},
		)
	}
	s.Messages = append(s.Messages, domain.SessionMessage{Role: domain.RoleUser, Content: "最后的问题"})
	return s
}

// summaryProvider 把摘要请求（tools 为 nil）和正常请求分开记下来
type summaryProvider struct {
	summaries  int
	summaryReq string
	mainCalls  [][]domain.AIChatMessage
}

func (p *summaryProvider) Chat(ctx context.Context, messages []domain.AIChatMessage, tools []domain.ToolSpec, onDelta func(domain.StreamEvent)) (domain.ChatResult, error) {
	if tools == nil {
		p.summaries++
		for _, m := range messages {
			p.summaryReq += m.Content
		}
		return domain.ChatResult{Content: "之前改过 a.go，还没跑测试"}, nil
	}
	p.mainCalls = append(p.mainCalls, messages)
	return domain.ChatResult{Content: "好了"}, nil
}

// failingSummaryProvider 的摘要请求总是失败，用来确认压缩失败不会拖垮这一轮
type failingSummaryProvider struct {
	mainCalls int
}

func (p *failingSummaryProvider) Chat(ctx context.Context, messages []domain.AIChatMessage, tools []domain.ToolSpec, onDelta func(domain.StreamEvent)) (domain.ChatResult, error) {
	if tools == nil {
		return domain.ChatResult{}, errors.New("连接超时")
	}
	p.mainCalls++
	return domain.ChatResult{Content: "好了"}, nil
}

// bigToolOutput 造一条够大的工具结果：只有超过 compactMinRunes 的才值得截断
func bigToolOutput() string { return strings.Repeat("line\n", 600) }

// 占用刚到阈值时只截断老工具输出，不做摘要
func TestCompactTruncatesOldToolOutputs(t *testing.T) {
	session := compactDialog(20, bigToolOutput())
	usage := &usageTracker{}
	before := estimateMessages(session.History())
	// 让占用正好卡在阈值上：截断之后自然会掉下去
	limit := before * 100 / compactThresholdPercent

	var events []domain.Event
	changed := compactContext(domain.Ctx{ContextLimit: limit}, session, func(ev domain.Event) {
		events = append(events, ev)
	}, usage, 0, false, false)
	if !changed {
		t.Fatal("超过阈值应该压缩会话")
	}
	if session.Summary != "" || session.SummaryUpTo != 0 {
		t.Fatalf("只是截断就够了，不该做摘要: %q / %d", session.Summary, session.SummaryUpTo)
	}

	keepFrom := keepBoundary(session, false)
	compressed := 0
	for i, msg := range session.Messages {
		if !msg.Compressed {
			continue
		}
		compressed++
		if i >= keepFrom {
			t.Fatalf("保留区里的消息不该被改写: %+v", msg)
		}
	}
	if compressed == 0 {
		t.Fatal("应该有条工具输出被标记为压缩")
	}
	if len(events) != 1 || events[0].Kind != domain.EventCompact || !strings.Contains(events[0].Text, "截断") {
		t.Fatalf("应该报一条截断事件，实际 %+v", events)
	}
}

// 几百字的工具结果压下去省不了多少 token，却要让前缀从那条开始重算，不值得动
func TestTruncateSkipsSmallToolOutputs(t *testing.T) {
	session := compactDialog(3, strings.Repeat("line\n", 20))
	big := domain.SessionMessage{
		Role:       domain.RoleTool,
		Content:    bigToolOutput(),
		ToolCallId: "big",
		ToolName:   "echo",
	}
	session.Messages = append([]domain.SessionMessage{big}, session.Messages...)

	if got := truncateOlderToolOutputs(session, len(session.Messages)); got != 1 {
		t.Fatalf("只有那条大的该被截断，实际 %d 条", got)
	}
	if !session.Messages[0].Compressed {
		t.Fatal("够大的工具结果应该被标记")
	}
	for _, msg := range session.Messages[1:] {
		if msg.Compressed {
			t.Fatalf("短工具结果不该被标记: %+v", msg)
		}
	}
}

// 保留区按 token 预算划，不按条数：消息少时整段都留着，一条都不改写
func TestKeepBoundaryReservesRecentTail(t *testing.T) {
	short := compactDialog(2, bigToolOutput())
	if got := keepBoundary(short, false); got != 0 {
		t.Fatalf("消息这么少，应该整段保留，实际保留区起点 %d", got)
	}

	long := compactDialog(40, bigToolOutput())
	keepFrom := keepBoundary(long, false)
	if keepFrom <= 0 || keepFrom >= len(long.Messages) {
		t.Fatalf("保留区起点不合理: %d / %d", keepFrom, len(long.Messages))
	}
	if kept := len(long.Messages) - keepFrom; kept < keepRecentMessages {
		t.Fatalf("保留区至少要 %d 条，实际 %d 条", keepRecentMessages, kept)
	}
	kept := 0
	for _, msg := range long.Messages[keepFrom:] {
		kept += estimateSessionMessage(msg)
	}
	if kept < keepRecentTokens {
		t.Fatalf("保留区至少要 %d token，实际 %d", keepRecentTokens, kept)
	}

	// 手动压缩（/compact）是用户明确要求的，只按条数留底，不受 token 预算影响
	if manualKept := len(long.Messages) - keepBoundary(long, true); manualKept != keepRecentMessages {
		t.Fatalf("手动压缩该只留 %d 条，实际 %d 条", keepRecentMessages, manualKept)
	}
}

// 接口上一轮报的用量已经超阈值时，即使本地估算偏低也要摘要
func TestCompactForceSummarizes(t *testing.T) {
	session := compactDialog(40, bigToolOutput())
	usage := &usageTracker{}
	before := estimateMessages(session.History())
	// 这个上限下，截断后的本地估算会低于阈值；force 仍然要求做摘要
	limit := before * 100 / compactThresholdPercent
	p := &summaryProvider{}

	changed := compactContext(domain.Ctx{
		ContextLimit: limit,
		Provider:     p,
		Context:      context.Background(),
	}, session, nil, usage, 0, true, false)
	if !changed || p.summaries != 1 || session.Summary == "" || session.SummaryUpTo == 0 {
		t.Fatalf("强制摘要没有生效: changed=%v summaries=%d summary=%q upTo=%d",
			changed, p.summaries, session.Summary, session.SummaryUpTo)
	}
}

// 截断之后还超阈值就做摘要；分界不能拆散工具调用和它的结果
func TestCompactSummarizesOldMessages(t *testing.T) {
	session := compactDialog(40, bigToolOutput())
	usage := &usageTracker{}
	p := &summaryProvider{}

	changed := compactContext(domain.Ctx{
		ContextLimit: 1, // 故意给个极小的窗口，保证截断后仍然超阈值
		Provider:     p,
		Context:      context.Background(),
	}, session, nil, usage, 0, false, false)
	if !changed {
		t.Fatal("应该压缩了会话")
	}
	if p.summaries != 1 {
		t.Fatalf("应该发一次摘要请求，实际 %d 次", p.summaries)
	}
	if strings.Contains(p.summaryReq, "最后的问题") {
		// 最后一条用户提问在保留区，不该进摘要请求
		t.Fatalf("摘要请求不该包含最近一轮的用户提问: %q", p.summaryReq)
	}
	if session.Summary != "之前改过 a.go，还没跑测试" {
		t.Fatalf("摘要没写回会话: %q", session.Summary)
	}
	end := session.SummaryUpTo
	if end <= 0 || end >= len(session.Messages) {
		t.Fatalf("摘要分界不对: %d / %d", end, len(session.Messages))
	}
	// 保留区不能从工具结果开始，否则它对应的调用被摘掉了
	if session.Messages[end].Role == domain.RoleTool {
		t.Fatalf("分界落在了工具结果上: %+v", session.Messages[end])
	}
	// 如果分界是带工具调用的 assistant，后面那条工具结果必须还在
	if calls := session.Messages[end].ToolCalls; len(calls) > 0 {
		if end+1 >= len(session.Messages) || session.Messages[end+1].ToolCallId != calls[0].Id {
			t.Fatalf("工具调用和结果被拆散了: %+v", session.Messages[end:])
		}
	}
	history := session.History()
	if len(history) == 0 || !strings.Contains(history[0].Content, "之前改过 a.go") {
		t.Fatalf("History 应该带上摘要: %+v", history)
	}
}

// 摘要失败只报一条事件，这一轮照常请求模型
func TestCompactSummaryFailureDoesNotBreakRun(t *testing.T) {
	session := compactDialog(40, bigToolOutput())
	var events []domain.Event
	p := &failingSummaryProvider{}

	_, err := Run(domain.Ctx{
		Provider:     p,
		Tools:        map[string]domain.Tool{},
		Context:      context.Background(),
		WorkDir:      t.TempDir(),
		Session:      session,
		ContextLimit: 1,
		Emit:         func(ev domain.Event) { events = append(events, ev) },
	}, session)
	if err != nil {
		t.Fatalf("压缩失败不该让这一轮失败: %v", err)
	}
	if p.mainCalls != 1 {
		t.Fatalf("压缩失败后仍然应该正常请求模型，实际 %d 次", p.mainCalls)
	}
	if session.Summary != "" || session.SummaryUpTo != 0 {
		t.Fatalf("摘要失败不该写回会话: %q / %d", session.Summary, session.SummaryUpTo)
	}
	found := false
	for _, ev := range events {
		if ev.Kind == domain.EventCompact && strings.Contains(ev.Text, "压缩失败") {
			found = true
		}
	}
	if !found {
		t.Fatalf("应该报一条压缩失败事件: %+v", events)
	}
}

// Run 在发请求前压缩，并且请求里用的是摘要后的历史
func TestRunCompactsBeforeRequest(t *testing.T) {
	session := compactDialog(40, bigToolOutput())
	p := &summaryProvider{}

	if _, err := Run(domain.Ctx{
		Provider:     p,
		Tools:        map[string]domain.Tool{},
		Context:      context.Background(),
		WorkDir:      t.TempDir(),
		Session:      session,
		ContextLimit: 1,
		Emit:         func(domain.Event) {},
	}, session); err != nil {
		t.Fatalf("Run 失败: %v", err)
	}

	if p.summaries != 1 {
		t.Fatalf("应该先摘要一次，实际 %d 次", p.summaries)
	}
	if len(p.mainCalls) != 1 {
		t.Fatalf("应该请求模型一次，实际 %d 次", len(p.mainCalls))
	}
	first := p.mainCalls[0]
	if len(first) < 2 {
		t.Fatalf("请求里的消息太少: %+v", first)
	}
	if first[0].Role != domain.RoleSystem {
		t.Fatalf("第一条应该是系统提示词: %+v", first[0])
	}
	if !strings.Contains(first[1].Content, "[更早对话的摘要]") {
		t.Fatalf("摘要应该紧跟在系统提示词后面: %+v", first[1])
	}
}

// 手动压缩不看阈值：上下文还宽裕，也要把较早的对话整理成摘要
func TestCompactNowIgnoresThreshold(t *testing.T) {
	session := compactDialog(40, bigToolOutput())
	p := &summaryProvider{}

	var events []domain.Event
	changed := CompactNow(domain.Ctx{
		ContextLimit: 1 << 20, // 远远没到阈值
		Provider:     p,
		Context:      context.Background(),
		Emit:         func(ev domain.Event) { events = append(events, ev) },
	}, session)
	if !changed || p.summaries != 1 || session.Summary == "" || session.SummaryUpTo == 0 {
		t.Fatalf("手动压缩没生效: changed=%v summaries=%d summary=%q upTo=%d",
			changed, p.summaries, session.Summary, session.SummaryUpTo)
	}
	found := false
	for _, ev := range events {
		if ev.Kind == domain.EventCompact && strings.Contains(ev.Text, "摘要") {
			found = true
		}
	}
	if !found {
		t.Fatalf("应该报一条整理摘要的事件: %+v", events)
	}
}

// 消息太少（都在保留区里）时手动压缩什么都不做，只说明一句，不该白花一次摘要请求
func TestCompactNowNothingToDo(t *testing.T) {
	session := compactDialog(1, "ok")
	p := &summaryProvider{}

	var events []domain.Event
	changed := CompactNow(domain.Ctx{
		Provider: p,
		Context:  context.Background(),
		Emit:     func(ev domain.Event) { events = append(events, ev) },
	}, session)
	if changed || p.summaries != 0 {
		t.Fatalf("没东西可压时不该动会话: changed=%v summaries=%d", changed, p.summaries)
	}
	if len(events) != 1 || events[0].Kind != domain.EventCompact || !strings.Contains(events[0].Text, "没有可压缩") {
		t.Fatalf("应该报一条「没有可压缩」的事件: %+v", events)
	}
}
