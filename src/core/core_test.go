package core

import (
	"context"
	"encoding/json"
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
