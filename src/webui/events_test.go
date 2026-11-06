package webui

import (
	"strings"
	"testing"
	"time"

	"hamusuta/agent-dev/src/domain"
)

// 每种事件都要翻成前端认的那个 kind，认不出的事件返回 nil（老前端不该因为多一种事件崩掉）
func TestEventFromDomain(t *testing.T) {
	cases := []struct {
		name string
		in   domain.Event
		want string
	}{
		{"轮次", domain.Event{Kind: domain.EventStep, Step: 2}, kindStep},
		{"思考", domain.Event{Kind: domain.EventReasoning, Text: "想想"}, kindReasoning},
		{"正文", domain.Event{Kind: domain.EventContent, Text: "答"}, kindContent},
		{"回答结束", domain.Event{Kind: domain.EventAnswerEnd, Text: "全文"}, kindAnswerEnd},
		{"重试", domain.Event{Kind: domain.EventRetry, Text: "再试一次"}, kindRetry},
		{"压缩", domain.Event{Kind: domain.EventCompact, Text: "压了"}, kindCompact},
		{"标题", domain.Event{Kind: domain.EventTitle, Text: "新标题"}, kindTitle},
		{"用量", domain.Event{Kind: domain.EventUsage}, kindUsage},
		{"调用", domain.Event{Kind: domain.EventToolCall}, kindToolCall},
		{"结果", domain.Event{Kind: domain.EventToolResult}, kindToolEnd},
		{"审批", domain.Event{Kind: domain.EventApproval}, kindApproval},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			got := eventFromDomain(7, c.in)
			if got == nil {
				t.Fatalf("%s 没翻译出来", c.name)
			}
			if got.Kind != c.want {
				t.Fatalf("kind 应该是 %q，实际 %q", c.want, got.Kind)
			}
			if got.Seq != 7 {
				t.Fatalf("序号没带上: %d", got.Seq)
			}
		})
	}

	if got := eventFromDomain(1, domain.Event{Kind: domain.EventKind(99)}); got != nil {
		t.Fatalf("认不出的事件该返回 nil，实际 %+v", got)
	}
}

// 工具事件要带齐前端要的东西：故意从参数里取意图，失败原因单独一个字段
func TestEventToolFields(t *testing.T) {
	call := domain.ToolCall{Id: "c1", Name: "Bash", Arguments: `{"intent":"跑测试","command":"go test ./..."}`}
	start := eventFromDomain(1, domain.Event{
		Kind: domain.EventToolCall, Call: call,
		Status: domain.ToolStatusRunning, StartedAt: time.Date(2026, 1, 2, 15, 4, 5, 0, time.UTC),
	})
	if start.Tool != "Bash" || start.Intent != "跑测试" || start.CallId != "c1" {
		t.Fatalf("调用事件字段不对: %+v", start)
	}
	if start.StartedAt != "15:04:05" {
		t.Fatalf("开始时间格式不对: %q", start.StartedAt)
	}
	if start.Status != domain.ToolStatusRunning.Label() {
		t.Fatalf("调用中状态不对: %q", start.Status)
	}

	fail := eventFromDomain(2, domain.Event{
		Kind: domain.EventToolResult, Call: call,
		Status: domain.ToolStatusFailed, Text: "执行失败: 没找到 go",
		Err: "没找到 go", Elapsed: 3 * time.Millisecond,
	})
	if fail.Kind != kindToolEnd {
		t.Fatalf("结果事件 kind 不对: %q", fail.Kind)
	}
	// Text 是给模型看的结果，原因单独放，两者不能混在一起
	if fail.Text != "执行失败: 没找到 go" {
		t.Fatalf("结果正文不对: %q", fail.Text)
	}
	if fail.FailCause != "没找到 go" {
		t.Fatalf("失败原因不对: %q", fail.FailCause)
	}
	if fail.Elapsed != "3ms" {
		t.Fatalf("耗时格式不对: %q", fail.Elapsed)
	}
}

