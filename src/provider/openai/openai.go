package openai

type AIChatMessage struct {
	Role       string     `json:"role"`
	Content    string     `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallId string     `json:"tool_call_id,omitempty"`
}

type FunctionDefinition struct {
	Name        string         `json:"name"`
	Description string         `json:"description"`
	Parameters  map[string]any `json:"parameters"`
}

type ToolSpec struct {
	Type     string             `json:"type"`
	Function FunctionDefinition `json:"function"`
}

type AIRequestMessage struct {
	Model         string          `json:"model"`
	Messages      []AIChatMessage `json:"messages"`
	Tools         []ToolSpec      `json:"tools,omitempty"`
	ToolChoice    string          `json:"tool_choice,omitempty"` // auto,none,required
	Stream        bool            `json:"stream"`
	StreamOptions *StreamOptions  `json:"stream_options,omitempty"`
}

// StreamOptions 只为了拿到用量：流式接口默认不报 usage，要多要一次
type StreamOptions struct {
	IncludeUsage bool `json:"include_usage"`
}

type AIResponseChoice struct {
	Message      AIResponseChoiceMessage `json:"message"`
	FinishReason string                  `json:"finish_reason"`
}

type ToolCallFunction struct {
	Name      string `json:"name"`
	Arguments string `json:"arguments"`
}

type ToolCall struct {
	Id       string           `json:"id"`
	Type     string           `json:"type"`
	Function ToolCallFunction `json:"function"`
}

type AIResponseChoiceMessage struct {
	Content   string     `json:"content"`
	Role      string     `json:"role"`
	ToolCalls []ToolCall `json:"tool_calls"`
}

type AIResponseMessage struct {
	Choices []AIResponseChoice `json:"choices"`
}

type AIStreamResponse struct {
	Choices []struct {
		Delta struct {
			Content          string          `json:"content"`
			ReasoningContent string          `json:"reasoning_content"`
			ToolCalls        []ToolCallDelta `json:"tool_calls"`
		} `json:"delta"`
		FinishReason string `json:"finish_reason"`
	}
	// Usage 只在流末尾带 include_usage 的那一帧里有，其余帧是空的
	Usage *Usage `json:"usage"`
	Error *struct {
		Message string `json:"message"`
	} `json:"error"`
}

// Usage 是响应里报的 token 用量
type Usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	TotalTokens      int `json:"total_tokens"`
}

type ToolCallDelta struct {
	Index    int    `json:"index"`
	Id       string `json:"id"`
	Type     string `json:"type"`
	Function struct {
		Name      string `json:"name"`
		Arguments string `json:"arguments"`
	} `json:"function"`
}
