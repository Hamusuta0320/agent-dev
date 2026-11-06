package webui

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hamusuta/agent-dev/src/domain"
)

// 跑一轮的过程中，每走完一步都要落盘。
//
// 这一条守的是"刷新页面 / 切走再切回来还能看见历史"：
// snapshot 在运行中读的是磁盘那份（内存里 core 正在追加，读它会撞上并发写），
// 所以只有落到盘上的东西才看得见。以前只在"本轮第一个 step 开始"时存过一次
// （那时会话里还只有一句提问），中途刷新就只剩提问，助手说了什么、跑了哪些工具全没了。
func TestSessionPersistsEachStepWhileRunning(t *testing.T) {
	m := newTestManager(t)
	target := filepath.Join(t.TempDir(), "note.txt")
	if err := writeTestFile(target, "内容\n"); err != nil {
		t.Fatalf("准备文件失败: %v", err)
	}
	withFakeProvider(t, &fakeProvider{steps: []fakeStep{
		// 第一步：说一句话，然后调一个只读工具
		{content: "我先看看", tool: "Read", args: fmt.Sprintf(`{"intent":"看看文件","path":%q}`, target)},
		// 第二步：一直等到被中断，模拟"还在跑"
		{wait: true},
	}})
	m.cfg.Mode = "full"

	s := m.create()
	if err := s.Start(m.Config(), "读一下这个文件", nil); err != nil {
		t.Fatalf("起一轮失败: %v", err)
	}

	// 等磁盘上出现这一步的产物。工具结果是在第二步的开头之前就记进会话的，
	// 所以这里该能等到 user + assistant + tool 三条
	deadline := time.Now().Add(5 * time.Second)
	var saved *domain.Session
	for time.Now().Before(deadline) {
		if got, err := s.store.Load(s.Data().Id); err == nil {
			saved = got
			if len(got.Messages) >= 3 {
				break
			}
		}
		time.Sleep(20 * time.Millisecond)
	}
	if saved == nil {
		t.Fatal("磁盘上读不到会话")
	}
	if len(saved.Messages) < 3 {
		t.Fatalf("跑着的时候磁盘上只有 %d 条消息，应该已经有三条（提问 + 回答 + 工具结果）: %+v",
			len(saved.Messages), saved.Messages)
	}

	roles := make([]string, 0, len(saved.Messages))
	for _, msg := range saved.Messages {
		roles = append(roles, string(msg.Role))
	}
	for i, want := range []string{"user", "assistant", "tool"} {
		if roles[i] != want {
			t.Fatalf("第 %d 条应该是 %s，实际 %v", i+1, want, roles)
		}
	}
	// 内容也得对得上：不光骨架在，字也在
	if saved.Messages[0].Content != "读一下这个文件" {
		t.Fatalf("提问不对: %q", saved.Messages[0].Content)
	}
	if saved.Messages[1].Content != "我先看看" {
		t.Fatalf("助手的回答没落盘: %q", saved.Messages[1].Content)
	}
	if !strings.Contains(saved.Messages[2].Content, "内容") {
		t.Fatalf("工具结果没落盘: %q", saved.Messages[2].Content)
	}

	// 刷新页面走的就是 snapshot：它此刻该看得见这一步的东西。
	// 注意快照的形状和磁盘不一样——工具结果是挂在对应 assistant 的 tool_calls 下面的
	// （historyFromSession 干的事），所以这里是 2 条而不是 3 条
	snap := s.snapshot()
	if !snap.Running {
		t.Fatal("这一轮还在跑，snapshot 应该说明 running")
	}
	if len(snap.Messages) < 2 {
		t.Fatalf("运行中拉到的快照只有 %d 条消息，刷新页面就看不到历史了: %+v",
			len(snap.Messages), snap.Messages)
	}
	last := snap.Messages[len(snap.Messages)-1]
	if last.Role != "assistant" || last.Content != "我先看看" {
		t.Fatalf("快照里没有这一步的回答: %+v", last)
	}
	if len(last.ToolCalls) == 0 || !strings.Contains(last.ToolCalls[0].Output, "内容") {
		t.Fatalf("快照里没有这个工具结果: %+v", last.ToolCalls)
	}

	s.Cancel()
	collect(t, mustSubscribe(t, s), untilFinish())
}

// 落盘不能按 token 来：流式的正文和思考一个 token 一条，
// 每条都存一次会把磁盘和 CPU 全吃满。这里直接盯着 shouldSave 的判定
func TestShouldSaveOnlyAtStepBoundaries(t *testing.T) {
	save := map[domain.EventKind]bool{
		domain.EventStep:       true,
		domain.EventAnswerEnd:  true,
		domain.EventToolResult: true,

		domain.EventContent:   false,
		domain.EventReasoning: false,
		domain.EventUsage:     false,
		domain.EventToolCall:  false,
		domain.EventApproval:  false,
	}
	for kind, want := range save {
		if got := shouldSave(kind); got != want {
			t.Fatalf("kind=%v 该返回 %v，实际 %v", kind, want, got)
		}
	}
}

// stopSession 中断这一轮并等它真的停下来。
//
// 不能只 Cancel 就走人：Cancel 只是取消了 ctx，core 那个 goroutine 还要走完收尾
// （落盘、广播 done），而落盘是往 t.TempDir() 里写文件。测试一返回，TempDir 的清理
// 就开始删目录，Windows 上撞上正在写的文件会报"目录非空"删不掉——偶发失败，
// 而且看起来像是被测代码的问题。等它停了再走，窗口就没了
func stopSession(t *testing.T, s *Session) {
	t.Helper()
	s.Cancel()
	deadline := time.Now().Add(5 * time.Second)
	for s.Running() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if s.Running() {
		t.Fatal("中断之后这一轮还没停下来")
	}
}

// writeTestFile 是给上面那个用例准备素材的小工具
func writeTestFile(path, content string) error {
	return os.WriteFile(path, []byte(content), 0o644)
}
