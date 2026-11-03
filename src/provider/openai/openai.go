package openai

// AIChatMessage 的 Content 有两种形态：纯文本时是一个字符串，带图片时是一组
// ContentPart（openai 那套多模态写法）。用 any 让调用方决定塞哪种，
// 不带图的请求体和以前一模一样，字节不变，接口侧的上下文缓存也就不会失效。
type AIChatMessage struct {
	Role       string     `json:"role"`
	Content    any        `json:"content"`
	ToolCalls  []ToolCall `json:"tool_calls,omitempty"`
	ToolCallId string     `json:"tool_call_id,omitempty"`
}

// ContentPart 是带图片的消息里 content 数组的一段：一段文字，或者一张图片
type ContentPart struct {
	Type     string    `json:"type"`
	Text     string    `json:"text,omitempty"`
	ImageURL *ImageURL `json:"image_url,omitempty"`
}

// ImageURL 是图片段的内容。URL 用 data:image/png;base64,... 这种内嵌写法，
// 就不用先把图片上传到别处
type ImageURL struct {
	URL string `json:"url"`
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
