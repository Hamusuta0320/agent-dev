package domain

import "context"

type Provider interface {
	Chat(ctx context.Context, messages []AIChatMessage, tools []ToolSpec, onDelta func(string)) (ChatResult, error)
}
