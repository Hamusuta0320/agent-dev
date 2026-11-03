package domain

import (
	"strings"
	"testing"
)

func TestTruncateToolContent(t *testing.T) {
	if got := TruncateToolContent(""); got != "" {
		t.Fatalf("空内容应该原样返回，实际 %q", got)
	}

	short := "第一行\n第二行\n第三行"
	if got := TruncateToolContent(short); got != short {
		t.Fatalf("不超过 4 行不该改写，实际 %q", got)
	}

	// 6 行：头两行、尾两行留下，中间两行换成说明
	got := TruncateToolContent("l1\nl2\nl3\nl4\nl5\nl6")
	for _, want := range []string{"l1", "l2", "l5", "l6", "省略 2 行"} {
		if !strings.Contains(got, want) {
			t.Fatalf("压缩结果里应该有 %q: %q", want, got)
		}
	}
	for _, dropped := range []string{"l3", "l4"} {
		if strings.Contains(got, dropped) {
			t.Fatalf("被省略的行不该出现 %q: %q", dropped, got)
		}
	}

	// 单行超长：前 200 字符 + 原长度说明
	long := strings.Repeat("a", 250)
	got = TruncateToolContent(long)
	if !strings.HasPrefix(got, strings.Repeat("a", 200)) {
		t.Fatalf("超长行应该保留前 200 字符: %q", got)
	}
	if !strings.Contains(got, "本行共 250 字符") {
		t.Fatalf("超长行应该说明原长度: %q", got)
	}

	// 恰好 200 字符不该截
	exact := strings.Repeat("b", compactLineRunes)
	if got := TruncateToolContent(exact); got != exact {
		t.Fatalf("恰好 200 字符不该改写: %q", got)
	}
}

// 摘要覆盖的消息不再发给模型，压缩过的工具结果发截断版，原文留在会话里
func TestHistoryAppliesCompaction(t *testing.T) {
	original := "l1\nl2\nl3\nl4\nl5\nl6"
	s := &Session{
		Messages: []SessionMessage{
			{Role: RoleUser, Content: "老问题"},
			{Role: RoleTool, Content: original, ToolName: "Read", Compressed: true},
			{Role: RoleUser, Content: "新问题"},
		},
		Summary:     "之前改过 a.go",
		SummaryUpTo: 1,
	}

	history := s.History()
	if len(history) != 3 {
		t.Fatalf("应该有 摘要 + 工具结果 + 新问题 3 条，实际 %d: %+v", len(history), history)
	}
	if history[0].Role != RoleUser || !strings.Contains(history[0].Content, "[更早对话的摘要]") ||
		!strings.Contains(history[0].Content, "之前改过 a.go") {
		t.Fatalf("摘要应该作为第一条 user 消息: %+v", history[0])
	}
	if history[1].Role != RoleTool || !strings.Contains(history[1].Content, "省略 2 行") {
		t.Fatalf("压缩过的工具结果应该发截断版: %+v", history[1])
	}
	if history[2].Content != "新问题" {
		t.Fatalf("最近的消息要保持原文: %+v", history[2])
	}
	if s.Messages[1].Content != original {
		t.Fatalf("会话里的原文不该被改写: %q", s.Messages[1].Content)
	}
}
