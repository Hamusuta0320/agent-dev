package domain

import "context"

type Ctx struct {
	Prompt string
	// Images 是这次提问附上的图片，Loop 会连图片一起记进会话，再发给模型
	Images   []Image
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
	// ContextLimit 是模型的上下文窗口（token），只用来算"上下文已用百分比"；0 表示不知道
	ContextLimit int
	// Mode 是工作模式：只读时没有写类工具，编辑需授权时每次写操作都要先问用户
	Mode Mode
	// MaxSteps 是一次提问里最多跑多少轮工具调用，用来给模型死循环刹车；
	// 0 表示用 config.DefaultMaxSteps
	MaxSteps int
}
