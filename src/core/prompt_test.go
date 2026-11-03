package core

import (
	"os"
	"strings"
	"testing"

	"hamusuta/agent-dev/src/domain"
)

func TestSystemPromptContainsEnv(t *testing.T) {
	p := systemPrompt(domain.Ctx{WorkDir: `/tmp/demo`, Shell: "pwsh"})
	// 每个工具都要用 intent 交代意图，提示词里得说清楚
	for _, want := range []string{"/tmp/demo", "pwsh", "Read", "Grep", "Edit", "Write", "Bash", "intent"} {
		if !strings.Contains(p, want) {
			t.Fatalf("系统提示词里缺少 %q:\n%s", want, p)
		}
	}
}

// 没传 WorkDir 时要退回进程当前目录，不能空着
func TestSystemPromptFallsBackToCwd(t *testing.T) {
	wd, err := os.Getwd()
	if err != nil {
		t.Skipf("拿不到当前目录: %v", err)
	}
	p := systemPrompt(domain.Ctx{})
	if !strings.Contains(p, wd) {
		t.Fatalf("系统提示词里缺少当前目录 %q:\n%s", wd, p)
	}
}

func TestMakeMessages(t *testing.T) {
	s := domain.NewSession("")
	s.AppendUser("改个 bug")
	msgs := buildMessages(systemPrompt(domain.Ctx{}), s)
	if len(msgs) != 2 {
		t.Fatalf("应该只有 system + user 两条，实际 %d", len(msgs))
	}
	if msgs[0].Role != "system" || !strings.Contains(msgs[0].Content, "编程助手") {
		t.Fatalf("第一条应是系统提示词: %+v", msgs[0])
	}
	if msgs[1].Role != "user" || msgs[1].Content != "改个 bug" {
		t.Fatalf("第二条应是用户输入: %+v", msgs[1])
	}
}

// 后台任务通知永远不进系统提示词：那里的任何变化都会打掉整段历史的上下文缓存。
// 没有通知时也不该多出一条空消息。
func TestTaskNoticeNotInSystemPrompt(t *testing.T) {
	p := systemPrompt(domain.Ctx{WorkDir: `/tmp/demo`, Shell: "pwsh"})
	if strings.Contains(p, "## 后台任务") {
		t.Fatalf("系统提示词里不该有后台任务小节:\n%s", p)
	}
	if msgs := taskMessages(); len(msgs) != 0 {
		t.Fatalf("没有任务通知时不该多出消息: %+v", msgs)
	}
}

// 有通知时作为一条 user 消息追加在对话末尾，内容带上任务摘要
func TestTaskNoticeIsTrailingUserMessage(t *testing.T) {
	msgs := taskNotice([]string{"- task_1 已完成（exit_code: 0，耗时 1.2s）：go test ./..."})
	if len(msgs) != 1 || msgs[0].Role != domain.RoleUser {
		t.Fatalf("应该是一条 user 消息: %+v", msgs)
	}
	if !strings.Contains(msgs[0].Content, "## 后台任务") || !strings.Contains(msgs[0].Content, "task_1") {
		t.Fatalf("通知内容不对: %q", msgs[0].Content)
	}
	if len(taskNotice(nil)) != 0 {
		t.Fatal("没有通知时不该返回消息")
	}
}
