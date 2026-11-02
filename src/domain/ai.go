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
)

type StreamEvent struct {
	Kind StreamEventKind
	Text string
}
