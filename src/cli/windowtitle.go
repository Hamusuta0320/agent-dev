package main

import "strings"

// windowAppName 是窗口标题尾巴上的程序名。标题长的会话会把窗口标题写满，
// 后面缀上程序名，同时开几个终端时还能认出哪个是它
const windowAppName = "agent-dev"

// windowTitle 把会话标题收拾成窗口标题：还没起标题的会话（老会话、刚开的新会话）
// 只留程序名，免得窗口上继续挂着上一个会话的名字
func windowTitle(sessionTitle string) string {
	// 标题里混进控制字符会把设置标题的转义序列截断，剩下的字节就漏到屏幕上了
	title := strings.Map(func(r rune) rune {
		if r < 0x20 || r == 0x7f {
			return -1
		}
		return r
	}, strings.TrimSpace(sessionTitle))
	if title == "" {
		return windowAppName
	}
	return title + " - " + windowAppName
}
