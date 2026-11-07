package domain

import (
	"strings"
	"testing"
)

// toolCallsOf 把历史里某条消息的调用 id 摘出来，断言时好比对
func toolCallIds(msg AIChatMessage) []string {
	out := make([]string, 0, len(msg.ToolCalls))
	for _, c := range msg.ToolCalls {
		out = append(out, c.Id)
	}
	return out
}

// 每个 tool_call 后面都要紧跟一条对应的 tool 消息：接口按顺序配，少一条整条请求被拒
// （"insufficient tool messages following tool_calls"）
func TestHistoryPairsToolResults(t *testing.T) {
	s := NewSession("/tmp/demo")
	s.AppendUser("跑两条命令")
	s.AppendAssistant("", "", []ToolCall{
		{Id: "call_a", Name: "Bash", Arguments: `{"command":"echo a"}`},
		{Id: "call_b", Name: "Bash", Arguments: `{"command":"echo b"}`},
	})
	s.AppendToolResult(ToolCall{Id: "call_a", Name: "Bash"}, "exit_code: 0\n a")
	s.AppendToolResult(ToolCall{Id: "call_b", Name: "Bash"}, "exit_code: 0\n b")

	history := s.History()
	// 提问 + 带两个调用的 assistant + 两条结果
	if len(history) != 4 {
		t.Fatalf("应该有 4 条，实际 %d: %+v", len(history), history)
	}
	if got := toolCallIds(history[1]); len(got) != 2 {
		t.Fatalf("调用应该都在: %+v", history[1])
	}
	for i, id := range []string{"call_a", "call_b"} {
		msg := history[2+i]
		if msg.Role != RoleTool || msg.ToolCallId != id {
			t.Fatalf("第 %d 条结果应该是 %s，实际 %+v", i+1, id, msg)
		}
	}
}

// 会话里只剩助手那次调用、结果没留下来（进程在工具执行中途被杀，会话就停在这个
// 中间态上）：补一条占位，别让整条会话从此每一轮都 400
func TestHistoryFillsMissingToolResult(t *testing.T) {
	s := NewSession("/tmp/demo")
	s.AppendUser("看一下")
	s.AppendAssistant("我查一下", "", []ToolCall{
		{Id: "call_a", Name: "Read", Arguments: `{"path":"a.go"}`},
		{Id: "call_b", Name: "Read", Arguments: `{"path":"b.go"}`},
	})
	// 只有第一个调用的结果落到了盘上，第二个没有
	s.AppendToolResult(ToolCall{Id: "call_a", Name: "Read"}, "package main")

	history := s.History()
	if len(history) != 4 {
		t.Fatalf("应该有 提问 + 调用 + 两条结果 4 条，实际 %d: %+v", len(history), history)
	}
	if history[2].ToolCallId != "call_a" || history[2].Content != "package main" {
		t.Fatalf("有结果的那个照原样发: %+v", history[2])
	}
	if history[3].ToolCallId != "call_b" || !strings.Contains(history[3].Content, "没有留下结果") {
		t.Fatalf("缺结果的那个该补一条占位: %+v", history[3])
	}
	// 占位只出现在发给模型的视图里，会话本身不动：下一次请求还会再补一遍
	if len(s.Messages) != 3 {
		t.Fatalf("会话不该被改写，实际 %d 条: %+v", len(s.Messages), s.Messages)
	}
}

// 结果在会话里的先后不能信：一律按 tool_calls 的顺序重发。
// 接口按顺序认，不认 id 对不对得上
func TestHistoryOrdersResultsByToolCalls(t *testing.T) {
	s := NewSession("/tmp/demo")
	s.AppendAssistant("", "", []ToolCall{
		{Id: "call_a", Name: "Read", Arguments: `{"path":"a.go"}`},
		{Id: "call_b", Name: "Read", Arguments: `{"path":"b.go"}`},
	})
	// 故意把两条结果写反
	s.AppendToolResult(ToolCall{Id: "call_b", Name: "Read"}, "这是 b")
	s.AppendToolResult(ToolCall{Id: "call_a", Name: "Read"}, "这是 a")

	history := s.History()
	if len(history) != 3 {
		t.Fatalf("应该有 调用 + 两条结果 3 条，实际 %d: %+v", len(history), history)
	}
	if history[1].ToolCallId != "call_a" || history[1].Content != "这是 a" {
		t.Fatalf("第一条要按 tool_calls 的顺序对到 call_a: %+v", history[1])
	}
	if history[2].ToolCallId != "call_b" || history[2].Content != "这是 b" {
		t.Fatalf("第二条要对到 call_b: %+v", history[2])
	}
}

// 对不上任何调用的工具结果（调用被摘要覆盖了，或者是别的版本写坏的）不能发出去：
// 接口同样会拒掉没有前导 tool_calls 的 tool 消息
func TestHistorySkipsOrphanToolResult(t *testing.T) {
	s := NewSession("/tmp/demo")
	s.Messages = []SessionMessage{
		{Role: RoleUser, Content: "老问题"},
		// 留下一条没有出处的工具结果：它的调用在摘要之前，早被盖掉了
		{Role: RoleTool, Content: "孤零零的结果", ToolName: "Read", ToolCallId: "call_gone"},
		{Role: RoleUser, Content: "新问题"},
	}
	s.Summary = "之前聊过"
	s.SummaryUpTo = 1

	history := s.History()
	if len(history) != 2 {
		t.Fatalf("应该只剩下摘要和提问 2 条，实际 %d: %+v", len(history), history)
	}
	if history[1].Content != "新问题" {
		t.Fatalf("最近那条提问要留着: %+v", history[1])
	}
}
