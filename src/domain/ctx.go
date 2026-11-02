package domain

import "context"

type Ctx struct {
	Prompt   string
	Provider Provider
	Tools    map[string]Tool
	Context  context.Context
	// WorkDir 是本次会话的工作目录，只用于写进系统提示词；为空时取进程当前目录
	WorkDir string
	// Shell 是 Bash 工具实际使用的 shell 名字，只用于写进系统提示词
	Shell string
	// Session 是这次对话的会话，历史消息会带进每一次请求；为空时 Loop 会新建一个空会话
	Session *Session
	// Emit 接收运行过程中的进度事件，CLI 层用它来渲染界面；为空时 core 直接往标准输出打纯文本
	Emit EmitFunc
}
