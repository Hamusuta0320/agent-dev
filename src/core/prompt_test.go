package core

import (
	"os"
	"path/filepath"
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

// 系统提示词按会话存快照：同样的输入必须原样返回同一份，否则每个 step 都在改前缀
func TestSystemPromptReusesSnapshot(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(path, []byte("# 项目约定\n第一版\n"), 0o644); err != nil {
		t.Fatalf("写约定文件失败: %v", err)
	}
	session := domain.NewSession(dir)
	ctx := domain.Ctx{WorkDir: dir, Session: session}

	first := systemPrompt(ctx)
	if session.PromptCache == nil {
		t.Fatal("应该留下了一份快照")
	}
	if second := systemPrompt(ctx); second != first {
		t.Fatal("输入没变时不该重拼，返回的字节必须一模一样")
	}
}

// 约定文件被改过时快照要失效，不能一直拿老提示词
func TestSystemPromptSnapshotInvalidated(t *testing.T) {
	dir := t.TempDir()
	path := filepath.Join(dir, "AGENTS.md")
	if err := os.WriteFile(path, []byte("# 项目约定\n第一版\n"), 0o644); err != nil {
		t.Fatalf("写约定文件失败: %v", err)
	}
	session := domain.NewSession(dir)
	ctx := domain.Ctx{WorkDir: dir, Session: session}
	if !strings.Contains(systemPrompt(ctx), "第一版") {
		t.Fatal("提示词里应该有第一版约定")
	}

	if err := os.WriteFile(path, []byte("# 项目约定\n第二版，这次多加了三条规矩\n"), 0o644); err != nil {
		t.Fatalf("改写约定文件失败: %v", err)
	}
	got := systemPrompt(ctx)
	if strings.Contains(got, "第一版") || !strings.Contains(got, "第二版") {
		t.Fatalf("约定文件改过之后快照没失效:\n%s", got)
	}
}

// 切模式要重拼提示词：只读和完全授权写进去的约束不一样
func TestSystemPromptSnapshotFollowsMode(t *testing.T) {
	session := domain.NewSession(t.TempDir())
	ctx := domain.Ctx{WorkDir: t.TempDir(), Session: session, Mode: domain.ModeFull}
	full := systemPrompt(ctx)
	if !strings.Contains(full, "完全授权") {
		t.Fatalf("完全授权模式的提示词不对:\n%s", full)
	}

	ctx.Mode = domain.ModeReadOnly
	readOnly := systemPrompt(ctx)
	if readOnly == full || !strings.Contains(readOnly, "只读") {
		t.Fatalf("切了模式提示词没跟着变:\n%s", readOnly)
	}
}

// 尾部注入合并成一处：没有通知也没有提醒时什么都不多出来
func TestTrailingMessages(t *testing.T) {
	if got := trailingMessages(nil); len(got) != 0 {
		t.Fatalf("没有内容时不该多出消息: %+v", got)
	}
	notice := domain.AIChatMessage{Role: domain.RoleUser, Content: "## 待办清单\n还有两条没核销"}
	got := trailingMessages([]domain.AIChatMessage{notice})
	if len(got) != 1 || got[0].Content != notice.Content {
		t.Fatalf("提醒应该原样跟在末尾: %+v", got)
	}
}
