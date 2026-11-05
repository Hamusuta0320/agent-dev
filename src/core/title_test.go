package core

import (
	"context"
	"errors"
	"strings"
	"testing"

	"hamusuta/agent-dev/src/domain"
)

// titleProvider 只用来测拟标题：按预设的回答或错误回应，顺便记下被问了几次
type titleProvider struct {
	reply string
	err   error
	calls int
}

func (p *titleProvider) Chat(ctx context.Context, messages []domain.AIChatMessage, tools []domain.ToolSpec, onDelta func(domain.StreamEvent)) (domain.ChatResult, error) {
	p.calls++
	if p.err != nil {
		return domain.ChatResult{}, p.err
	}
	return domain.ChatResult{Content: p.reply}, nil
}

// 第一轮聊完就给会话起个标题，拟用的原文是用户第一句提问和模型对它的回答
func TestEnsureTitleNamesSession(t *testing.T) {
	p := &titleProvider{reply: "给 /resume 列表加会话标题。"}
	s := domain.NewSession(".")
	s.AppendUser("session要优化下，可以第一轮会话后生成一个session标题")
	s.AppendAssistant("好的，我加一个标题字段。", "", nil)

	ensureTitle(domain.Ctx{Provider: p, Context: context.Background()}, s)

	if s.Title != "给 /resume 列表加会话标题" {
		t.Fatalf("标题不对: %q", s.Title)
	}
	if p.calls != 1 {
		t.Fatalf("应该问一次模型，实际 %d", p.calls)
	}
}

// 已经有标题就不再打扰模型：这是第一轮之后才做的事，不是每轮都做
func TestEnsureTitleKeepsExisting(t *testing.T) {
	p := &titleProvider{reply: "另一个标题"}
	s := domain.NewSession(".")
	s.Title = "原来的标题"
	s.AppendUser("随便问点什么")

	ensureTitle(domain.Ctx{Provider: p, Context: context.Background()}, s)

	if s.Title != "原来的标题" || p.calls != 0 {
		t.Fatalf("有标题就不该再拟: title=%q calls=%d", s.Title, p.calls)
	}
}

// 拟标题失败（接口报错、模型返回空）不算错误，退回用第一句提问凑一个，
// 否则 /resume 列表里还是一串时间戳
func TestEnsureTitleFallsBackToPrompt(t *testing.T) {
	s := domain.NewSession(".")
	s.AppendUser("  把恢复会话的列表\n 弄得能看懂一点，现在只有时间戳 ")
	s.AppendAssistant("好", "", nil)

	ensureTitle(domain.Ctx{Provider: &titleProvider{err: errors.New("接口挂了")}, Context: context.Background()}, s)
	if s.Title != "把恢复会话的列表 弄得能看懂一点，现在只有时间戳" {
		t.Fatalf("该退回成第一句提问: %q", s.Title)
	}

	// 模型返回空也是同样的兜底
	blank := domain.NewSession(".")
	blank.AppendUser("改个 bug")
	ensureTitle(domain.Ctx{Provider: &titleProvider{reply: "  "}, Context: context.Background()}, blank)
	if blank.Title != "改个 bug" {
		t.Fatalf("模型返回空时也该兜底: %q", blank.Title)
	}
}

// 没有模型的会话（比如只跑工具）就别硬拟标题了，也不能崩
func TestEnsureTitleWithoutProvider(t *testing.T) {
	s := domain.NewSession(".")
	s.AppendUser("改个 bug")

	ensureTitle(domain.Ctx{Context: context.Background()}, s)

	if s.Title != "改个 bug" {
		t.Fatalf("没模型时也该用提问兜底: %q", s.Title)
	}
}

// 还没有任何消息的会话没什么可拟的，保持空标题
func TestEnsureTitleSkipsEmptySession(t *testing.T) {
	s := domain.NewSession(".")
	p := &titleProvider{reply: "不该被问到"}

	ensureTitle(domain.Ctx{Provider: p, Context: context.Background()}, s)

	if s.Title != "" || p.calls != 0 {
		t.Fatalf("空会话不该拟标题: title=%q calls=%d", s.Title, p.calls)
	}
}

// 模型爱把标题写成"标题：xxx"、加上引号或者一长串，这些都得收拾干净
func TestCleanTitle(t *testing.T) {
	cases := map[string]string{
		"标题：给恢复会话列表加标题。":    "给恢复会话列表加标题",
		"《给会话起标题》":          "给会话起标题",
		`"给 /resume 加标题"`:   "给 /resume 加标题",
		"给会话起标题\n（另一个说法）":   "给会话起标题",
		"  加个标题  ":          "加个标题",
		"标题：":               "",
	}
	for in, want := range cases {
		if got := cleanTitle(in); got != want {
			t.Fatalf("cleanTitle(%q) = %q，应该是 %q", in, got, want)
		}
	}

	// 太长就截到上限，别让 /resume 里那一行被顶出去
	long := strings.Repeat("长", titleMaxRunes+5)
	if got := []rune(cleanTitle(long)); len(got) != titleMaxRunes {
		t.Fatalf("超长标题该截到 %d 字: %q", titleMaxRunes, string(got))
	}
}

// 拟标题时送给模型的原文要压到上限以内，别让标题请求自己撑爆上下文
func TestClipText(t *testing.T) {
	if got := clipText("  短的  ", titleSourceRunes); got != "短的" {
		t.Fatalf("短文本该原样留下: %q", got)
	}
	long := strings.Repeat("长", titleSourceRunes+10)
	got := clipText(long, titleSourceRunes)
	if !strings.HasSuffix(got, "…") || len([]rune(got)) != titleSourceRunes+1 {
		t.Fatalf("长文本该截断成 %d 字加省略号: %d", titleSourceRunes, len([]rune(got)))
	}
}
