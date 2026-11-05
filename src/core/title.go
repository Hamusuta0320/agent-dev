package core

import (
	"context"
	"strings"
	"time"

	"hamusuta/agent-dev/src/domain"
)

const (
	// titleMaxRunes 是标题的长度上限（字符）。提示词里让模型写到 20 字以内，
	// 这里留点余量；再长就截断，得能一行放进 /resume 列表
	titleMaxRunes = 24
	// titleSourceRunes 是拟标题时原文的单段上限。标题请求本身不该花多少钱
	titleSourceRunes = 600
	// titleTimeout 是拟标题最多等多久。这只是给会话搭个名字，
	// 慢到让人以为这一轮还没聊完就不值了
	titleTimeout = 20 * time.Second
)

// titleSystemPrompt 是让模型给会话起标题时用的指令。只输出标题正文，方便直接存进会话。
const titleSystemPrompt = `你在给一段编程助手和用户的会话起标题，方便以后在会话列表里认出是哪一次。
要求：
- 说清楚这次在聊什么、要做什么，比如"给恢复会话列表加标题"；
- 用中文，不超过 20 个字，不要引号、书名号、句号，也不要"会话""讨论"这类废话；
- 只输出标题本身，不要解释。`

// ensureTitle 给还没有标题的会话起一个标题，已经有了就什么也不做。
// 第一轮聊完之后调一次：/resume 列表里只有一串时间戳的话，谁也认不出那是哪一次会话。
// 拟标题失败（没配模型、超时、模型返回空）不算错误，退回用用户的第一句提问凑一个，
// 免得为了一个标题把这一轮的收尾搞成失败。
func ensureTitle(ctx domain.Ctx, session *domain.Session) {
	if session == nil || strings.TrimSpace(session.Title) != "" {
		return
	}
	user, reply := titleSource(session)
	if user == "" {
		return
	}
	title := askTitle(ctx, user, reply)
	if title == "" {
		title = fallbackTitle(user)
	}
	if title == "" {
		return
	}
	session.Title = title

	emit := ctx.Emit
	if emit == nil {
		emit = consoleEmit()
	}
	emit(domain.Event{Kind: domain.EventTitle, Text: title})
}

// titleSource 挑出拟标题要用的原文：用户的第一句提问，加上模型对它的那段回答。
// 提问本身就够拟标题了，带上回答是为了"继续""按你说的改"这种单看认不出聊什么的提问。
func titleSource(session *domain.Session) (user, reply string) {
	for _, msg := range session.Messages {
		switch msg.Role {
		case domain.RoleUser:
			if user == "" {
				user = clipText(msg.Content, titleSourceRunes)
			}
		case domain.RoleAssistant:
			if user != "" && reply == "" {
				reply = clipText(msg.Content, titleSourceRunes)
			}
		}
	}
	return user, reply
}

// askTitle 把这段原文发给模型拟个标题。任何失败都返回空串，由调用方兜底
func askTitle(ctx domain.Ctx, user, reply string) string {
	if ctx.Provider == nil {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("用户: " + user + "\n")
	if reply != "" {
		sb.WriteString("助手: " + reply + "\n")
	}
	// 这一轮可能已经被用户中断了，别让标题请求赖着不走
	c := context.Background()
	if ctx.Context != nil {
		c = ctx.Context
	}
	c, cancel := context.WithTimeout(c, titleTimeout)
	defer cancel()

	res, err := ctx.Provider.Chat(c, []domain.AIChatMessage{
		{Role: domain.RoleSystem, Content: titleSystemPrompt},
		{Role: domain.RoleUser, Content: sb.String()},
	}, nil, nil)
	if err != nil {
		return ""
	}
	return cleanTitle(res.Content)
}

// cleanTitle 把模型返回的内容收拾成一行能直接用的标题：
// 只取第一行，去掉引号、"标题："这类前缀和收尾标点，太长就截断
func cleanTitle(s string) string {
	s = strings.TrimSpace(s)
	if line, _, ok := strings.Cut(s, "\n"); ok {
		s = line
	}
	s = trimTitleEdges(s)
	for _, prefix := range []string{"会话标题", "标题"} {
		if rest, ok := cutPrefixPunct(s, prefix); ok {
			s = trimTitleEdges(rest)
			break
		}
	}
	if runes := []rune(s); len(runes) > titleMaxRunes {
		s = strings.TrimSpace(string(runes[:titleMaxRunes]))
	}
	// 模型说不出个所以然时宁可当作没给，让调用方用提问兜底
	switch s {
	case "", "无", "没有", "暂无", "未知", "标题", "会话标题":
		return ""
	}
	return s
}

// trimTitleEdges 去掉标题两头那些模型爱加的标点和装饰
func trimTitleEdges(s string) string {
	const edges = "。.,，、:：;；!！?？\"'“”‘’《》【】[]()（）*#-· \t"
	return strings.TrimFunc(s, func(r rune) bool { return strings.ContainsRune(edges, r) })
}

// cutPrefixPunct 认"标题：xxx"这种写法：前缀后面必须跟一个分隔符才算引导语，
// 否则"标题栏怎么改"这种正经标题会被切掉开头
func cutPrefixPunct(s, prefix string) (string, bool) {
	rest, ok := strings.CutPrefix(s, prefix)
	if !ok {
		return "", false
	}
	trimmed := strings.TrimLeft(rest, ":：-— ")
	if trimmed == rest {
		return "", false
	}
	return trimmed, trimmed != ""
}

// fallbackTitle 是模型拟不出标题时的兜底：拿用户第一句提问压成一行。
// 不完美，但比列表里只有时间戳强
func fallbackTitle(user string) string {
	return clipText(strings.Join(strings.Fields(user), " "), titleMaxRunes)
}

// clipText 把一段文字压到 n 个字以内，多出来的用省略号收尾
func clipText(s string, n int) string {
	s = strings.TrimSpace(s)
	runes := []rune(s)
	if len(runes) <= n {
		return s
	}
	return strings.TrimSpace(string(runes[:n])) + "…"
}
