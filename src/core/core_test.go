package core

import (
	"context"
	"encoding/json"
	"errors"
	"strings"
	"testing"

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

type fakeTool struct{ lastArgs string }

func (t *fakeTool) Name() string           { return "echo" }
func (t *fakeTool) Description() string    { return "回显" }
func (t *fakeTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (t *fakeTool) Execute(raw json.RawMessage) (string, error) {
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
func (failingTool) Execute(json.RawMessage) (string, error) {
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
