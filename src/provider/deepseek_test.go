package provider

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"hamusuta/agent-dev/src/config"
	"hamusuta/agent-dev/src/domain"
)

// sseServer 起一个假接口：handler 拿到第几次请求（从 1 开始）自己决定怎么回
func sseServer(t *testing.T, handler func(w http.ResponseWriter, attempt int)) (*httptest.Server, *int32) {
	t.Helper()
	var count int32
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		n := int(atomic.AddInt32(&count, 1))
		handler(w, n)
	}))
	t.Cleanup(srv.Close)
	return srv, &count
}

// writeStream 回一段正常的流式响应：一句回答 + 用量 + [DONE]
func writeStream(w http.ResponseWriter, content string) {
	w.Header().Set("Content-Type", "text/event-stream")
	w.WriteHeader(http.StatusOK)
	fmt.Fprintf(w, "data: {\"choices\":[{\"delta\":{\"content\":%q}}]}\n\n", content)
	fmt.Fprint(w, "data: {\"usage\":{\"prompt_tokens\":10,\"completion_tokens\":2,\"total_tokens\":12}}\n\n")
	fmt.Fprint(w, "data: [DONE]\n\n")
}

func newTestProvider(url string) DeepSeekProvider {
	p, _ := NewDeepSeek(config.DeepSeekConfig{ApiUrl: url, ApiKey: "test-key", Model: "test-model"})
	return p
}

func testMessages() []domain.AIChatMessage {
	return []domain.AIChatMessage{{Role: domain.RoleUser, Content: "你好"}}
}

// 服务端 5xx 是临时故障，要自动重连，而且把重试这件事报给界面
func TestChatRetriesOnServerError(t *testing.T) {
	srv, count := sseServer(t, func(w http.ResponseWriter, attempt int) {
		if attempt == 1 {
			w.WriteHeader(http.StatusInternalServerError)
			fmt.Fprint(w, "临时故障")
			return
		}
		writeStream(w, "好了")
	})

	var retries []string
	res, err := newTestProvider(srv.URL).Chat(context.Background(), testMessages(), nil, func(ev domain.StreamEvent) {
		if ev.Kind == domain.StreamRetry {
			retries = append(retries, ev.Text)
		}
	})
	if err != nil {
		t.Fatalf("重试之后应该成功: %v", err)
	}
	if res.Content != "好了" {
		t.Fatalf("回答不对: %q", res.Content)
	}
	if got := atomic.LoadInt32(count); got != 2 {
		t.Fatalf("应该请求 2 次，实际 %d 次", got)
	}
	if len(retries) != 1 {
		t.Fatalf("应该报 1 次重试，实际 %d 次: %v", len(retries), retries)
	}
	// 界面上得能看出是第几次、为什么失败
	for _, want := range []string{"2/5", "临时故障", "500"} {
		if !strings.Contains(retries[0], want) {
			t.Fatalf("重试提示里少了 %q: %q", want, retries[0])
		}
	}
}

// 401 这种重试也没用的错误不该反复试
func TestChatDoesNotRetryOnAuthError(t *testing.T) {
	srv, count := sseServer(t, func(w http.ResponseWriter, attempt int) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":"bad key"}`)
	})

	_, err := newTestProvider(srv.URL).Chat(context.Background(), testMessages(), nil, nil)
	if err == nil {
		t.Fatal("401 应该直接失败")
	}
	if got := atomic.LoadInt32(count); got != 1 {
		t.Fatalf("401 不该重试，实际请求 %d 次", got)
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "bad key") {
		t.Fatalf("错误里要带上状态码和响应体: %v", err)
	}
}

