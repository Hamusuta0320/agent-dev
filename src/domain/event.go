package domain

import (
	"fmt"
	"time"
)

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
	// EventToolCall 表示模型要求调用一个工具，此时工具还没执行（状态是调用中）
	EventToolCall
	// EventToolResult 表示工具执行完，Text 是结果，Status 是成功还是失败
	EventToolResult
)

// ToolStatus 是一次工具调用的执行状态
type ToolStatus int

const (
	// ToolStatusRunning 表示工具正在执行，还没拿到结果
	ToolStatusRunning ToolStatus = iota
	// ToolStatusSuccess 表示工具执行成功
	ToolStatusSuccess
	// ToolStatusFailed 表示工具执行失败，原因在 Event.Err 里
	ToolStatusFailed
)

// Label 是状态给人看的文案
func (s ToolStatus) Label() string {
	switch s {
	case ToolStatusRunning:
		return "调用中"
	case ToolStatusSuccess:
		return "调用成功"
	case ToolStatusFailed:
		return "调用失败"
	default:
		return "未知状态"
	}
}

// Event 是 Run 过程中往外发的进度事件，UI 层拿它来渲染，core 自己不关心怎么显示
type Event struct {
	Kind EventKind
	Step int
	Text string
	Call ToolCall
	// Status 是工具调用的状态，只有 EventToolCall 和 EventToolResult 会带
	Status ToolStatus
	// Err 是工具调用失败的原因，只有调用失败时才有值
	Err string
	// StartedAt 是工具开始执行的时间，EventToolCall 时就是发事件的这一刻
	StartedAt time.Time
	// Elapsed 是这次工具调用花的时间，EventToolCall 时还是 0
	Elapsed time.Duration
}

// ElapsedLabel 把耗时格式化成给人看的样子，界面和控制台输出都用它
func (e Event) ElapsedLabel() string {
	switch {
	case e.Elapsed < time.Millisecond:
		return "<1ms"
	case e.Elapsed < time.Second:
		return fmt.Sprintf("%dms", e.Elapsed.Milliseconds())
	default:
		return fmt.Sprintf("%.2fs", e.Elapsed.Seconds())
	}
}

// EmitFunc 接收进度事件，实现方要保证调用是串行的（Run 是单线程发的）
type EmitFunc func(Event)
