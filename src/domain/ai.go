package domain

import (
	"context"
	"encoding/json"
)

type Tool interface {
	Name() string
	Description() string
	Schema() map[string]any
	// Mutating 说明这个工具会不会改动磁盘上的东西（写文件、跑命令）。
	// 只读的工作模式下不发给模型，需要授权的模式下执行前要先问用户。
	Mutating() bool
	// Execute 执行一次调用。ctx 被取消（用户按了 Ctrl+C）时要尽快停下来，
	// 能中断的（Bash 的子进程）要真的杀掉，别留一摊子进程在后台跑。
	Execute(ctx context.Context, rawArgs json.RawMessage) (string, error)
}

type ToolSpec struct {
	Name        string
	Description string
	Parameters  map[string]any
}

type ToolCall struct {
	Id        string
	Name      string
	Arguments string
}

type AIChatMessage struct {
	Role       string
	Content    string
	ToolCalls  []ToolCall
	ToolCallId string
	// Images 是随这条消息一起发给模型的图片，目前只有用户的提问会带。
	// 带图的消息发给接口时 content 会变成"文字 + 图片"的数组，没有图还是老样子的字符串
	Images []Image
}

type ChatResult struct {
	Content   string
	ToolCalls []ToolCall
	Reasoning string
	// Usage 是这次请求的 token 用量，由 provider 从响应里原样带回来；
	// 有的接口流式不报用量，那就是零值
	Usage Usage
}

// Usage 是响应里报的 token 用量
type Usage struct {
	PromptTokens     int
	CompletionTokens int
	TotalTokens      int
}

type StreamEventKind int

const (
	StreamContent StreamEventKind = iota
	StreamReasoning
	// StreamRetry 表示这次请求失败了、正在重试，Text 是给用户看的说明。
	// provider 只在这一轮还没吐出任何内容时才重试（已经出过字就没法撤回了），
	// 所以收到它的一方不用清空什么
	StreamRetry
)

type StreamEvent struct {
	Kind StreamEventKind
	Text string
}
