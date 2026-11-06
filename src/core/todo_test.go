package core

import (
	"context"
	"encoding/json"
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

// 催办得给出两条明确的出口：能做完就核销，这轮做不了就移出清单并说明。
// 特别是不能再说"还没做完就接着做"——2026-11-06 实测：模型把"重启后生效"这类
// 必须等本轮结束才能验证的事写进了清单，被这句一逼，只能反复改写措辞让它消失，
// 一轮里改了 16 次、237 条消息
func TestTodoNoticeGivesBothWaysOut(t *testing.T) {
	s := sessionWithTodos(`{"todos":[{"content":"重启后生效","status":"in_progress"}]}`)
	msgs := todoNotice(s)
	if len(msgs) != 1 {
		t.Fatalf("该有一条提醒，实际 %d 条", len(msgs))
	}
	got := msgs[0].Content

	// 出口一：核销
	if !strings.Contains(got, "completed") {
		t.Fatalf("得说清能做完就标 completed:\n%s", got)
	}
	// 出口二：这轮做不了就移出清单 + 在回答里说明
	if !strings.Contains(got, "从清单里删掉") || !strings.Contains(got, "回答里说清楚") {
		t.Fatalf("得给出\"做不了就从清单删掉并说明\"这条路:\n%s", got)
	}
	// 必须点名"要等这一轮结束才能验证"这种自指情形
	if !strings.Contains(got, "这一轮结束") {
		t.Fatalf("得点明有些事要等本轮结束才能验证:\n%s", got)
	}
	// 不能再推着人往前干
	if strings.Contains(got, "接着做") {
		t.Fatalf("不该再说\"接着做\"，那会把做不了的事逼成反复改措辞:\n%s", got)
	}
}

// 一轮里反复改清单要能被拦一下：超过上限补一句"别再改了"，
// 而且只说一次——否则刹车本身又变成新的念叨
func TestRunWarnsAboutRepeatedTodoEdits(t *testing.T) {
	s := domain.NewSession("/tmp/demo")
	s.AppendUser("干点活")

	// 前 12 轮每轮都改一次清单，之后老实收尾
	p := &repeatingTodoProvider{limit: 12}

	if _, err := Run(domain.Ctx{
		Provider: p,
		Tools:    map[string]domain.Tool{"echo": &fakeTool{}, todoToolName: fakeTodoTool{}},
		Context:  context.Background(),
		WorkDir:  "/tmp/demo",
		Session:  s,
		Emit:     func(domain.Event) {},
	}, s); err != nil {
		t.Fatalf("Run 失败: %v", err)
	}

	// 上限内不该出现，过了上限才出现（加进去一次，之后跟着本回合走）
	const probe = "别再重写"
	firstAt := -1
	for i, msgs := range p.calls {
		for _, m := range msgs {
			if strings.Contains(m.Content, probe) {
				firstAt = i
				break
			}
		}
		if firstAt >= 0 {
			break
		}
	}
	if firstAt < 0 {
		t.Fatal("改清单超过上限了却没提醒")
	}
	// 第 N 次改清单是在第 N 次请求里发生的，提醒从下一次请求起才带上
	if firstAt < maxTodoEdits {
		t.Fatalf("还没到上限（%d 次）就提醒了，首次出现在第 %d 次请求", maxTodoEdits, firstAt+1)
	}
}

// repeatingTodoProvider 前 limit 轮每轮都调一次 TodoWrite，之后就收尾。
// 它把每次请求原样留一份，好断言 core 往里塞了几句提醒
type repeatingTodoProvider struct {
	limit int
	n     int
	calls [][]domain.AIChatMessage
}

func (p *repeatingTodoProvider) Chat(ctx context.Context, messages []domain.AIChatMessage, tools []domain.ToolSpec, onDelta func(domain.StreamEvent)) (domain.ChatResult, error) {
	p.calls = append(p.calls, messages)
	p.n++
	if p.n <= p.limit {
		return domain.ChatResult{
			Content:   "再改一次清单",
			ToolCalls: []domain.ToolCall{{Id: "t", Name: "TodoWrite", Arguments: `{"todos":[{"content":"干活","status":"in_progress"}]}`}},
		}, nil
	}
	return domain.ChatResult{Content: "收尾了"}, nil
}

// fakeTodoTool 是个什么都不做的 TodoWrite：这些用例只看 core 发了几次提醒
type fakeTodoTool struct{}

func (fakeTodoTool) Name() string           { return todoToolName }
func (fakeTodoTool) Description() string    { return "记清单" }
func (fakeTodoTool) Schema() map[string]any { return map[string]any{"type": "object"} }
func (fakeTodoTool) Mutating() bool         { return false }
func (fakeTodoTool) Execute(ctx context.Context, raw json.RawMessage) (string, error) {
	return "待办已更新", nil
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
