package core

import (
	"encoding/json"
	"sort"

	"hamusuta/agent-dev/src/domain"
)

// estimateTokens 是本地估算 token 数的土办法。接口只回报 prompt 总数，
// 想知道系统提示词、工具定义、对话消息各占多少，只能自己按文本估个比例，
// 再拿响应里的总数去校准。ASCII 按 4 字符 1 token，非 ASCII（中文等）按 1 字 0.6 token。
func estimateTokens(s string) int {
	var ascii, wide int
	for _, r := range s {
		if r < 128 {
			ascii++
			continue
		}
		wide++
	}
	return (ascii+3)/4 + wide*3/5
}

// estimateMessages 估一批对话消息（不含系统提示词）的占用
func estimateMessages(messages []domain.AIChatMessage) int {
	total := 0
	for _, m := range messages {
		total += estimateTokens(m.Role) + estimateTokens(m.Content) + estimateTokens(m.ToolCallId)
		for _, call := range m.ToolCalls {
			total += estimateTokens(call.Name) + estimateTokens(call.Arguments)
		}
		for _, img := range m.Images {
			if !img.Empty() {
				total += imageTokens
			}
		}
	}
	return total
}

// imageTokens 是估算一张图片占用的 token 数。接口是按像素分块算的，
// 本地只知道字节数，差得远；这里按"一张普通截图一千上下"给个固定值，
// 足够让界面上那根百分比条不至于漏掉图片这一块
const imageTokens = 1000

// estimateToolSpecs 估工具定义（名字、说明、参数 schema）的占用
func estimateToolSpecs(specs []domain.ToolSpec) int {
	total := 0
	for _, s := range specs {
		total += estimateTokens(s.Name) + estimateTokens(s.Description)
		if params, err := json.Marshal(s.Parameters); err == nil {
			total += estimateTokens(string(params))
		}
	}
	return total
}

// usageTracker 维护上下文占用里那几项的估算值，避免每轮都从头数一遍：
// 工具定义一整轮都不变，只算一次；对话消息每追加一条就累加一条的量。
// 估算只用来把响应里的 prompt tokens 拆成三项，不追求精确。
type usageTracker struct {
	toolTokens    int
	messageTokens int
}

// newUsageTracker 起一个估算器：工具定义当场算好，历史消息（比如恢复的老会话）先数一遍
func newUsageTracker(specs []domain.ToolSpec, session *domain.Session) *usageTracker {
	t := &usageTracker{toolTokens: estimateToolSpecs(specs)}
	if session != nil {
		t.messageTokens = estimateMessages(session.History())
	}
	return t
}

// trackAssistant 记下刚追加的模型回答。过滤规则和 Session.History 保持一致：
// 既没正文也没工具调用的 assistant 消息不会发给模型，也就不该算进占用
func (t *usageTracker) trackAssistant(content string, calls []domain.ToolCall) {
	if content == "" && len(calls) == 0 {
		return
	}
	t.messageTokens += estimateMessages([]domain.AIChatMessage{
		{Role: domain.RoleAssistant, Content: content, ToolCalls: calls},
	})
}

// trackToolResult 记下刚追加的工具结果
func (t *usageTracker) trackToolResult(call domain.ToolCall, content string) {
	t.messageTokens += estimateMessages([]domain.AIChatMessage{
		{Role: domain.RoleTool, Content: content, ToolCallId: call.Id},
	})
}

// newContextUsage 把响应里的 prompt tokens 当总数，按本地估算的比例拆成
// 系统提示词、工具定义、对话消息三项，三项之和正好等于 PromptTokens。
// 响应里没报用量时退化成纯估算，这时候 PromptTokens 也是估的。
// limit 是模型的上下文窗口，只用于算已用百分比，<=0 表示不知道。
func newContextUsage(systemPrompt string, toolTokens, messageTokens int, usage domain.Usage, limit int) domain.ContextUsage {
	system := estimateTokens(systemPrompt)
	prompt := usage.PromptTokens
	if prompt <= 0 {
		prompt = usage.TotalTokens - usage.CompletionTokens
	}
	if prompt <= 0 {
		prompt = system + toolTokens + messageTokens
	}
	parts := splitByWeight(prompt, system, toolTokens, messageTokens)
	return domain.ContextUsage{
		SystemTokens:     parts[0],
		ToolTokens:       parts[1],
		MessageTokens:    parts[2],
		PromptTokens:     prompt,
		CompletionTokens: usage.CompletionTokens,
		ContextLimit:     limit,
	}
}

// splitByWeight 按权重把 total 分成 len(weights) 份，
// 用最大余数法收拾零头，保证各份之和正好等于 total。
func splitByWeight(total int, weights ...int) []int {
	out := make([]int, len(weights))
	sum := 0
	for _, w := range weights {
		sum += w
	}
	if total <= 0 || sum <= 0 {
		return out
	}

	type remain struct {
		idx  int
		frac float64
	}
	rems := make([]remain, len(weights))
	left := total
	for i, w := range weights {
		exact := float64(total) * float64(w) / float64(sum)
		out[i] = int(exact)
		left -= out[i]
		rems[i] = remain{idx: i, frac: exact - float64(out[i])}
	}

	// 剩下的零头从余数大的那几项补，剩下的几个按顺序发，总够分完
	sort.SliceStable(rems, func(a, b int) bool { return rems[a].frac > rems[b].frac })
	for i := 0; i < left; i++ {
		out[rems[i%len(rems)].idx]++
	}
	return out
}