// 用量事件把真值原样带上，百分比算好一起给
func TestEventUsage(t *testing.T) {
	got := eventFromDomain(1, domain.Event{Kind: domain.EventUsage, Usage: domain.ContextUsage{
		SystemTokens: 1200, ToolTokens: 3000, MessageTokens: 5800,
		PromptTokens: 10000, CompletionTokens: 250, ContextLimit: 40000,
	}})
	if got.Usage == nil {
		t.Fatal("用量没带上")
	}
	if got.Usage.PromptTokens != 10000 || got.Usage.ContextLimit != 40000 {
		t.Fatalf("用量数字不对: %+v", got.Usage)
	}
	if got.Usage.UsedPercent != 25 {
		t.Fatalf("百分比算错了: %d", got.Usage.UsedPercent)
	}
}

// 确认请求：Kind 换成字符串，空的 diff 不往前端带
func TestEventApproval(t *testing.T) {
	write := eventFromDomain(1, domain.Event{
		Kind: domain.EventApproval,
		Approval: &domain.ApprovalRequest{
			Tool: "Edit", Kind: domain.ApprovalWriteFile,
			Target: "src/cli/ui.go", Detail: "3 行改成 5 行",
			Diff: &domain.DiffPreview{Removed: []string{"旧"}, Added: []string{"新"}, Truncated: true},
		},
	})
	if write.Approval == nil {
		t.Fatal("确认内容没带上")
	}
	if write.Approval.Kind != "write_file" || write.Approval.Target != "src/cli/ui.go" {
		t.Fatalf("确认内容不对: %+v", write.Approval)
	}
	if d := write.Approval.Diff; d == nil || len(d.Removed) != 1 || !d.Truncated {
		t.Fatalf("diff 没带上: %+v", d)
	}

	run := eventFromDomain(1, domain.Event{
		Kind:     domain.EventApproval,
		Approval: &domain.ApprovalRequest{Tool: "Bash", Kind: domain.ApprovalRunCommand, Target: "go build ./..."},
	})
	if run.Approval.Kind != "run_command" {
		t.Fatalf("执行命令的 kind 不对: %q", run.Approval.Kind)
	}

	// 没有 diff 预览时不要把空对象带过去，前端就不用判"是不是白给一段空框"
	empty := eventFromDomain(1, domain.Event{
		Kind:     domain.EventApproval,
		Approval: &domain.ApprovalRequest{Tool: "Write", Diff: &domain.DiffPreview{}},
	})
	if empty.Approval.Diff != nil {
		t.Fatalf("空 diff 不该带上: %+v", empty.Approval.Diff)
	}
}

// 历史：提问、回答、工具调用按顺序留下，工具结果挂到对应调用上
func TestHistoryFromSession(t *testing.T) {
	s := &domain.Session{}
	s.AppendUser("看看这个文件")
	call := domain.ToolCall{Id: "c1", Name: "Read", Arguments: `{"intent":"读一下","path":"a.go"}`}
	s.AppendAssistant("", "", []domain.ToolCall{call})
	s.AppendToolResult(call, "package main")
	s.AppendAssistant("看完了，是个 main 包", "先看看入口", nil)

	got := historyFromSession(s)
	if len(got) != 3 {
		t.Fatalf("应该留下提问 + 两轮回答，实际 %d 条: %+v", len(got), got)
	}
	if got[0].Role != "user" || got[0].Content != "看看这个文件" {
		t.Fatalf("第一条该是提问: %+v", got[0])
	}
	// 工具调用挂在这一轮的 assistant 上，结果也合进去了
	turn := got[1]
	if turn.Role != "assistant" || len(turn.ToolCalls) != 1 {
		t.Fatalf("第二条该带一次工具调用: %+v", turn)
	}
	c := turn.ToolCalls[0]
	if c.Name != "Read" || c.Intent != "读一下" || c.Status != callSuccess {
		t.Fatalf("调用信息不对: %+v", c)
	}
	if !strings.Contains(c.Output, "package main") {
		t.Fatalf("结果没挂上: %+v", c)
	}
	// tool 消息本身不再单独出现
	for _, m := range got {
		if m.Role == domain.RoleTool {
			t.Fatalf("工具结果不该单独成一条: %+v", m)
		}
	}
	if got[2].Reasoning != "先看看入口" || got[2].Content != "看完了，是个 main 包" {
		t.Fatalf("最后一轮回答不对: %+v", got[2])
	}
}

