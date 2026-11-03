package provider

import (
	"bufio"
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"hamusuta/agent-dev/src/config"
	"hamusuta/agent-dev/src/domain"
	"hamusuta/agent-dev/src/provider/openai"
	"io"
	"math/rand/v2"
	"net"
	"net/http"
	"strings"
	"time"
)

const (
	// maxAttempts 是一次请求最多试几次。网络抖动、限流（429）、服务端 5xx 都算可重试，
	// 试满 5 次还连不上，基本就是 key / url 配错了或者线路本身不通，再试也是白等
	maxAttempts = 5
	// retryBaseDelay 是第一次重试前等多久，之后每次翻倍，最多 maxRetryDelay
	retryBaseDelay = time.Second
	maxRetryDelay  = 8 * time.Second
	// streamIdleTimeout 是流式响应多久没收到任何字节就当成断了。
	// 接口在思考期间会发 `: keep-alive` 注释行保活，长时间一片死寂就是线路没了：
	// 不掐断的话界面会一直停在"回答中…"，分不清是慢还是断
	streamIdleTimeout = 2 * time.Minute
	// maxStreamLineBytes 是单行流式响应（一个 data:）的长度上限。
	// bufio.Scanner 默认只给 64KB，一次性吐出整段 arguments 的接口会撞上 token too long
	maxStreamLineBytes = 16 * 1024 * 1024
)

var httpClient = func() *http.Client {
	tr := http.DefaultTransport.(*http.Transport).Clone()
	// 连不上要尽快失败，好让重试有机会发生；等响应头也得有上限，否则线路卡住时请求会一直挂着。
	// 总超时不设：流式回答本来就可能聊很久，掐总时长的做法会误伤正常的长回答
	tr.DialContext = (&net.Dialer{Timeout: 15 * time.Second, KeepAlive: 30 * time.Second}).DialContext
	tr.TLSHandshakeTimeout = 15 * time.Second
	tr.ResponseHeaderTimeout = 60 * time.Second
	tr.IdleConnTimeout = 90 * time.Second
	return &http.Client{Transport: tr}
}()

// statusError 是接口回的 HTTP 错误码。429（限流）和 5xx（服务端故障）值得再试，
// 400 / 401 / 404 这种重试一百次也是同样的结果
type statusError struct {
	code int
	body string
}

func (e *statusError) Error() string {
	return fmt.Sprintf("deepseek 返回 %d: %s", e.code, e.body)
}

// retriable 说明这个状态码值不值得再试
func (e *statusError) retriable() bool {
	return e.code == http.StatusTooManyRequests || e.code >= 500
}

// transientError 表示这次失败是临时的（流里带的 error 帧、空响应、读流中断），
// 再试一次往往就好了
type transientError struct {
	msg string
}

func (e *transientError) Error() string { return e.msg }

// errStreamStalled 是流式响应长时间没有新数据：连接名义上还在，实际上已经死了
var errStreamStalled = fmt.Errorf("流式响应超过 %s 没有新数据", streamIdleTimeout)

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
			Content:    buildContent(m),
			ToolCalls:  buildOpenAIToolCalls(m.ToolCalls),
			ToolCallId: m.ToolCallId,
		})
	}
	return result
}

// buildContent 拼一条消息的 content：没带图片就还是那个字符串，
// 带了就换成"文字 + 每张图一个 image_url"的数组（文字放最前面，
// 有的接口在只有图片时会抱怨没有文字指令）。
func buildContent(m domain.AIChatMessage) any {
	if len(m.Images) == 0 {
		return m.Content
	}
	parts := make([]openai.ContentPart, 0, len(m.Images)+1)
	if strings.TrimSpace(m.Content) != "" {
		parts = append(parts, openai.ContentPart{Type: "text", Text: m.Content})
	}
	for _, img := range m.Images {
		if img.Empty() {
			continue
		}
		parts = append(parts, openai.ContentPart{
			Type:     "image_url",
			ImageURL: &openai.ImageURL{URL: img.DataURL()},
		})
	}
	return parts
}

