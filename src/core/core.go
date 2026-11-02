package core

import (
	"encoding/json"
	"fmt"
	"hamusuta/agent-dev/src/domain"
	"strings"
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

	for step := range maxSteps {
		emit(domain.Event{Kind: domain.EventStep, Step: step + 1})
		onDelta := func(ev domain.StreamEvent) {
			kind := domain.EventContent
			if ev.Kind == domain.StreamReasoning {
				kind = domain.EventReasoning
			}
			emit(domain.Event{Kind: kind, Text: ev.Text})
		}
		res, err := ctx.Provider.Chat(ctx.Context, buildMessages(ctx, session), specs, onDelta)
		if err != nil {
			return "", err
		}
		emit(domain.Event{Kind: domain.EventAnswerEnd, Step: step + 1, Text: res.Content})
		session.AppendAssistant(res.Content, res.Reasoning, res.ToolCalls)
		if len(res.ToolCalls) == 0 {
			return res.Content, nil
		}
		for _, call := range res.ToolCalls {
			// 先上报"调用中"，执行完再上报结果和耗时，界面靠这两步更新状态
			started := time.Now()
			emit(domain.Event{
				Kind:      domain.EventToolCall,
				Call:      call,
				Status:    domain.ToolStatusRunning,
				StartedAt: started,
			})
			out, status, reason := executeTool(ctx.Tools, call)
			emit(domain.Event{
				Kind:      domain.EventToolResult,
				Text:      out,
				Call:      call,
				Status:    status,
				Err:       reason,
				StartedAt: started,
				Elapsed:   time.Since(started),
			})
			session.AppendToolResult(call, out)
		}
	}
	return "", fmt.Errorf("工具调用超过 %d 轮, 疑似死循环", maxSteps)
}

// executeTool 执行一次工具调用，返回给模型的输出、调用状态和失败原因。
// 输出里也带上失败原因，模型看到才知道下一步该怎么改。
func executeTool(tools map[string]domain.Tool, call domain.ToolCall) (string, domain.ToolStatus, string) {
	t, ok := tools[call.Name]
	if !ok {
		reason := "不存在的工具 " + call.Name
		return "错误: " + reason, domain.ToolStatusFailed, reason
	}
	result, err := t.Execute(json.RawMessage(call.Arguments))
	if err != nil {
		return "执行失败: " + err.Error(), domain.ToolStatusFailed, err.Error()
	}
	return result, domain.ToolStatusSuccess, ""
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

// consoleEmit 是 ctx.Emit 为空时的兜底实现：往标准输出打纯文本进度
func consoleEmit() domain.EmitFunc {
	var inReasoning, midLine bool
	return func(ev domain.Event) {
		switch ev.Kind {
		case domain.EventStep:
			if midLine {
				fmt.Println()
				midLine = false
			}
			fmt.Printf("[轮次: %d]\n", ev.Step)
		case domain.EventReasoning:
			if !inReasoning {
				if midLine {
					fmt.Println()
				}
				fmt.Print("[思考] ")
				inReasoning = true
			}
			fmt.Print(ev.Text)
			midLine = true
		case domain.EventContent:
			if inReasoning {
				fmt.Println("\n[回答]")
				inReasoning = false
			}
			fmt.Print(ev.Text)
			midLine = true
		// 流式输出不带换行，这一轮结束要收个尾，否则下一轮的标题会粘在后面
		case domain.EventAnswerEnd:
			if midLine {
				fmt.Println()
				midLine = false
			}
		case domain.EventToolCall:
			if midLine {
				fmt.Println()
				midLine = false
			}
			fmt.Printf("[工具] %s %s · %s · %s\n",
				ev.Call.Name,
				truncate(ev.Call.Arguments, 160),
				ev.StartedAt.Format("15:04:05"),
				ev.Status.Label())
		case domain.EventToolResult:
			if ev.Status == domain.ToolStatusFailed {
				fmt.Printf("[结果] %s · 耗时 %s · 原因: %s\n",
					ev.Status.Label(), ev.ElapsedLabel(), truncate(ev.Err, 200))
			} else {
				fmt.Printf("[结果] %s · 耗时 %s\n", ev.Status.Label(), ev.ElapsedLabel())
			}
			fmt.Printf("  %s\n", truncate(ev.Text, 400))
		}
	}
}

func truncate(s string, maxRunes int) string {
	s = strings.Join(strings.Fields(s), " ")
	runes := []rune(s)
	if len(runes) <= maxRunes {
		return s
	}
	return string(runes[:maxRunes]) + fmt.Sprintf("...(已截断, 共 %d 字符)", len(runes))
}
