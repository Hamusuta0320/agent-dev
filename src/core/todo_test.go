package core

import (
	"context"
	"strings"
	"testing"

	"hamusuta/agent-dev/src/domain"
)

// quietProvider 每次都只回一段纯文本、不调工具，用来模拟"模型准备收尾"。
// 它把每次请求的 messages 留一份，好断言末尾有没有多出那条催核销的提醒
type quietProvider struct{ calls [][]domain.AIChatMessage }

func (p *quietProvider) Chat(ctx context.Context, messages []domain.AIChatMessage, tools []domain.ToolSpec, onDelta func(domain.StreamEvent)) (domain.ChatResult, error) {
	p.calls = append(p.calls, messages)
	return domain.ChatResult{Content: "干完了"}, nil
}

// lastContent 取最后一次请求末尾那条消息的正文，没有消息时返回空串
func (p *quietProvider) lastContent() string {
	if len(p.calls) == 0 {
		return ""
	}
	last := p.calls[len(p.calls)-1]
	if len(last) == 0 {
		return ""
	}
	return last[len(last)-1].Content
}

// sessionWithTodos 造一条会话，历史里已经有模型调用 TodoWrite 记下的那份清单
func sessionWithTodos(raw string) *domain.Session {
	s := domain.NewSession("/tmp/demo")
	s.AppendUser("干点活")
	s.AppendAssistant("先列个计划", "", []domain.ToolCall{
		{Id: "todo_1", Name: "TodoWrite", Arguments: raw},
	})
	s.AppendToolResult(domain.ToolCall{Id: "todo_1", Name: "TodoWrite"}, "待办已更新")
	return s
}

// runQuiet 拿这条会话跑一轮，返回 provider 看看请求了几次、末尾说了什么
func runQuiet(t *testing.T, s *domain.Session) *quietProvider {
	t.Helper()
	p := &quietProvider{}
	if _, err := Run(domain.Ctx{
		Provider: p,
		Tools:    map[string]domain.Tool{"echo": &fakeTool{}},
		Context:  context.Background(),
		WorkDir:  "/tmp/demo",
		Session:  s,
		// 别让控制台的纯文本进度刷满测试输出
		Emit: func(domain.Event) {},
	}, s); err != nil {
		t.Fatalf("Run 失败: %v", err)
	}
	return p
}

// 清单里还有没做完的：追问一轮，末尾补一条提醒，而且提醒不写进会话历史
func TestRunNudgesUnfinishedTodos(t *testing.T) {
	s := sessionWithTodos(`{"intent":"列计划","todos":[
		{"content":"读代码","status":"completed"},
		{"content":"改配置","status":"in_progress"}]}`)
	before := len(s.Messages)

	p := runQuiet(t, s)

	if len(p.calls) != 2 {
		t.Fatalf("清单没做完时该再问一轮，实际请求了 %d 次", len(p.calls))
	}
	got := p.lastContent()
	for _, want := range []string{"待办清单", "[~] 2. 改配置", "completed"} {
		if !strings.Contains(got, want) {
			t.Fatalf("提醒里少了 %q:\n%s", want, got)
		}
	}
	// 已经做完的那条不再重复提
	if strings.Contains(got, "读代码") {
		t.Fatalf("做完的条目不该出现在提醒里:\n%s", got)
	}
	// 提醒只发给模型，不落进会话：否则恢复会话时又冒出来一遍
	if want := before + 2; len(s.Messages) != want {
		t.Fatalf("提醒不该写进会话历史，期望 %d 条消息，实际 %d", want, len(s.Messages))
	}
}

// 催过一次还不改就不再催：否则模型和自检来回打转，永远收不了尾
func TestRunNudgesTodosOnlyOnce(t *testing.T) {
	s := sessionWithTodos(`{"todos":[{"content":"改配置","status":"pending"}]}`)

	p := runQuiet(t, s)

	if len(p.calls) != 2 {
		t.Fatalf("最多催一次，实际请求了 %d 次", len(p.calls))
	}
}

// 没什么可催的情况：清单全做完、清空过、压根没列过、参数是坏的，都只问一轮
func TestRunSkipsNudgeWhenNothingPending(t *testing.T) {
	cases := map[string]*domain.Session{
		"全做完了": sessionWithTodos(`{"todos":[
			{"content":"读代码","status":"completed"},
			{"content":"写改动","status":"completed"}]}`),
		"清单清空过": sessionWithTodos(`{"todos":[]}`),
		"没列过清单": func() *domain.Session {
			s := domain.NewSession("/tmp/demo")
			s.AppendUser("随口问一句")
			return s
		}(),
		"最后一次参数坏了": sessionWithTodos(`{"intent":"只有意图"}`),
	}
	for name, s := range cases {
		p := runQuiet(t, s)
		if len(p.calls) != 1 {
			t.Fatalf("%s：不该催，实际请求了 %d 次，末尾是:\n%s", name, len(p.calls), p.lastContent())
		}
	}
}

// lastTodos 只认会话历史里的清单，不认进程级那份：两条会话不该互相串
func TestLastTodosIsPerSession(t *testing.T) {
	a := sessionWithTodos(`{"todos":[{"content":"甲","status":"pending"}]}`)
	b := sessionWithTodos(`{"todos":[{"content":"乙","status":"pending"}]}`)

	if got := lastTodos(a); len(got) != 1 || got[0].Content != "甲" {
		t.Fatalf("会话 a 的清单不对: %+v", got)
	}
	if got := lastTodos(b); len(got) != 1 || got[0].Content != "乙" {
		t.Fatalf("会话 b 的清单不对: %+v", got)
	}
	if got := lastTodos(nil); got != nil {
		t.Fatalf("没有会话时该返回 nil: %+v", got)
	}
	if got := lastTodos(domain.NewSession("/tmp/demo")); got != nil {
		t.Fatalf("空会话该返回 nil: %+v", got)
	}
}