// Chat 请求一次模型：失败（网络抖动、限流、5xx、空响应）时最多重试 maxAttempts 次，
// 每次重试前把原因通过 onDelta 的 StreamRetry 报给界面——线路问题用户得看得见，
// 否则界面上只会一直停在"连接模型…"，分不清是慢还是断了。
// 已经吐出过内容就不再重试：撤回不了，重试会把同一段话再说一遍。
func (p DeepSeekProvider) Chat(ctx context.Context, messages []domain.AIChatMessage, tools []domain.ToolSpec, onDelta func(domain.StreamEvent)) (domain.ChatResult, error) {
	ctx = contextOrBackground(ctx)
	// 请求体在几次重试之间是同一份，先序列化好
	reqBody, err := json.Marshal(openai.AIRequestMessage{
		Model:         p.Cfg.Model,
		Messages:      buildMessages(messages),
		Tools:         buildOpenAITools(tools),
		ToolChoice:    "auto",
		Stream:        true,
		StreamOptions: &openai.StreamOptions{IncludeUsage: true},
	})
	if err != nil {
		return domain.ChatResult{}, fmt.Errorf("序列化请求失败: %w", err)
	}

	var lastErr error
	attempts := 0
	for attempt := 1; attempt <= maxAttempts; attempt++ {
		attempts = attempt
		emitted := false
		res, err := p.attempt(ctx, reqBody, func(ev domain.StreamEvent) {
			if ev.Kind != domain.StreamRetry {
				emitted = true
			}
			if onDelta != nil {
				onDelta(ev)
			}
		})
		if err == nil {
			return res, nil
		}
		lastErr = err
		// 用户按了 Ctrl+C / 这一轮被取消：别再要下一次了
		if ctx.Err() != nil {
			return domain.ChatResult{}, ctx.Err()
		}
		if emitted || attempt == maxAttempts || !retriable(err) {
			break
		}
		delay := retryDelay(attempt)
		notifyRetry(onDelta, attempt+1, err, delay)
		if err := sleepCtx(ctx, delay); err != nil {
			return domain.ChatResult{}, err
		}
	}
	if attempts > 1 {
		// 试过不止一次就把次数说出来，免得用户以为是第一次就失败
		return domain.ChatResult{}, fmt.Errorf("连接模型失败（已试 %d 次）：%w", attempts, lastErr)
	}
	return domain.ChatResult{}, lastErr
}

// attempt 是一次完整的请求 + 读流。它受两层看管：外面的 ctx（用户中断）管着整轮，
// 里面还有个空闲超时——有一阵子没数据就把连接掐了，交给 Chat 重连一次
func (p DeepSeekProvider) attempt(ctx context.Context, reqBody []byte, onDelta func(domain.StreamEvent)) (domain.ChatResult, error) {
	var chatResult = domain.ChatResult{
		Content:   "",
		ToolCalls: nil,
	}
	if err := ctx.Err(); err != nil {
		return chatResult, err
	}

	reqCtx, cancel := context.WithCancel(ctx)
	defer cancel()
	// 空闲看门狗：每收到一段数据就重新计时，到点还没动静就取消这次请求
	stalled := make(chan struct{})
	watchdog := time.AfterFunc(streamIdleTimeout, func() {
		close(stalled)
		cancel()
	})
	defer watchdog.Stop()
	touch := func() { watchdog.Reset(streamIdleTimeout) }

	req, err := http.NewRequestWithContext(
		reqCtx,
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
		return chatResult, &statusError{code: resp.StatusCode, body: truncateBody(string(body))}
	}

	var content, reasoning strings.Builder
	var calls []domain.ToolCall
	// 用量在带 include_usage 的那一帧里，那一帧没有 choices，得在筛掉空 choices 之前收着
	var usage domain.Usage

	scanner := bufio.NewScanner(resp.Body)
	scanner.Buffer(make([]byte, 0, 64*1024), maxStreamLineBytes)

	for scanner.Scan() {
		touch()
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
			return chatResult, &transientError{msg: fmt.Sprintf("解析流式响应失败: %v: %s", err, truncateBody(payload))}
		}
		if chunk.Error != nil {
			return chatResult, &transientError{msg: "流式响应错误: " + chunk.Error.Message}
		}
		if chunk.Usage != nil {
			usage = domain.Usage{
				PromptTokens:     chunk.Usage.PromptTokens,
				CompletionTokens: chunk.Usage.CompletionTokens,
				TotalTokens:      chunk.Usage.TotalTokens,
			}
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
		// 看门狗先动手的：读流的错误只是被取消的连带结果，真正的原因要说清楚
		if isClosed(stalled) {
			return chatResult, errStreamStalled
		}
		if errors.Is(err, bufio.ErrTooLong) {
			return chatResult, fmt.Errorf("流式响应的单行超过 %d 字节，读不下去了", maxStreamLineBytes)
		}
		if ctx.Err() != nil {
			return chatResult, ctx.Err()
		}
		return chatResult, fmt.Errorf("读取流式响应失败: %w", err)
	}
	if isClosed(stalled) {
		return chatResult, errStreamStalled
	}

	chatResult.Content = content.String()
	chatResult.Reasoning = reasoning.String()
	chatResult.Usage = usage
	filtered := calls[:0]
	for _, c := range calls {
		if c.Name != "" || c.Id != "" {
			filtered = append(filtered, c)
		}
	}
	chatResult.ToolCalls = filtered

	if chatResult.Content == "" && len(chatResult.ToolCalls) == 0 {
		return chatResult, &transientError{msg: "模型返回空响应"}
	}
	return chatResult, nil
}

