package webui

import (
	"strings"
	"time"

	"hamusuta/agent-dev/src/domain"
	"hamusuta/agent-dev/src/tool"
)

// message 是给前端的一份会话历史。和 domain.SessionMessage 的差别在于：
// 工具的调用和结果在这一层就合并好了——历史里调用和结果本来是两条消息，
// 前端要按 tool_call_id 自己去配，不如在这儿配完，前端只管按顺序画。
type message struct {
	Role      string `json:"role"`
	Content   string `json:"content,omitempty"`
	Reasoning string `json:"reasoning,omitempty"`
	// Images 是提问里附的图，转成 data: 地址直接给前端当 src 用
	Images    []string  `json:"images,omitempty"`
	ToolCalls []call    `json:"tool_calls,omitempty"`
	CreatedAt time.Time `json:"created_at"`
}

// call 是一次工具调用连同它的结果。Output 为空、Status 是"未完成"时说明
// 那一轮被打断了，结果没回来。
type call struct {
	ID      string `json:"id"`
	Name    string `json:"name"`
	Intent  string `json:"intent,omitempty"`
	Args    string `json:"args,omitempty"`
	Status  string `json:"status"`
	Output  string `json:"output,omitempty"`
	FailMsg string `json:"fail_msg,omitempty"`
}

// 一次工具调用在历史里的状态。用 TUI 那三个词，前端配色也照它来
const (
	callSuccess    = "调用成功"
	callFailed     = "调用失败"
	callIncomplete = "未完成"
)

// historyFromSession 把会话消息整理成前端要的块。
// 规则和 TUI 重开会话时打历史的那套一致：提问、思考、回答按顺序留下，
// 工具结果挂到对应调用的下面；被摘要覆盖的老消息不再显示（它们已经并进摘要），
// 摘要本身作为一条提示放在最前面。
func historyFromSession(s *domain.Session) []message {
	if s == nil {
		return nil
	}
	out := make([]message, 0, len(s.Messages))
	if summary := strings.TrimSpace(s.Summary); summary != "" {
		out = append(out, message{
			Role:    "summary",
			Content: "更早的对话已压缩成摘要：\n" + summary,
		})
	}
	start := min(max(s.SummaryUpTo, 0), len(s.Messages))

	// outputs 按 tool_call_id 攒起来，等回头往调用上挂
	outputs := make(map[string]domain.SessionMessage, len(s.Messages))
	for _, m := range s.Messages[start:] {
		if m.Role == domain.RoleTool && m.ToolCallId != "" {
			outputs[m.ToolCallId] = m
		}
	}

	for _, m := range s.Messages[start:] {
		switch m.Role {
		case domain.RoleUser:
			if m.Content == "" && len(m.Images) == 0 {
				continue
			}
			out = append(out, message{
				Role:      "user",
				Content:   m.Content,
				Images:    imageURLs(m.Images),
				CreatedAt: m.CreatedAt,
			})
		case domain.RoleAssistant:
			if m.Content == "" && m.Reasoning == "" && len(m.ToolCalls) == 0 {
				continue
			}
			calls := make([]call, 0, len(m.ToolCalls))
			for _, c := range m.ToolCalls {
				calls = append(calls, callFromHistory(c, outputs[c.Id]))
			}
			out = append(out, message{
				Role:      "assistant",
				Content:   m.Content,
				Reasoning: m.Reasoning,
				ToolCalls: calls,
				CreatedAt: m.CreatedAt,
			})
		}
		// tool 消息本身不再单独输出：内容已经挂到上面那次调用上了
	}
	return out
}

// callFromHistory 把一次调用和它的结果拼成前端那份。
// 没有结果（那一轮被打断）标成"未完成"，和 TUI 里补的那句一样
func callFromHistory(c domain.ToolCall, result domain.SessionMessage) call {
	out := call{
		ID:     c.Id,
		Name:   c.Name,
		Intent: tool.CallIntent(c.Arguments),
		Args:   c.Arguments,
		Status: callIncomplete,
	}
	if result.Role == "" {
		return out
	}
	out.Output = result.Content
	if failedContent(result.Content) {
		out.Status = callFailed
		// 失败原因就是结果开头那句，剥掉前缀摆到前端单独一行
		out.FailMsg = strings.TrimSpace(strings.TrimPrefix(strings.TrimPrefix(
			result.Content, "执行失败: "), "错误: "))
		return out
	}
	out.Status = callSuccess
	return out
}

// failedContent 按结果的内容反推当时成没成功：core 失败时一定以这两句开头，
// 命令自己退出码非零不算失败（那种结果以 exit_code 开头）。
// 和 TUI 的 historyStatus 是同一条规则
func failedContent(content string) bool {
	return strings.HasPrefix(content, "错误: ") || strings.HasPrefix(content, "执行失败: ")
}

// imageURLs 把会话里的图片转成前端能直接塞进 src 的地址
func imageURLs(images []domain.Image) []string {
	if len(images) == 0 {
		return nil
	}
	out := make([]string, 0, len(images))
	for _, img := range images {
		if img.Empty() {
			continue
		}
		out = append(out, img.DataURL())
	}
	return out
}
