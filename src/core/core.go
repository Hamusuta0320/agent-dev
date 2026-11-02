package core

import (
	"context"
	"encoding/json"
	"fmt"
	"hamusuta/agent-dev/src/domain"
	"hamusuta/agent-dev/src/tool"
	"io"
	"os"
	"strings"
	"sync"
	"time"
)

// Loop 跑完一次用户提问：先把输入记进会话，再和模型来回，直到它不再调用工具
func Loop(ctx domain.Ctx) error {
	if ctx.Session == nil {
		ctx.Session = domain.NewSession(ctx.WorkDir)
	}
	ctx.Session.AppendUser(ctx.Prompt)
	_, err := Run(ctx, ctx.Session)
	return err
}

func buildSpecs(tools map[string]domain.Tool) []domain.ToolSpec {
	toolSpecs := []domain.ToolSpec{}
	for _, tool := range tools {
		toolSpecs = append(toolSpecs, domain.ToolSpec{
			Name:        tool.Name(),
			Description: tool.Description(),
			Parameters:  tool.Schema(),
		})
	}
	return toolSpecs
}

// Run 反复请求模型，把每一轮的思考/回答/工具调用和工具结果都写进 session。
// 每一轮都按 session 重新拼 messages，所以会话历史就是上下文。
// 过程进度通过 ctx.Emit 上报，没设置时走默认的控制台打印。
func Run(ctx domain.Ctx, session *domain.Session) (string, error) {
	const maxSteps = 1200
	specs := buildSpecs(ctx.Tools)
	emit := ctx.Emit
	if emit == nil {
		emit = consoleEmit()
	}

	// always 记着本回合里用户说过"总是允许"的工具，后面同样的工具不再问
	always := make(map[string]bool)

	for step := range maxSteps {
		// 用户中途按了 Ctrl+C 就别再要下一轮了，赶紧把这一轮结束掉
		if err := cancelErr(ctx.Context); err != nil {
			return "", err
		}
		emit(domain.Event{Kind: domain.EventStep, Step: step + 1})
		onDelta := func(ev domain.StreamEvent) {
			kind := domain.EventContent
			if ev.Kind == domain.StreamReasoning {
				kind = domain.EventReasoning
			}
			emit(domain.Event{Kind: kind, Text: ev.Text})
		}
		messages := buildMessages(ctx, session)
		res, err := ctx.Provider.Chat(ctx.Context, messages, specs, onDelta)
		if err != nil {
			return "", err
		}
		// 上下文占用：总数用响应里的 usage，三项分项按本地估算的比例拆
		emit(domain.Event{
			Kind:  domain.EventUsage,
			Step:  step + 1,
			Usage: newContextUsage(messages[0].Content, messages[1:], specs, res.Usage, ctx.ContextLimit),
		})
		emit(domain.Event{Kind: domain.EventAnswerEnd, Step: step + 1, Text: res.Content})
		session.AppendAssistant(res.Content, res.Reasoning, res.ToolCalls)
		if len(res.ToolCalls) == 0 {
			return res.Content, nil
		}
		for i := 0; i < len(res.ToolCalls); {
			// 只读的工具互不干扰，连着的一批可以并行跑；会改磁盘的（Write / Edit / Bash）
			// 单独一批一个跑，免得两个写操作撞在同一个文件上
			batch := res.ToolCalls[i : i+1]
			if parallelizable(ctx, res.ToolCalls[i]) {
				j := i + 1
				for j < len(res.ToolCalls) && parallelizable(ctx, res.ToolCalls[j]) {
					j++
				}
				batch = res.ToolCalls[i:j]
			}

			// 先上报"调用中"，执行完再上报结果和耗时，界面靠这两步更新状态。
			// emit 只在这个 goroutine 里调，UI 那边不用操心并发。
			started := make([]time.Time, len(batch))
			for k, call := range batch {
				started[k] = time.Now()
				emit(domain.Event{
					Kind:      domain.EventToolCall,
					Call:      call,
					Status:    domain.ToolStatusRunning,
					StartedAt: started[k],
				})
			}

			outcomes := runCalls(ctx, emit, always, batch)

			for k, call := range batch {
				emit(domain.Event{
					Kind:      domain.EventToolResult,
					Text:      outcomes[k].out,
					Call:      call,
					Status:    outcomes[k].status,
					Err:       outcomes[k].reason,
					StartedAt: started[k],
					Elapsed:   outcomes[k].elapsed,
				})
				session.AppendToolResult(call, outcomes[k].out)
			}
			i += len(batch)

			// 中断发生在工具执行期间：这一轮剩下的工具不再跑，但每个调用都要补一条结果，
			// 否则会话里留着没有结果的工具调用，下次请求会被模型接口拒掉
			if err := cancelErr(ctx.Context); err != nil {
				for _, rest := range res.ToolCalls[i:] {
					session.AppendToolResult(rest, "错误: "+interruptedReason)
				}
				return "", err
			}
		}
	}
	return "", fmt.Errorf("工具调用超过 %d 轮, 疑似死循环", maxSteps)
}

