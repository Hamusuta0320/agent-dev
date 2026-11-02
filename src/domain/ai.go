package domain

import "encoding/json"

type Tool interface {
	Name() string
	Description() string
	Schema() map[string]any
	Execute(rawArgs json.RawMessage) (string, error)
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
}
