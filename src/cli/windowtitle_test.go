package main

import "testing"

// 会话标题就是窗口标题：前面是标题，尾巴上缀程序名，好和别的程序开在同一栏里分开
func TestWindowTitleUsesSessionTitle(t *testing.T) {
	if got := windowTitle("给窗口标题同步会话名"); got != "给窗口标题同步会话名 - agent-dev" {
		t.Fatalf("窗口标题不对: %q", got)
	}
}

// 还没起标题的会话（老会话、刚开的新会话）只留程序名，
// 免得窗口上继续挂着上一个会话的名字
func TestWindowTitleFallsBackToAppName(t *testing.T) {
	for _, in := range []string{"", "   ", "\t\n"} {
		if got := windowTitle(in); got != "agent-dev" {
			t.Fatalf("标题是 %q 时该退回程序名，实际 %q", in, got)
		}
	}
}

// 标题里混进控制字符会把设置标题的转义序列截断，剩下的字节漏到屏幕上
func TestWindowTitleDropsControlChars(t *testing.T) {
	if got := windowTitle(" 改个\x1b]0;伪造的标题\x07 bug "); got != "改个]0;伪造的标题 bug - agent-dev" {
		t.Fatalf("控制字符没清干净: %q", got)
	}
}
