package core

import (
	"os"
	"strings"
	"testing"

	"hamusuta/agent-dev/src/domain"
)

func TestSystemPromptContainsEnv(t *testing.T) {
	p := systemPrompt(domain.Ctx{WorkDir: `/tmp/demo`, Shell: "pwsh"})
	for _, want := range []string{"/tmp/demo", "pwsh", "Read", "Grep", "Edit", "Write", "Bash"} {
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
	msgs := buildMessages(domain.Ctx{}, s)
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
