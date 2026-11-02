package core

import (
	"context"
	"encoding/json"
	"fmt"
	"hamusuta/agent-dev/src/domain"
	"os"
)

func Loop(ctx domain.Ctx) {
	messages := makeMessages(ctx)
	_, err := Run(ctx.Context, ctx.Provider, ctx.Tools, messages)
	if err != nil {
		fmt.Fprintf(os.Stderr, "Run失败: %v\n", err)
		return
	}
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

func Run(ctx context.Context, p domain.Provider, tools map[string]domain.Tool, messages []domain.AIChatMessage) (string, error) {
	const maxSteps = 30
	specs := buildSpecs(tools)

	for range maxSteps {
		res, err := p.Chat(ctx, messages, specs, func(delta string) {
			fmt.Print(delta)
		})
		if err != nil {
			return "", err
		}
		if res.Content != "" {
			fmt.Println()
		}
		messages = append(messages, domain.AIChatMessage{
			Role: "assistant", Content: res.Content, ToolCalls: res.ToolCalls,
		})
		if len(res.ToolCalls) == 0 {
			return res.Content, nil
		}
		for _, call := range res.ToolCalls {
			out := ""
			t, ok := tools[call.Name]
			if !ok {
				out = "错误: 不存在的工具 " + call.Name
			} else if result, err := t.Execute(json.RawMessage(call.Arguments)); err != nil {
				out = "执行失败: " + err.Error()
			} else {
				out = result
			}
			messages = append(messages, domain.AIChatMessage{
				Role: "tool", ToolCallId: call.Id, Content: out,
			})
		}
	}
	return "", fmt.Errorf("工具调用超过 %d 轮, 疑似死循环", maxSteps)
}

func makeMessages(ctx domain.Ctx) []domain.AIChatMessage {
	return []domain.AIChatMessage{
		{
			Content: "你是一位AI助手",
			Role:    "system",
		},
		{
			Content: ctx.Prompt,
			Role:    "user",
		},
	}
}
