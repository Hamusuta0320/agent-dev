package webui

import (
	"time"

	"hamusuta/agent-dev/src/domain"
	"hamusuta/agent-dev/src/tool"
)

// 推给浏览器的事件类型。core 那边用 iota 的 EventKind，这里换成字符串：
// 浏览器里 `ev.kind === "content"` 比 `ev.kind === 2` 好读得多，而且加一种事件
// 不会因为顺序变了把旧前端弄错。
const (
	kindStep      = "step"
	kindReasoning = "reasoning"
	kindContent   = "content"
	kindAnswerEnd = "answer_end"
	kindToolCall  = "tool_call"
	kindToolEnd   = "tool_result"
	kindUsage     = "usage"
	kindApproval  = "approval"
	kindRetry     = "retry"
	kindCompact   = "compact"
	kindTitle     = "title"
	// 下面几种 core 不会发，是这一轮的开头和结尾，前端靠它们切换"跑着/闲着"
	kindStarted   = "started"
	kindDone      = "done"
	kindError     = "error"
	kindCancelled = "cancelled"
	// kindPing 是心跳：连接空转时每 sseHeartbeat 发一条，前端靠它判断
	// "这条连接还活着"。SSE 的注释式心跳（": ping"）够用但前端看不见，
	// 断网后 EventSource 要等 TCP 超时才知道，这里让它有个明确的信号
	kindPing = "ping"
)

// event 是 SSE 里推给浏览器的一条消息。所有字段都带 omitempty，
// 一种事件只占它用得上的那几个键。
type event struct {
	// Seq 是这条会话里事件的序号，从 1 开始，前端可以拿它对齐重连后的位置
	Seq  int    `json:"seq"`
	Kind string `json:"kind"`
	Step int    `json:"step,omitempty"`

	// Text 是流式增量、回答全文、工具输出，或者一句给人的说明（重试、压缩、标题）
	Text string `json:"text,omitempty"`

	// 工具调用相关，只有 tool_call / tool_result 会带
	Tool      string `json:"tool,omitempty"`
	Intent    string `json:"intent,omitempty"`
	Args      string `json:"args,omitempty"`
	CallId    string `json:"call_id,omitempty"`
	Status    string `json:"status,omitempty"`
	FailCause string `json:"fail_cause,omitempty"`
	Elapsed   string `json:"elapsed,omitempty"`
	StartedAt string `json:"started_at,omitempty"`

	Usage    *usage    `json:"usage,omitempty"`
	Approval *approval `json:"approval,omitempty"`
}

// usage 是上下文占用，数字给前端自己排版，百分比也算好一起带过去
type usage struct {
	PromptTokens     int `json:"prompt_tokens"`
	CompletionTokens int `json:"completion_tokens"`
	SystemTokens     int `json:"system_tokens"`
	ToolTokens       int `json:"tool_tokens"`
	MessageTokens    int `json:"message_tokens"`
	ContextLimit     int `json:"context_limit"`
	UsedPercent      int `json:"used_percent"`
}

// approval 是一项等用户点头的写操作。ID 用来回答时指认是哪一项：
// 同时可能有多个工具在等，光靠顺序对不上。
type approval struct {
	ID     string        `json:"id"`
	Tool   string        `json:"tool"`
	Kind   string        `json:"kind"`
	Target string        `json:"target"`
	Detail string        `json:"detail,omitempty"`
	Diff   *approvalDiff `json:"diff,omitempty"`
}

type approvalDiff struct {
	Removed   []string `json:"removed,omitempty"`
	Added     []string `json:"added,omitempty"`
	Truncated bool     `json:"truncated,omitempty"`
}