// 已经吐出内容之后再失败就不重试了：重试会把同一段话再说一遍
func TestChatDoesNotRetryAfterContent(t *testing.T) {
	srv, count := sseServer(t, func(w http.ResponseWriter, attempt int) {
		w.Header().Set("Content-Type", "text/event-stream")
		w.WriteHeader(http.StatusOK)
		fmt.Fprint(w, "data: {\"choices\":[{\"delta\":{\"content\":\"说过的话\"}}]}\n\n")
		// 流里带 error 帧：平时这是可重试的，但这次已经出过字了
		fmt.Fprint(w, "data: {\"error\":{\"message\":\"连接断了\"}}\n\n")
	})

	var emitted int
	_, err := newTestProvider(srv.URL).Chat(context.Background(), testMessages(), nil, func(ev domain.StreamEvent) {
		if ev.Kind == domain.StreamContent {
			emitted++
		}
	})
	if err == nil {
		t.Fatal("流里报错应该让这次请求失败")
	}
	if emitted != 1 {
		t.Fatalf("已经收到的内容不该丢: %d 段", emitted)
	}
	if got := atomic.LoadInt32(count); got != 1 {
		t.Fatalf("出过字之后不该重试，实际请求 %d 次", got)
	}
}

// 中断了这一轮就别接着重试了
func TestChatStopsRetryOnCancel(t *testing.T) {
	srv, count := sseServer(t, func(w http.ResponseWriter, attempt int) {
		w.WriteHeader(http.StatusServiceUnavailable)
	})

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := newTestProvider(srv.URL).Chat(ctx, testMessages(), nil, nil); err == nil {
		t.Fatal("已经取消的请求应该返回错误")
	}
	if got := atomic.LoadInt32(count); got != 0 {
		t.Fatalf("取消之后一次都不该发，实际 %d 次", got)
	}
}

// 没传 ctx 也不能 panic（NewRequestWithContext 收到 nil 会炸）
func TestChatWithoutContext(t *testing.T) {
	srv, _ := sseServer(t, func(w http.ResponseWriter, attempt int) { writeStream(w, "行") })
	//nolint:staticcheck // 故意传 nil：兜底逻辑就是要扛住这个
	if _, err := newTestProvider(srv.URL).Chat(nil, testMessages(), nil, nil); err != nil {
		t.Fatalf("没传 ctx 也该能跑: %v", err)
	}
}

// 一直连不上就试满 5 次为止，最后的错误里要说明试了几次
func TestChatGivesUpAfterMaxAttempts(t *testing.T) {
	// 重试等待调成 1ms，不然这个用例要等十几秒
	old := retryBase
	retryBase = time.Millisecond
	t.Cleanup(func() { retryBase = old })

	srv, count := sseServer(t, func(w http.ResponseWriter, attempt int) {
		w.WriteHeader(http.StatusBadGateway)
		fmt.Fprintf(w, "第 %d 次也是坏的", attempt)
	})

	var retries int
	_, err := newTestProvider(srv.URL).Chat(context.Background(), testMessages(), nil, func(ev domain.StreamEvent) {
		if ev.Kind == domain.StreamRetry {
			retries++
		}
	})
	if err == nil {
		t.Fatal("一直失败就该报错")
	}
	if got := atomic.LoadInt32(count); got != maxAttempts {
		t.Fatalf("应该试满 %d 次，实际 %d 次", maxAttempts, got)
	}
	// 重试提示只报"要再试一次"的那几次，最后一次失败不再提示重试
	if retries != maxAttempts-1 {
		t.Fatalf("应该报 %d 次重试，实际 %d 次", maxAttempts-1, retries)
	}
	for _, want := range []string{"已试 5 次", "502"} {
		if !strings.Contains(err.Error(), want) {
			t.Fatalf("错误里少了 %q: %v", want, err)
		}
	}
}