// toolOutcome 是一次工具调用的结果：给模型的输出、状态、失败原因和花了多久
type toolOutcome struct {
	out     string
	status  domain.ToolStatus
	reason  string
	elapsed time.Duration
}

// parallelizable 说明这次调用能不能和同批的其它调用一起跑：工具存在，而且不改磁盘。
// 不存在的工具、只读模式下被挡住的工具都会走单独的一批，报错的先后顺序和以前一样。
func parallelizable(ctx domain.Ctx, call domain.ToolCall) bool {
	t, ok := ctx.Tools[call.Name]
	return ok && !t.Mutating()
}

// runCalls 跑一批调用：只有一个就当场跑，多个就并发跑，结果按原顺序返回。
// 需要用户确认的写操作永远不会进多调用的批次（都是单独一批），
// 所以 always 这张 map 在并发时只有读，不会打架。
func runCalls(ctx domain.Ctx, emit domain.EmitFunc, always map[string]bool, calls []domain.ToolCall) []toolOutcome {
	outcomes := make([]toolOutcome, len(calls))
	one := func(i int) {
		started := time.Now()
		out, status, reason := executeTool(ctx, emit, always, calls[i])
		outcomes[i] = toolOutcome{out: out, status: status, reason: reason, elapsed: time.Since(started)}
	}
	if len(calls) == 1 {
		one(0)
		return outcomes
	}

	var wg sync.WaitGroup
	for i := range calls {
		wg.Add(1)
		go func(i int) {
			defer wg.Done()
			one(i)
		}(i)
	}
	wg.Wait()
	return outcomes
}

// executeTool 执行一次工具调用，返回给模型的输出、调用状态和失败原因。
// 输出里也带上失败原因，模型看到才知道下一步该怎么改。
func executeTool(ctx domain.Ctx, emit domain.EmitFunc, always map[string]bool, call domain.ToolCall) (string, domain.ToolStatus, string) {
	t, ok := ctx.Tools[call.Name]
	if !ok {
		reason := "不存在的工具 " + call.Name
		return "错误: " + reason, domain.ToolStatusFailed, reason
	}
	if ok, reason := approved(ctx, emit, always, call, t); !ok {
		return "错误: " + reason, domain.ToolStatusFailed, reason
	}
	// 只读模式下写类工具本该没发出去，漏进来也在这儿兜住
	if ctx.Mode == domain.ModeReadOnly && t.Mutating() {
		reason := "当前是只读模式，不能用会改动磁盘的工具 " + call.Name
		return "错误: " + reason, domain.ToolStatusFailed, reason
	}
	result, err := t.Execute(ctx.Context, json.RawMessage(call.Arguments))
	if err != nil {
		// 中断时工具返回的一般就是 context.Canceled，换成看得懂的说法给用户和模型
		if cancelErr(ctx.Context) != nil {
			return "错误: " + interruptedReason, domain.ToolStatusFailed, interruptedReason
		}
		return "执行失败: " + err.Error(), domain.ToolStatusFailed, err.Error()
	}
	return result, domain.ToolStatusSuccess, ""
}