// eventFromDomain 把 core 发的事件翻成推给浏览器的形状。
// 认不出的事件返回 nil，调用方直接跳过——多一种事件不该让老前端崩掉。
func eventFromDomain(seq int, ev domain.Event) *event {
	e := &event{Seq: seq, Step: ev.Step}
	switch ev.Kind {
	case domain.EventStep:
		e.Kind = kindStep
	case domain.EventReasoning:
		e.Kind, e.Text = kindReasoning, ev.Text
	case domain.EventContent:
		e.Kind, e.Text = kindContent, ev.Text
	case domain.EventAnswerEnd:
		e.Kind, e.Text = kindAnswerEnd, ev.Text
	case domain.EventRetry:
		e.Kind, e.Text = kindRetry, ev.Text
	case domain.EventCompact:
		e.Kind, e.Text = kindCompact, ev.Text
	case domain.EventTitle:
		e.Kind, e.Text = kindTitle, ev.Text
	case domain.EventToolCall:
		e.Kind = kindToolCall
		fillTool(e, ev)
	case domain.EventToolResult:
		e.Kind = kindToolEnd
		e.Text = ev.Text
		fillTool(e, ev)
		// 失败原因单独一个字段：Text 里是给模型看的结果，两者不能混
		e.FailCause = ev.Err
		e.Elapsed = ev.ElapsedLabel()
	case domain.EventUsage:
		e.Kind = kindUsage
		u := ev.Usage
		e.Usage = &usage{
			PromptTokens:     u.PromptTokens,
			CompletionTokens: u.CompletionTokens,
			SystemTokens:     u.SystemTokens,
			ToolTokens:       u.ToolTokens,
			MessageTokens:    u.MessageTokens,
			ContextLimit:     u.ContextLimit,
			UsedPercent:      u.LimitPercent(),
		}
	case domain.EventApproval:
		e.Kind = kindApproval
		if ev.Approval != nil {
			e.Approval = approvalFromDomain(*ev.Approval)
		}
	default:
		return nil
	}
	return e
}

// fillTool 把一次调用的公共字段填上：工具名、意图、参数和状态。
// 意图从参数里现取（和 TUI 一样），取不到就让前端退回参数摘要
func fillTool(e *event, ev domain.Event) {
	e.Tool = ev.Call.Name
	e.CallId = ev.Call.Id
	e.Args = ev.Call.Arguments
	e.Intent = tool.CallIntent(ev.Call.Arguments)
	e.Status = ev.Status.Label()
	if !ev.StartedAt.IsZero() {
		e.StartedAt = ev.StartedAt.Format("15:04:05")
	}
}

// approvalFromDomain 把确认请求翻成前端那份，Kind 换成字符串好判断配色
func approvalFromDomain(req domain.ApprovalRequest) *approval {
	out := &approval{
		Tool:   req.Tool,
		Target: req.Target,
		Detail: req.Detail,
		Kind:   "write_file",
	}
	if req.Kind == domain.ApprovalRunCommand {
		out.Kind = "run_command"
	}
	if d := req.Diff; d != nil && !d.Empty() {
		out.Diff = &approvalDiff{Removed: d.Removed, Added: d.Added, Truncated: d.Truncated}
	}
	return out
}

// doneEvent 是这一轮正常结束的收尾事件。Text 是模型最后那段回答，
// 前端拿它校对一遍流式攒下来的内容（流里最后一段有时会丢帧）
func doneEvent(seq int, answer string) *event {
	return &event{Seq: seq, Kind: kindDone, Text: answer}
}

// errorEvent 是这一轮失败时的收尾事件
func errorEvent(seq int, err error, cancelled bool) *event {
	kind := kindError
	if cancelled {
		kind = kindCancelled
	}
	msg := ""
	if err != nil {
		msg = err.Error()
	}
	return &event{Seq: seq, Kind: kind, Text: msg}
}

// startedEvent 在一轮开始时发出去，带上这次提问本身：
// 前端据此把用户那条消息挂进对话（多个标签页同时看同一会话时也能看到）
func startedEvent(seq int, prompt string) *event {
	return &event{Seq: seq, Kind: kindStarted, Text: prompt}
}

// pingEvent 是心跳。At 是服务端时间，前端只拿来显示"最后一次见到服务端"，
// 不做时钟对齐，所以给字符串就够
func pingEvent() *event {
	return &event{Kind: kindPing, StartedAt: time.Now().Format("15:04:05")}
}