func TestRetriable(t *testing.T) {
	cases := []struct {
		name string
		err  error
		want bool
	}{
		{"限流", &statusError{code: 429}, true},
		{"服务端故障", &statusError{code: 500}, true},
		{"认证失败", &statusError{code: 401}, false},
		{"参数错误", &statusError{code: 400}, false},
		{"流里的错误帧", &transientError{msg: "流式响应错误"}, true},
		{"空响应", &transientError{msg: "模型返回空响应"}, true},
		{"网络层错误", errors.New("connection reset by peer"), true},
		{"读流卡死", errStreamStalled, true},
		{"用户中断", context.Canceled, false},
		{"超时", context.DeadlineExceeded, false},
	}
	for _, c := range cases {
		if got := retriable(c.err); got != c.want {
			t.Fatalf("%s: 应该返回 %v，实际 %v", c.name, c.want, got)
		}
	}
}

// 重试间隔要一次比一次长（封顶之前），但不超过上限
func TestRetryDelay(t *testing.T) {
	prev := time.Duration(0)
	for i := 1; i <= maxAttempts; i++ {
		d := retryDelay(i)
		base := retryBaseDelay << (i - 1)
		capped := base > maxRetryDelay || base <= 0
		if capped {
			base = maxRetryDelay
		}
		// 抖动只会加上去，最多多加 1/8
		if d < base || d > base+maxRetryDelay/8 {
			t.Fatalf("第 %d 次重试的等待 %s 不在 %s 附近", i, d, base)
		}
		// 还没封顶时，后一次必须比前一次长（封顶后只靠抖动区分，不再要求递增）
		if i > 1 && !capped && d <= prev {
			t.Fatalf("第 %d 次重试的等待 %s 该比上一次 %s 长", i, d, prev)
		}
		prev = d
	}
}

// 带图片的提问要按 openai 那套多模态格式发出去：content 变成数组，
// 文字在最前面，后面每张图一个 image_url，用 data: 地址内嵌
func TestChatSendsImageParts(t *testing.T) {
	var got map[string]any
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var body map[string]any
		if err := json.NewDecoder(r.Body).Decode(&body); err != nil {
			t.Errorf("请求体不是 JSON: %v", err)
		}
		got = body
		writeStream(w, "看到了")
	}))
	t.Cleanup(srv.Close)

	img, err := domain.NewImage([]byte("\x89PNG\r\n\x1a\n 假装这是图"))
	if err != nil {
		t.Fatalf("造图片失败: %v", err)
	}
	messages := []domain.AIChatMessage{
		{Role: domain.RoleUser, Content: "这个界面哪里不对？", Images: []domain.Image{img}},
		{Role: domain.RoleUser, Content: "没有图的照着老样子发"},
	}
	if _, err := newTestProvider(srv.URL).Chat(context.Background(), messages, nil, nil); err != nil {
		t.Fatalf("请求失败: %v", err)
	}

	msgs, _ := got["messages"].([]any)
	if len(msgs) != 2 {
		t.Fatalf("应该发两条消息，实际 %d 条", len(msgs))
	}

	// 带图的那条：content 是数组，第一段文字、第二段图片
	first, _ := msgs[0].(map[string]any)
	parts, ok := first["content"].([]any)
	if !ok {
		t.Fatalf("带图的消息 content 应该是数组，实际 %T", first["content"])
	}
	if len(parts) != 2 {
		t.Fatalf("应该是一段文字加一张图，实际 %d 段", len(parts))
	}
	text, _ := parts[0].(map[string]any)
	if text["type"] != "text" || text["text"] != "这个界面哪里不对？" {
		t.Fatalf("第一段应该是原话: %+v", text)
	}
	image, _ := parts[1].(map[string]any)
	if image["type"] != "image_url" {
		t.Fatalf("第二段应该是图片: %+v", image)
	}
	url, _ := image["image_url"].(map[string]any)
	wantURL := img.DataURL()
	if url["url"] != wantURL {
		t.Fatalf("图片应该用 data: 地址内嵌，实际 %v", url["url"])
	}

	// 没有图的消息还是老样子的字符串，请求体字节不变才能命中接口侧的上下文缓存
	second, _ := msgs[1].(map[string]any)
	if second["content"] != "没有图的照着老样子发" {
		t.Fatalf("没图的消息 content 应该还是字符串，实际 %T", second["content"])
	}
}
