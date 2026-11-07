package domain

import (
	"fmt"
	"math/rand/v2"
	"strings"
	"time"
)

// 会话消息的角色，和发给 provider 的 role 字段一一对应
const (
	RoleSystem    = "system"
	RoleUser      = "user"
	RoleAssistant = "assistant"
	RoleTool      = "tool"
)

// SessionMessage 是会话里的一条完整记录。
// 除了要发给模型的内容，还保留模型的思考过程、工具调用明细和工具结果，
// 这样会话文件既是上下文来源，也是排查问题的日志。
type SessionMessage struct {
	Role    string `json:"role"`
	Content string `json:"content,omitempty"`
	// Images 是用户提问里附的图片，跟着会话一起存下来，恢复会话后还能接着问
	Images []Image `json:"images,omitempty"`
	// Reasoning 是模型的思考内容，只做记录，不回传给模型
	Reasoning  string     `json:"reasoning,omitempty"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallId string     `json:"tool_call_id,omitempty"`
	// ToolName 是工具结果对应的工具名，方便直接看会话文件
	ToolName  string    `json:"tool_name,omitempty"`
	CreatedAt time.Time `json:"created_at"`
	// Compressed 表示这条工具结果太老，发给模型时只发截断版；Content 里仍是原文
	Compressed bool `json:"compressed"`
}

type Session struct {
	Id string `json:"id"`
	// Title 是第一轮聊完之后模型给起的一句话标题，/resume 列表里靠它认出是哪一次会话。
	// 老会话没有这个字段，读出来是空串。字段位置紧跟在 id 后面：列表只读文件头就能拿到它
	Title string `json:"title,omitempty"`
	// WorkDir 是会话开始时的工作目录
	WorkDir   string           `json:"work_dir,omitempty"`
	CreatedAt time.Time        `json:"created_at"`
	UpdatedAt time.Time        `json:"updated_at"`
	Messages  []SessionMessage `json:"messages"`
	// Summary 是较早历史的摘要。有值时 History 会把它作为一条 user 消息放在最前面，
	// 而 Messages[:SummaryUpTo] 不再回传给模型（原文仍留在文件里）
	Summary string `json:"summary,omitempty"`
	// SummaryUpTo 表示 Messages[:SummaryUpTo] 已经被 Summary 覆盖
	SummaryUpTo int `json:"summary_up_to,omitempty"`
	// LastUsage 是最近一次请求模型的上下文占用。它本来只是运行时的一份快照
	// （见 webui 里那条用量条），但那样一重启就没了——页面上"上下文 xx / xxx"
	// 整块消失，看着像功能坏了。落进会话文件，换个进程也还看得见上次占了多少。
	// 老会话没有这个字段，读出来是 nil
	LastUsage *ContextUsage `json:"last_usage,omitempty"`
}

// NewSession 起一个新会话。id 里带时间戳，按文件名排序就是会话的先后顺序
func NewSession(workDir string) *Session {
	now := time.Now()
	return &Session{
		Id:        fmt.Sprintf("%s-%04d", now.Format("20060102-150405"), rand.IntN(10000)),
		WorkDir:   workDir,
		CreatedAt: now,
		UpdatedAt: now,
	}
}

// Append 往会话末尾追加一条消息，顺便维护时间戳
func (s *Session) Append(msg SessionMessage) {
	if msg.CreatedAt.IsZero() {
		msg.CreatedAt = time.Now()
	}
	s.Messages = append(s.Messages, msg)
	if msg.CreatedAt.After(s.UpdatedAt) {
		s.UpdatedAt = msg.CreatedAt
	}
}

// AppendUser 记录用户的一条提问，images 是随提问附上的图片（可以没有）
func (s *Session) AppendUser(content string, images ...Image) {
	s.Append(SessionMessage{Role: RoleUser, Content: content, Images: images})
}

// AppendAssistant 记录模型这一轮的回答：思考过程、正文和工具调用
func (s *Session) AppendAssistant(content, reasoning string, calls []ToolCall) {
	s.Append(SessionMessage{
		Role:      RoleAssistant,
		Content:   content,
		Reasoning: reasoning,
		ToolCalls: calls,
	})
}

// AppendToolResult 记录一次工具执行的结果，ToolCallId 要和模型的调用对得上
func (s *Session) AppendToolResult(call ToolCall, content string) {
	s.Append(SessionMessage{
		Role:       RoleTool,
		Content:    content,
		ToolCallId: call.Id,
		ToolName:   call.Name,
	})
}

// missingToolResult 是顶替"结果丢了"的那条占位正文。话说得直白些，
// 模型才知道别指望那个结果、该重新调一次
const missingToolResult = "错误: 这次工具调用没有留下结果（那一轮被中断或服务重启，结果丢了）。要确认结果，请重新调用一次。"

// History 把会话整理成发给模型的 messages。
// 思考内容不回传（有的 provider 不接受 reasoning 字段），空白的 assistant 消息也跳过；
// 只有图片没有文字的提问要留着，那也是一条正经提问。
// 被摘要覆盖的老消息不再回传，只发一条摘要；标记了 Compressed 的工具结果发截断版。
//
// 工具调用和它的结果在这里重新配对：assistant 的每个 tool_call 后面一定紧跟一条
// 对应的 tool 消息，顺序照 tool_calls 来。接口是按顺序认的（不认 id 对不对），
// 少一条就整条请求被拒：
//
//	An assistant message with 'tool_calls' must be followed by tool messages
//	responding to each 'tool_call_id'. (insufficient tool messages following tool_calls message)
//
// 中间态是会落盘的：core 记完 assistant 就落一次盘（好让人刷新页面看到进度），
// 那时工具还没跑完、结果还没进会话。进程正好在这中间被杀（重启、崩溃），
// 会话就永久停在"有调用没有结果"上，之后每一轮都 400，这条会话再也用不了。
// 所以缺的结果在这里补一条占位，而不是把整条会话判死。
func (s *Session) History() []AIChatMessage {
	messages := make([]AIChatMessage, 0, len(s.Messages)+1)
	if summary := strings.TrimSpace(s.Summary); summary != "" {
		messages = append(messages, AIChatMessage{
			Role:    RoleUser,
			Content: "[更早对话的摘要]\n" + summary,
		})
	}
	start := min(max(s.SummaryUpTo, 0), len(s.Messages))
	tail := s.Messages[start:]

	// 结果按 tool_call_id 归位，同一个 id 出现多次只认第一条
	results := make(map[string]SessionMessage, len(tail))
	for _, m := range tail {
		if m.Role == RoleTool && m.ToolCallId != "" {
			if _, dup := results[m.ToolCallId]; !dup {
				results[m.ToolCallId] = m
			}
		}
	}

	for _, m := range tail {
		switch m.Role {
		case RoleTool:
			// 结果一律在遇到它那次调用时发出去。走到这儿说明没人认领它：
			// 要么它的调用已经被摘要覆盖，要么本来就是坏数据。孤立的 tool 消息
			// 会被接口同样拒掉，宁可不发
			continue
		case RoleAssistant:
			if m.Content == "" && len(m.ToolCalls) == 0 {
				continue
			}
			messages = append(messages, AIChatMessage{
				Role:      RoleAssistant,
				Content:   m.Content,
				ToolCalls: m.ToolCalls,
			})
			for _, c := range m.ToolCalls {
				result, ok := results[c.Id]
				if !ok {
					messages = append(messages, AIChatMessage{
						Role:       RoleTool,
						Content:    missingToolResult,
						ToolCallId: c.Id,
					})
					continue
				}
				messages = append(messages, AIChatMessage{
					Role:       RoleTool,
					Content:    toolResultContent(result),
					ToolCallId: c.Id,
				})
			}
		default:
			if m.Role == RoleUser && m.Content == "" && len(m.Images) == 0 {
				continue
			}
			messages = append(messages, AIChatMessage{
				Role:       m.Role,
				Content:    m.Content,
				ToolCallId: m.ToolCallId,
				Images:     m.Images,
			})
		}
	}
	return messages
}

// toolResultContent 是一条工具结果发给模型时的正文：压缩过的只发截断版，
// 原文一直留在会话文件里
func toolResultContent(m SessionMessage) string {
	if m.Compressed {
		return TruncateToolContent(m.Content)
	}
	return m.Content
}

// LastAssistant 返回最后一条有正文的模型回答，用来回答"上一轮说了什么"
func (s *Session) LastAssistant() string {
	for i := len(s.Messages) - 1; i >= 0; i-- {
		if s.Messages[i].Role == RoleAssistant && s.Messages[i].Content != "" {
			return s.Messages[i].Content
		}
	}
	return ""
}
