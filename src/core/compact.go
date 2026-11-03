package core

import (
	"fmt"
	"strings"

	"hamusuta/agent-dev/src/domain"
)

const (
	// compactThresholdPercent 是上下文占用到达窗口的百分之多少时开始压缩
	compactThresholdPercent = 80
	// keepRecentMessages 是压缩时原样保留的最近消息条数
	keepRecentMessages = 12
	// summaryMaxRunes 是单条消息送进摘要请求前的上限，免得摘要请求本身又撑爆上下文
	summaryMaxRunes = 2000
)

// summarySystemPrompt 是让模型整理旧对话时用的指令。只输出摘要正文，方便直接替换进会话。
const summarySystemPrompt = `你在压缩一段编程助手和用户的历史对话。请输出一段简洁的中文摘要，保留：
- 用户的目标、需求和明确说过的约束；
- 已经确认的事实、结论和决定；
- 改过或看过的文件路径，以及关键代码位置；
- 跑过的关键命令和结果；
- 还没完成的事。
不要编造没出现过的内容，不要写客套话。只输出摘要正文，控制在 500 字以内。`

// needCompact 判断当前占用是不是到了该压缩的地步。limit <= 0 表示不知道窗口大小，不压。
func needCompact(limit, used int) bool {
	return limit > 0 && used > 0 && used*100 >= limit*compactThresholdPercent
}

// compactContext 按两级压缩会话：先把较早的工具输出截断，还不够就把更早的消息摘要掉。
// 只改压缩标记和摘要，原文一直留在会话里，所以这里动过的会话照常落盘即可。
// forceSummary 表示上一轮接口报的用量已经超阈值——本地估算可能偏低，这时候别跳过摘要。
// 返回是否真的改动了会话。
func compactContext(ctx domain.Ctx, session *domain.Session, emit domain.EmitFunc, usage *usageTracker, systemTokens int, forceSummary bool) bool {
	if session == nil || usage == nil {
		return false
	}
	changed := false

	// 第一阶段：截断较早的工具输出。最近 keepRecentMessages 条保持原样
	truncBefore := max(len(session.Messages)-keepRecentMessages, 0)
	truncated := 0
	for i := range truncBefore {
		msg := &session.Messages[i]
		if msg.Role != domain.RoleTool || msg.Compressed {
			continue
		}
		msg.Compressed = true
		truncated++
	}
	if truncated > 0 {
		changed = true
		emitCompact(emit, fmt.Sprintf("已截断 %d 条较早的工具输出", truncated))
	}

	// 截断完还超阈值，才值得多花一次请求去做摘要
	if !forceSummary && !needCompact(ctx.ContextLimit, systemTokens+usage.toolTokens+estimateMessages(session.History())) {
		return changed
	}
	end := summaryBoundary(session, truncBefore)
	if end <= session.SummaryUpTo {
		return changed
	}

	from := session.SummaryUpTo
	emitCompact(emit, "上下文接近上限，正在把较早的对话整理成摘要…")
	summary, err := summarize(ctx, session, end)
	if err != nil {
		// 压缩失败不该让用户的这一轮直接失败，照常往下请求
		emitCompact(emit, "压缩失败: "+err.Error())
		return changed
	}
	if strings.TrimSpace(summary) == "" {
		emitCompact(emit, "压缩失败: 模型没有返回摘要")
		return changed
	}
	session.Summary = summary
	session.SummaryUpTo = end
	emitCompact(emit, fmt.Sprintf("已把 %d 条较早的消息整理成 %d 字摘要", end-from, len([]rune(summary))))
	return true
}

// summaryBoundary 找摘要和原文的分界：Messages[:end] 进摘要，Messages[end:] 原样留着。
// 两条硬约束：最近一轮用户的提问不能被摘掉；不能把工具结果和它对应的调用拆散。
func summaryBoundary(session *domain.Session, end int) int {
	end = min(end, len(session.Messages))
	for i := len(session.Messages) - 1; i >= 0; i-- {
		if session.Messages[i].Role == domain.RoleUser {
			end = min(end, i)
			break
		}
	}
	for end > 0 && end < len(session.Messages) && session.Messages[end].Role == domain.RoleTool {
		end--
	}
	return max(end, 0)
}

// summarize 把 Messages[SummaryUpTo:end] 连同已有摘要发给模型，换一份新摘要。
// 工具定义不用传（这轮不调用工具），也不需要流式回调。
func summarize(ctx domain.Ctx, session *domain.Session, end int) (string, error) {
	if ctx.Provider == nil {
		return "", fmt.Errorf("没有可用的模型")
	}
	from := min(max(session.SummaryUpTo, 0), end)

	var sb strings.Builder
	if prev := strings.TrimSpace(session.Summary); prev != "" {
		sb.WriteString("已有摘要（把新内容并进去，不要丢掉已经确认的结论）：\n")
		sb.WriteString(prev)
		sb.WriteString("\n\n")
	}
	sb.WriteString("需要并入摘要的较早对话：\n")
	for _, msg := range session.Messages[from:end] {
		writeTranscript(&sb, msg)
	}

	res, err := ctx.Provider.Chat(ctx.Context, []domain.AIChatMessage{
		{Role: domain.RoleSystem, Content: summarySystemPrompt},
		{Role: domain.RoleUser, Content: sb.String()},
	}, nil, nil)
	if err != nil {
		return "", err
	}
	return strings.TrimSpace(res.Content), nil
}

// writeTranscript 把一条会话消息写进摘要请求的正文；工具结果已经是压缩过的就直接用
func writeTranscript(sb *strings.Builder, msg domain.SessionMessage) {
	switch msg.Role {
	case domain.RoleUser:
		text := summaryText(msg.Content)
		if len(msg.Images) > 0 {
			text = fmt.Sprintf("[附 %d 张图片] %s", len(msg.Images), text)
		}
		fmt.Fprintf(sb, "用户: %s\n", text)
	case domain.RoleAssistant:
		if text := summaryText(msg.Content); text != "" {
			fmt.Fprintf(sb, "助手: %s\n", text)
		}
		for _, call := range msg.ToolCalls {
			fmt.Fprintf(sb, "工具调用 %s: %s\n", call.Name, summaryText(call.Arguments))
		}
	case domain.RoleTool:
		content := msg.Content
		if msg.Compressed {
			content = domain.TruncateToolContent(content)
		}
		fmt.Fprintf(sb, "工具结果 %s: %s\n", msg.ToolName, summaryText(content))
	}
}

// summaryText 把送进摘要请求的单条内容压到上限，避免摘要请求本身又超上下文
func summaryText(s string) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= summaryMaxRunes {
		return s
	}
	return string(runes[:summaryMaxRunes]) + "…"
}

// emitCompact 把压缩进展报给界面，没设置回调时什么也不做
func emitCompact(emit domain.EmitFunc, text string) {
	if emit != nil {
		emit(domain.Event{Kind: domain.EventCompact, Text: text})
	}
}