// 失败的调用：结果以"执行失败: "开头就是失败，原因剥掉前缀单独给一份
func TestHistoryFailedCall(t *testing.T) {
	s := &domain.Session{}
	s.AppendUser("跑个命令")
	call := domain.ToolCall{Id: "c1", Name: "Bash", Arguments: `{"intent":"编译","command":"go build"}`}
	s.AppendAssistant("", "", []domain.ToolCall{call})
	s.AppendToolResult(call, "执行失败: 找不到 go 命令")

	got := historyFromSession(s)
	c := got[1].ToolCalls[0]
	if c.Status != callFailed {
		t.Fatalf("该标成失败: %+v", c)
	}
	if c.FailMsg != "找不到 go 命令" {
		t.Fatalf("失败原因没剥干净: %q", c.FailMsg)
	}

	// 命令自己退出码非零不算失败：结果以 exit_code 开头
	s2 := &domain.Session{}
	s2.AppendUser("跑个命令")
	call2 := domain.ToolCall{Id: "c2", Name: "Bash", Arguments: `{"command":"false"}`}
	s2.AppendAssistant("", "", []domain.ToolCall{call2})
	s2.AppendToolResult(call2, "exit_code: 1\n")
	if got := historyFromSession(s2); got[1].ToolCalls[0].Status != callSuccess {
		t.Fatalf("退出码非零不该算调用失败: %+v", got[1].ToolCalls[0])
	}
}

// 被中断的那次调用没有结果，标成"未完成"
func TestHistoryIncompleteCall(t *testing.T) {
	s := &domain.Session{}
	s.AppendUser("读一下")
	call := domain.ToolCall{Id: "c1", Name: "Read", Arguments: `{"path":"a.go"}`}
	s.AppendAssistant("", "", []domain.ToolCall{call})

	got := historyFromSession(s)
	c := got[1].ToolCalls[0]
	if c.Status != callIncomplete || c.Output != "" {
		t.Fatalf("没结果的调用该标未完成: %+v", c)
	}
}

// 压缩过的老历史：摘要放最前面，被摘要覆盖的消息不再显示
func TestHistorySummary(t *testing.T) {
	s := &domain.Session{}
	s.AppendUser("很早以前说的")
	s.AppendAssistant("很早以前的回答", "", nil)
	s.AppendUser("最近说的")
	s.AppendAssistant("最近的回答", "", nil)
	s.Summary = "之前聊了聊项目结构"
	s.SummaryUpTo = 2

	got := historyFromSession(s)
	if len(got) != 3 || got[0].Role != "summary" {
		t.Fatalf("摘要该排在最前面: %+v", got)
	}
	if !strings.Contains(got[0].Content, "之前聊了聊项目结构") {
		t.Fatalf("摘要内容不对: %q", got[0].Content)
	}
	for _, m := range got {
		if strings.Contains(m.Content, "很早以前") {
			t.Fatalf("被摘要覆盖的消息不该再显示: %+v", m)
		}
	}

	// 摘要指针越界（会话被改小了）时不该 panic
	s.SummaryUpTo = 999
	if got := historyFromSession(s); len(got) != 1 {
		t.Fatalf("越界时只剩摘要那一条: %+v", got)
	}
}

// 图片转成能直接塞进 src 的 data: 地址；空图跳过
func TestHistoryImages(t *testing.T) {
	img, err := domain.NewImage([]byte("\x89PNG\r\n\x1a\n 假装是图"))
	if err != nil {
		t.Fatalf("造图片失败: %v", err)
	}
	s := &domain.Session{}
	s.AppendUser("看看这张", img, domain.Image{})

	got := historyFromSession(s)
	if len(got) != 1 || len(got[0].Images) != 1 {
		t.Fatalf("应该只有一张有效图片: %+v", got)
	}
	if !strings.HasPrefix(got[0].Images[0], "data:image/png;base64,") {
		t.Fatalf("图片地址不对: %q", got[0].Images[0])
	}
}

// 空会话不返回一堆空块
func TestHistoryEmpty(t *testing.T) {
	if got := historyFromSession(nil); got != nil {
		t.Fatalf("nil 会话该返回 nil: %+v", got)
	}
	if got := historyFromSession(&domain.Session{}); len(got) != 0 {
		t.Fatalf("空会话不该有内容: %+v", got)
	}
}