// interruptedReason 是用户中断这一轮时记给模型看的原因
const interruptedReason = "用户中断了这一轮，操作没有执行完"

// cancelErr 只在 Context 真被取消（用户按了 Ctrl+C）时返回错误，没传 Context 时返回 nil
func cancelErr(c context.Context) error {
	if c == nil {
		return nil
	}
	return c.Err()
}

// approved 判断这次调用能不能往下走。"编辑需授权"模式下会发一个 EventApproval
// 停下来等用户选，用户拒绝时返回 false 和给模型看的原因。
func approved(ctx domain.Ctx, emit domain.EmitFunc, always map[string]bool, call domain.ToolCall, t domain.Tool) (bool, string) {
	if ctx.Mode != domain.ModeAsk || !t.Mutating() || always[call.Name] {
		return true, ""
	}
	if emit == nil {
		// 没人能回答确认：宁可不做，也不要默默写下去
		return false, "用户不在，没法确认写操作；要直接执行请切到完全授权模式"
	}

	req := approvalRequest(call, t)
	reply := make(chan domain.ApprovalAnswer, 1)
	emit(domain.Event{Kind: domain.EventApproval, Approval: &req, Reply: reply})

	var ans domain.ApprovalAnswer
	select {
	case ans = <-reply:
	case <-doneChan(ctx.Context):
		return false, "用户没确认就中断了这一轮"
	}
	if !ans.Allowed {
		return false, denyReason(ans.Feedback)
	}
	if ans.Always {
		always[call.Name] = true
	}
	return true, ""
}

// denyReason 是拒绝时回给模型的话。用户顺手写了说明的话就带上，
// 模型照着这个方向改，比盲目重试一遍强。
func denyReason(feedback string) string {
	reason := "用户拒绝了这次操作"
	if fb := strings.TrimSpace(feedback); fb != "" {
		reason += "，并说明：" + fb
	}
	return reason + "；别原样重试，按用户的说明换个做法，或者先问清楚再动手"
}

// approvalRequest 问工具自己该怎么展示这次改动，工具没实现 Previewer 就退回原始参数
func approvalRequest(call domain.ToolCall, t domain.Tool) domain.ApprovalRequest {
	if p, ok := t.(domain.Previewer); ok {
		req := p.Preview(json.RawMessage(call.Arguments))
		req.Tool = call.Name
		return req
	}
	return domain.ApprovalRequest{Tool: call.Name, Target: truncate(call.Arguments, 200)}
}

// doneChan 在没传 Context 时返回 nil，select 会一直等着 Reply
func doneChan(c context.Context) <-chan struct{} {
	if c == nil {
		return nil
	}
	return c.Done()
}

// buildMessages 每次请求都重新拼：系统提示词在前（环境可能变），后面是会话历史
func buildMessages(ctx domain.Ctx, session *domain.Session) []domain.AIChatMessage {
	messages := []domain.AIChatMessage{
		{
			Content: systemPrompt(ctx),
			Role:    domain.RoleSystem,
		},
	}
	if session != nil {
		messages = append(messages, session.History()...)
	}
	return messages
}

// consoleEmit 是 ctx.Emit 为空时的兜底实现：标准输入输出上的纯文本进度
func consoleEmit() domain.EmitFunc {
	return ConsoleEmit(os.Stdin, os.Stdout)
}