// retriable 说明这个错误值不值得再试一次：只有明确是"重试也没用"的才放过
func retriable(err error) bool {
	var se *statusError
	if errors.As(err, &se) {
		return se.retriable()
	}
	var te *transientError
	if errors.As(err, &te) {
		return true
	}
	// 单行超长是响应本身的问题，再来一次还是同一行长
	if errors.Is(err, bufio.ErrTooLong) {
		return false
	}
	// 用户中断 / 上下文到期不算失败
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return false
	}
	// 剩下的基本都是网络层的问题：连接被拒或重置、DNS 抖动、读到一半断流
	return true
}

// retryBase 是实际生效的重试等待基数。正常跑就是 retryBaseDelay，
// 测试里会临时调小，免得跑一个"重试用尽"的用例要等十几秒
var retryBase = retryBaseDelay

// retryDelay 算第 failed 次失败后要等多久：1s、2s、4s、8s 翻倍上去，
// 再撒一点随机抖动，免得几个请求同时重试又撞在一起
func retryDelay(failed int) time.Duration {
	delay := retryBase
	for i := 1; i < failed && delay < maxRetryDelay; i++ {
		delay *= 2
	}
	if delay > maxRetryDelay {
		delay = maxRetryDelay
	}
	jitter := maxRetryDelay / 8
	return delay + time.Duration(rand.Int64N(int64(jitter)+1))
}

// notifyRetry 把"这次失败了、等会儿再试"报给界面
func notifyRetry(onDelta func(domain.StreamEvent), next int, err error, delay time.Duration) {
	if onDelta == nil {
		return
	}
	onDelta(domain.StreamEvent{
		Kind: domain.StreamRetry,
		Text: fmt.Sprintf("连接模型失败（%s），%s 后第 %d/%d 次重试",
			briefError(err), delay.Round(100*time.Millisecond), next, maxAttempts),
	})
}

// sleepCtx 等待一会儿，中途被取消就立刻返回
func sleepCtx(ctx context.Context, d time.Duration) error {
	timer := time.NewTimer(d)
	defer timer.Stop()
	select {
	case <-timer.C:
		return nil
	case <-ctx.Done():
		return ctx.Err()
	}
}

// isClosed 判断 channel 是不是已经关了（看门狗有没有动过手），不阻塞
func isClosed(ch <-chan struct{}) bool {
	select {
	case <-ch:
		return true
	default:
		return false
	}
}

// briefError 把错误压成一行，重试提示里不用整段响应体
func briefError(err error) string {
	return briefText(err.Error())
}

// truncateBody 截断接口回的错误响应体，别让一段 HTML 错误页把日志和界面刷爆
func truncateBody(body string) string {
	return briefText(strings.TrimSpace(body))
}

// briefText 把文本压成一行并截断到 200 字符
func briefText(s string) string {
	msg := strings.Join(strings.Fields(s), " ")
	const limit = 200
	if r := []rune(msg); len(r) > limit {
		msg = string(r[:limit]) + "…"
	}
	return msg
}

// contextOrBackground 兜住没传 ctx 的调用方：NewRequestWithContext 拿到 nil 会 panic
func contextOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

func NewDeepSeek(dc config.DeepSeekConfig) (DeepSeekProvider, error) {
	if dc.ApiUrl == "" || dc.ApiKey == "" || dc.Model == "" {
		return DeepSeekProvider{}, fmt.Errorf("deepseek 配置不完整：ApiUrl/ApiKey/Model 必填")
	}
	return DeepSeekProvider{
		Cfg: dc,
	}, nil
}
