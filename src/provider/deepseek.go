package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"fmt"
	"hamusuta/agent-dev/src/config"
	"hamusuta/agent-dev/src/domain"
	"hamusuta/agent-dev/src/provider/openai"
	"io"
	"net/http"
	"strings"
	"time"
)

var httpClient = func() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	tr.ResponseHeaderTimeout = 60 * time.Second
	return &http.Client{Transport: tr}
}()

type DeepSeekProvider struct {
	Cfg config.DeepSeekConfig
}

func buildOpenAIToolCalls(calls []domain.ToolCall) []openai.ToolCall {
	result := []openai.ToolCall{}
	for _, c := range calls {
		result = append(result, openai.ToolCall{
			Id:   c.Id,
			Type: "function",
			Function: openai.ToolCallFunction{
				Name:      c.Name,
				Arguments: c.Arguments,
			},
		})
	}
	return result
}

func buildOpenAITools(specs []domain.ToolSpec) []openai.ToolSpec {
	result := make([]openai.ToolSpec, 0, len(specs))
	for _, c := range specs {
		result = append(result, openai.ToolSpec{
			Type: "function",
			Function: openai.FunctionDefinition{
				Name:        c.Name,
				Description: c.Description,
				Parameters:  c.Parameters,
			},
		})
	}
	return result
}

func buildDomainToolCalls(calls []openai.ToolCall) []domain.ToolCall {
	result := []domain.ToolCall{}
	for _, c := range calls {
		result = append(result, domain.ToolCall{
			Id:        c.Id,
			Name:      c.Function.Name,
			Arguments: c.Function.Arguments,
		})
	}
	return result
}

func buildMessages(messages []domain.AIChatMessage) []openai.AIChatMessage {
	result := []openai.AIChatMessage{}
	for _, m := range messages {
		result = append(result, openai.AIChatMessage{
			Role:       m.Role,
			Content:    m.Content,
			ToolCalls:  buildOpenAIToolCalls(m.ToolCalls),
			ToolCallId: m.ToolCallId,
		})
	}
	return result
}

func (p DeepSeekProvider) Chat(ctx context.Context, messages []domain.AIChatMessage, tools []domain.ToolSpec, onDelta func(domain.StreamEvent)) (domain.ChatResult, error) {
	var chatResult = domain.ChatResult{
		Content:   "",
		ToolCalls: nil,
	}
	reqBody, err := json.Marshal(openai.AIRequestMessage{
		Model:      p.Cfg.Model,
		Messages:   buildMessages(messages),
		Tools:      buildOpenAITools(tools),
		ToolChoice: "auto",
		Stream:     true,
	})
	if err != nil {
		return chatResult, fmt.Errorf("序列化请求失败: %w", err)
	}
	req, err := http.NewRequestWithContext(
		ctx,
		http.MethodPost,
		strings.TrimRight(p.Cfg.ApiUrl, "/")+"/chat/completions",
		bytes.NewReader(reqBody),
	)

	if err != nil {
		return chatResult, fmt.Errorf("构建请求失败: %w", err)
	}

	req.Header.Set("Content-Type", "application/json")
	req.Header.Set("Authorization", "Bearer "+p.Cfg.ApiKey)

	resp, err := httpClient.Do(req)
	if err != nil {
		return chatResult, fmt.Errorf("请求 deepseek 失败: %w", err)
	}
	defer resp.Body.Close()

	if resp.StatusCode != http.StatusOK {
		body, _ := io.ReadAll(resp.Body)
		return chatResult, fmt.Errorf("deepseek 返回 %d: %s", resp.StatusCode, body)
	}

	var content, reasoning strings.Builder
	var calls []domain.ToolCall

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), 4*1024*1024)

	for scanner.Scan() {
		line := strings.TrimSpace(scanner.Text())
		if line == "" || strings.HasPrefix(line, ":") {
			continue
		}
		if !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		if payload == "[DONE]" {
			break
		}
		var chunk openai.AIStreamResponse
		if err := json.Unmarshal([]byte(payload), &chunk); err != nil {
			return chatResult, fmt.Errorf("解析流式响应失败: %w: %s", err, payload)
		}
		if chunk.Error != nil {
			return chatResult, fmt.Errorf("流式响应错误: %s", chunk.Error.Message)
		}
		if len(chunk.Choices) == 0 {
			continue
		}
		delta := chunk.Choices[0].Delta
		if delta.ReasoningContent != "" {
			reasoning.WriteString(delta.ReasoningContent)
			if onDelta != nil {
				onDelta(domain.StreamEvent{Kind: domain.StreamReasoning, Text: delta.ReasoningContent})
			}
		}
		if delta.Content != "" {
			content.WriteString(delta.Content)
			if onDelta != nil {
				onDelta(domain.StreamEvent{Kind: domain.StreamContent, Text: delta.Content})
			}
		}
		for _, tc := range delta.ToolCalls {
			for len(calls) <= tc.Index {
				calls = append(calls, domain.ToolCall{})
			}
			call := &calls[tc.Index]
			if tc.Id != "" {
				call.Id = tc.Id
			}
			if tc.Function.Name != "" {
				call.Name = tc.Function.Name
			}
			call.Arguments += tc.Function.Arguments
		}
	}
	if err := scanner.Err(); err != nil {
		return chatResult, fmt.Errorf("读取流式响应失败: %w", err)
	}

	chatResult.Content = content.String()
	chatResult.Reasoning = reasoning.String()
	filtered := calls[:0]
	for _, c := range calls {
		if c.Name != "" || c.Id != "" {
			filtered = append(filtered, c)
		}
	}
	chatResult.ToolCalls = filtered

	if chatResult.Content == "" && len(chatResult.ToolCalls) == 0 {
		return chatResult, fmt.Errorf("模型返回空响应")
	}
	return chatResult, nil
}

func NewDeepSeek(dc config.DeepSeekConfig) (DeepSeekProvider, error) {
	if dc.ApiUrl == "" || dc.ApiKey == "" || dc.Model == "" {
		return DeepSeekProvider{}, fmt.Errorf("deepseek 配置不完整：ApiUrl/ApiKey/Model 必填")
	}
	return DeepSeekProvider{
		Cfg: dc,
	}, nil
}
