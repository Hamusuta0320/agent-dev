package main

import (
	"fmt"
	"strings"

	"hamusuta/agent-dev/src/domain"
	"hamusuta/agent-dev/src/session"
)

// command 是本地命令，不发给模型
type command int

const (
	cmdNone command = iota
	cmdHelp
	cmdNew
	cmdSession
	cmdExit
)

// parseCommand 认出输入里的本地命令，认不出就当成普通提问
func parseCommand(input string) command {
	fields := strings.Fields(input)
	if len(fields) == 0 {
		return cmdNone
	}
	switch fields[0] {
	case "/help":
		return cmdHelp
	case "/new":
		return cmdNew
	case "/session":
		return cmdSession
	case "/exit", "/quit", "exit", "quit":
		return cmdExit
	}
	return cmdNone
}

func helpText() string {
	return strings.Join([]string{
		bannerStyle.Render("命令"),
		"  /new      开一个新会话",
		"  /session  看当前会话 id 和消息条数",
		"  /help     看这个帮助",
		"  /exit     退出（也可以 Ctrl+D）",
		"",
		bannerStyle.Render("输入"),
		"  Enter 发送，Shift+Enter 换行（Ctrl+J 也行），粘贴多行文本会当成一次输入",
	}, "\n")
}

func sessionText(cur *domain.Session, store *session.Store) string {
	dir := ""
	if store != nil {
		dir = store.Dir
	}
	return fmt.Sprintf("会话 %s，共 %d 条消息，目录 %s", cur.Id, len(cur.Messages), dir)
}