// ConsoleEmit 用指定的输入输出打纯文本进度，需要授权的写操作会当场从 in 读一行回答。
// 纯控制台的读行循环要和自己共用同一个 Reader（各读各的 buffer 会互相吞输入），
// 所以这里把 in / out 留成参数。
func ConsoleEmit(in io.Reader, out io.Writer) domain.EmitFunc {
	var inReasoning, midLine bool
	approver := newConsoleApprover(in, out)
	return func(ev domain.Event) {
		switch ev.Kind {
		case domain.EventStep:
			if midLine {
				fmt.Fprintln(out)
				midLine = false
			}
			fmt.Fprintf(out, "[轮次: %d]\n", ev.Step)
		case domain.EventReasoning:
			if !inReasoning {
				if midLine {
					fmt.Fprintln(out)
				}
				fmt.Fprint(out, "[思考] ")
				inReasoning = true
			}
			fmt.Fprint(out, ev.Text)
			midLine = true
		case domain.EventContent:
			if inReasoning {
				fmt.Fprint(out, "\n[回答]\n")
				inReasoning = false
			}
			fmt.Fprint(out, ev.Text)
			midLine = true
		// 流式输出不带换行，这一轮结束要收个尾，否则下一轮的标题会粘在后面
		case domain.EventAnswerEnd:
			if midLine {
				fmt.Fprintln(out)
				midLine = false
			}
		case domain.EventUsage:
			if midLine {
				fmt.Fprintln(out)
				midLine = false
			}
			u := ev.Usage
			fmt.Fprintf(out, "[上下文] %s tokens%s（系统提示词 %s · 工具定义 %s · 对话消息 %s）· 本轮回复 %s\n",
				domain.FormatTokens(u.PromptTokens),
				limitLabel(u),
				domain.FormatTokens(u.SystemTokens),
				domain.FormatTokens(u.ToolTokens),
				domain.FormatTokens(u.MessageTokens),
				domain.FormatTokens(u.CompletionTokens))
		// 控制台模式在这里真的停下来问一句，和 TUI 里的确认框是一回事
		case domain.EventApproval:
			if midLine {
				fmt.Fprintln(out)
				midLine = false
			}
			ans := domain.ApprovalAnswer{}
			if ev.Approval != nil {
				ans = askOnConsole(approver, *ev.Approval)
			}
			if ev.Reply != nil {
				ev.Reply <- ans
			}
		case domain.EventToolCall:
			if midLine {
				fmt.Fprintln(out)
				midLine = false
			}
			// 有意图就显示意图，没有（老会话或参数坏了）就退回参数摘要
			label := tool.CallIntent(ev.Call.Arguments)
			if label == "" {
				label = truncate(ev.Call.Arguments, 160)
			}
			fmt.Fprintf(out, "[工具] %s %s · %s · %s\n",
				ev.Call.Name,
				label,
				ev.StartedAt.Format("15:04:05"),
				ev.Status.Label())
		case domain.EventToolResult:
			if ev.Status == domain.ToolStatusFailed {
				fmt.Fprintf(out, "[结果] %s · 耗时 %s · 原因: %s\n",
					ev.Status.Label(), ev.ElapsedLabel(), truncate(ev.Err, 200))
			} else {
				fmt.Fprintf(out, "[结果] %s · 耗时 %s\n", ev.Status.Label(), ev.ElapsedLabel())
			}
			fmt.Fprintf(out, "  %s\n", truncate(ev.Text, 400))
		}
	}
}

// limitLabel 是控制台输出里跟在用量后面的"/上限（已用 x%）"，不知道窗口大小时返回空串
func limitLabel(u domain.ContextUsage) string {
	if u.ContextLimit <= 0 {
		return ""
	}
	return fmt.Sprintf("/%s（已用 %d%%）", domain.FormatTokens(u.ContextLimit), u.LimitPercent())
}

// truncate 把文本压成一行并截断，用于控制台里的工具输出
func truncate(s string, maxRunes int) string {
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + fmt.Sprintf("...(已截断, 共 %d 字符)", len(runes))
}
