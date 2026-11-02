package domain

// EventKind 是运行过程中上报给 UI 的事件类型
type EventKind int

const (
	// EventStep 表示开始新一轮请求模型，Step 是轮次（从 1 开始）
	EventStep EventKind = iota
	// EventReasoning 是模型思考内容的增量文本
	EventReasoning
	// EventContent 是模型回答内容的增量文本
	EventContent
	// EventAnswerEnd 表示这一轮模型的输出结束，Text 是本轮的完整回答
	EventAnswerEnd
	// EventToolCall 表示模型要求调用一个工具
	EventToolCall
	// EventToolResult 表示工具执行完，Text 是结果
	EventToolResult
)

// Event 是 Run 过程中往外发的进度事件，UI 层拿它来渲染，core 自己不关心怎么显示
type Event struct {
	Kind EventKind
	Step int
	Text string
	Call ToolCall
}

// EmitFunc 接收进度事件，实现方要保证调用是串行的（Run 是单线程发的）
type EmitFunc func(Event)
