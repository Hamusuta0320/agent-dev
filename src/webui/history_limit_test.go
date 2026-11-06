package webui

import (
	"fmt"
	"strings"
	"testing"

	"hamusuta/agent-dev/src/domain"
)

// longSession 造一条"聊了很久"的会话：每轮一条提问 + 一条带工具调用的回答 + 一条工具结果
func longSession(rounds int) *domain.Session {
	s := domain.NewSession("/tmp/demo")
	for i := range rounds {
		s.AppendUser(fmt.Sprintf("第 %d 轮提问", i))
		call := domain.ToolCall{
			Id: fmt.Sprintf("call-%d", i), Name: "Bash",
			Arguments: `{"intent":"跑一下","command":"echo hi"}`,
		}
		s.AppendAssistant("", "", []domain.ToolCall{call})
		s.AppendToolResult(call, strings.Repeat("输出", 40))
	}
	return s
}

// limit 只取尾部：切会话时先渲染最近这些就够了，几百条一次 markdown 解析要 160ms
func TestHistoryLimitKeepsTail(t *testing.T) {
	s := longSession(20) // 60 条原始消息
	full, hasMore := historyFromSessionOpts(s, historyOptions{})
	if hasMore {
		t.Fatal("没给 limit 时不该说还有更早的")
	}

	tail, hasMore := historyFromSessionOpts(s, historyOptions{Limit: 9})
	if !hasMore {
		t.Fatal("裁掉了内容就必须说 has_more，否则用户以为会话只有这么点")
	}
	if len(tail) >= len(full) {
		t.Fatalf("limit 没起作用：裁后 %d 条，全量 %d 条", len(tail), len(full))
	}
	// 留下的必须是最后那几轮，不是开头那几轮
	last := tail[len(tail)-1]
	if last.Role != "assistant" {
		t.Fatalf("最后一条该是 assistant，实际 %+v", last)
	}
	// 最后一轮的提问也要在
	found := false
	for _, m := range tail {
		if m.Role == "user" && m.Content == "第 19 轮提问" {
			found = true
		}
	}
	if !found {
		t.Fatalf("尾部那段里没有最后一轮的提问: %+v", tail)
	}
}

// limit 比总条数还大时等于全给，且不该说"还有更早的"
func TestHistoryLimitLargerThanSession(t *testing.T) {
	s := longSession(2)
	all, hasMore := historyFromSessionOpts(s, historyOptions{Limit: 999})
	if hasMore {
		t.Fatal("limit 比总条数大时不该说还有更早的")
	}
	full, _ := historyFromSessionOpts(s, historyOptions{})
	if len(all) != len(full) {
		t.Fatalf("limit 超过总数时结果该和全量一样：%d vs %d", len(all), len(full))
	}
}

// 精简显示下工具输出不回传：实测一条聊得久的会话 43% 的字节是工具输出，
// 而精简模式下前端连那些 DOM 都不建，传过去纯浪费
func TestHistoryQuietDropsToolOutput(t *testing.T) {
	s := longSession(3)

	verbose, _ := historyFromSessionOpts(s, historyOptions{})
	quiet, _ := historyFromSessionOpts(s, historyOptions{Quiet: true})

	if len(verbose) != len(quiet) {
		t.Fatalf("两种模式的消息条数该一样：%d vs %d", len(verbose), len(quiet))
	}
	for i := range verbose {
		if len(verbose[i].ToolCalls) != len(quiet[i].ToolCalls) {
			t.Fatalf("第 %d 条的调用数不一样", i)
		}
		for j, c := range verbose[i].ToolCalls {
			if c.Output == "" {
				t.Fatalf("详细模式下输出不该是空的: %+v", c)
			}
			if q := quiet[i].ToolCalls[j]; q.Output != "" || q.FailMsg != "" {
				t.Fatalf("精简模式下不该回传输出和失败原因: %+v", q)
			}
		}
	}
	// 一行标题要用的东西必须留着，不然精简模式下连"跑了什么"都看不出来
	var first *call
	for i := range quiet {
		if len(quiet[i].ToolCalls) > 0 {
			first = &quiet[i].ToolCalls[0]
			break
		}
	}
	if first == nil {
		t.Fatal("一条工具调用都没有，用例构造错了")
	}
	if first.Name != "Bash" || first.Intent != "跑一下" || first.Status != callSuccess {
		t.Fatalf("精简模式下工具名/意图/状态要保留: %+v", *first)
	}
}

// 精简模式下"成功还是失败"仍然要判对：那个状态是用来给图标上色的
func TestHistoryQuietKeepsToolStatus(t *testing.T) {
	s := domain.NewSession("/tmp/demo")
	call := domain.ToolCall{Id: "c1", Name: "Write", Arguments: `{"intent":"写文件"}`}
	s.AppendUser("写一下")
	s.AppendAssistant("", "", []domain.ToolCall{call})
	s.AppendToolResult(call, "错误: 权限不够")

	msgs, _ := historyFromSessionOpts(s, historyOptions{Quiet: true})
	got := msgs[len(msgs)-1].ToolCalls[0]
	if got.Status != callFailed {
		t.Fatalf("精简模式下也要认出失败，实际状态 %q", got.Status)
	}
	if got.Output != "" {
		t.Fatalf("精简模式下不该带输出: %q", got.Output)
	}
}

// 摘要也算"上面还有内容"：被摘要覆盖的老消息虽然不显示，但它们确实存在过
func TestHistoryLimitWithSummary(t *testing.T) {
	s := longSession(5)
	s.Summary = "之前聊过一堆"
	s.SummaryUpTo = 5

	msgs, hasMore := historyFromSessionOpts(s, historyOptions{})
	if len(msgs) == 0 || msgs[0].Role != "summary" {
		t.Fatalf("摘要该排在最前面: %+v", msgs)
	}
	if hasMore {
		t.Fatal("没给 limit 时不该说还有更早的（摘要已经代表了更早的内容）")
	}
}
