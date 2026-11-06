package web

import (
	"context"
	"encoding/json"
	"io"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// deepSeekPage 仿造实测拿到的响应结构：text → server_tool_use →
// web_search_tool_result → text。encrypted_content 留着，确认它不影响解析
const deepSeekResponseJSON = `{
  "id": "msg_1",
  "type": "message",
  "role": "assistant",
  "model": "deepseek-flash",
  "content": [
    {"type":"text","text":"I'll search for that."},
    {"type":"server_tool_use","id":"call_1","name":"web_search","input":{"query":"golang context"}},
    {"type":"web_search_tool_result","tool_use_id":"call_1","content":[
      {"type":"web_search_result","title":"context package","url":"https://pkg.go.dev/context","encrypted_content":"xxxx","page_age":null},
      {"type":"web_search_result","title":"Go Blog","url":"https://go.dev/blog/context","encrypted_content":"yyyy","page_age":null},
      {"type":"web_search_result","title":"重复的","url":"https://pkg.go.dev/context","encrypted_content":"zzzz","page_age":null}
    ]},
    {"type":"text","text":"WithTimeout 返回一个带超时的子 Context，记得 defer cancel()。"}
  ],
  "usage": {"input_tokens": 4401, "output_tokens": 636,
            "server_tool_use": {"web_search_requests": 1}}
}`

// 实测的端点确实支持服务端搜索，这里守的是"解析这块对得上"：
// 文本块拼成总结、来源块去重成列表、没有摘要就不编
func TestMapDeepSeekResponse(t *testing.T) {
	var parsed deepSeekResponse
	if err := json.Unmarshal([]byte(deepSeekResponseJSON), &parsed); err != nil {
		t.Fatalf("测试数据本身就不合法: %v", err)
	}

	result, ok := mapDeepSeekResponse(parsed)
	if !ok {
		t.Fatal("有来源块，不该报失败")
	}
	if len(result.Sources) != 2 {
		t.Fatalf("同一 URL 该去重，实际 %d 条: %+v", len(result.Sources), result.Sources)
	}
	if result.Sources[0].URL != "https://pkg.go.dev/context" || result.Sources[0].Title != "context package" {
		t.Fatalf("第一条来源不对: %+v", result.Sources[0])
	}
	// 这个端点不给摘要（真 Anthropic 走 citations，它没有），所以必须空着
	if result.Sources[0].Snippet != "" {
		t.Fatalf("没有摘要就不该编一个出来: %q", result.Sources[0].Snippet)
	}
	// 两段 text 都要留下：第一段是开场，第二段才是真正的总结
	if !strings.Contains(result.Content, "I'll search for that.") ||
		!strings.Contains(result.Content, "defer cancel()") {
		t.Fatalf("两段文本都该拼进 Content: %q", result.Content)
	}
}

// 没有 web_search_tool_result 块 = 厂商没真搜（模型自己编的答案不能当搜索结果）
func TestMapDeepSeekResponseWithoutResultBlock(t *testing.T) {
	var parsed deepSeekResponse
	body := `{"content":[{"type":"text","text":"我直接答了，没搜"}],"usage":{}}`
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("测试数据不合法: %v", err)
	}
	if _, ok := mapDeepSeekResponse(parsed); ok {
		t.Fatal("没有来源块时必须报失败，不能拿模型的话当搜索结果")
	}
}

// 搜索块里 content 是错误对象（不是数组）时跳过它，别整次失败：
// 后面可能还有别的块带着结果
func TestMapDeepSeekResponseSkipsErrorBlock(t *testing.T) {
	var parsed deepSeekResponse
	body := `{"content":[
      {"type":"web_search_tool_result","content":{"type":"web_search_tool_result_error","error_code":"max_uses_exceeded"}},
      {"type":"web_search_tool_result","content":[{"type":"web_search_result","title":"t","url":"https://a.example"}]}
    ]}`
	if err := json.Unmarshal([]byte(body), &parsed); err != nil {
		t.Fatalf("测试数据不合法: %v", err)
	}
	result, ok := mapDeepSeekResponse(parsed)
	if !ok {
		t.Fatal("有成功的块就该算成功")
	}
	if len(result.Sources) != 1 {
		t.Fatalf("该只剩成功那条: %+v", result.Sources)
	}
}

// 请求体要按 Anthropic 协议拼：服务端工具的类型名、thinking 关掉、max_uses
func TestDeepSeekRequestShape(t *testing.T) {
	var gotBody map[string]any
	var gotKey, gotVersion string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotKey = r.Header.Get("x-api-key")
		gotVersion = r.Header.Get("anthropic-version")
		raw, _ := io.ReadAll(r.Body)
		json.Unmarshal(raw, &gotBody)
		w.Write([]byte(deepSeekResponseJSON))
	}))
	defer srv.Close()

	p := NewDeepSeekProvider(DeepSeekOptions{BaseURL: srv.URL, Model: "m", APIKey: "k", MaxUses: 3})
	if _, err := p.Search(context.Background(), SearchRequest{Query: "golang context"}); err != nil {
		t.Fatalf("搜索失败: %v", err)
	}

	if gotKey != "k" {
		t.Fatalf("key 头不对: %q", gotKey)
	}
	if gotVersion != "2023-06-01" {
		t.Fatalf("版本头不对: %q", gotVersion)
	}
	tools, _ := gotBody["tools"].([]any)
	if len(tools) != 1 {
		t.Fatalf("该带一个服务端工具: %v", gotBody["tools"])
	}
	tool := tools[0].(map[string]any)
	if tool["type"] != "web_search_20250305" || tool["name"] != "web_search" {
		t.Fatalf("工具声明不对: %v", tool)
	}
	if tool["max_uses"] != float64(3) {
		t.Fatalf("max_uses 应该是 3，实际 %v", tool["max_uses"])
	}
	// 关掉思考：这次调用的产出是总结，推理 token 没人消费
	thinking, _ := gotBody["thinking"].(map[string]any)
	if thinking["type"] != "disabled" {
		t.Fatalf("thinking 该是 disabled: %v", gotBody["thinking"])
	}
	// 查询词要进 messages
	if !strings.Contains(strings.Join(contentTexts(gotBody), " "), "golang context") {
		t.Fatalf("查询词没进请求: %v", gotBody["messages"])
	}
}

// contentTexts 把请求体里的 messages[].content[].text 抽出来，方便断言
func contentTexts(body map[string]any) []string {
	var out []string
	messages, _ := body["messages"].([]any)
	for _, m := range messages {
		msg, _ := m.(map[string]any)
		contents, _ := msg["content"].([]any)
		for _, c := range contents {
			if block, ok := c.(map[string]any); ok {
				if text, ok := block["text"].(string); ok {
					out = append(out, text)
				}
			}
		}
	}
	return out
}

// 没 key 就直接不可用：接缝据此跳过它，不用发一次请求才知道
func TestDeepSeekAvailability(t *testing.T) {
	if NewDeepSeekProvider(DeepSeekOptions{Model: "m"}).Available() {
		t.Fatal("没有 key 必须报不可用")
	}
	if !NewDeepSeekProvider(DeepSeekOptions{Model: "m", APIKey: "k"}).Available() {
		t.Fatal("有 key 就该可用")
	}
}

// 基址是 .../v1 时拼 /messages；已经是完整 /messages 时不能拼成 /messages/messages
func TestDeepSeekMessagesURL(t *testing.T) {
	cases := map[string]string{
		"https://api.deepseek.com/anthropic/v1":          "https://api.deepseek.com/anthropic/v1/messages",
		"https://api.deepseek.com/anthropic/v1/":         "https://api.deepseek.com/anthropic/v1/messages",
		"https://api.deepseek.com/anthropic/v1/messages": "https://api.deepseek.com/anthropic/v1/messages",
		"": DeepSeekDefaultBaseURL + "/messages",
	}
	for base, want := range cases {
		got := NewDeepSeekProvider(DeepSeekOptions{BaseURL: base, APIKey: "k"}).messagesURL()
		if got != want {
			t.Fatalf("基址 %q 该拼成 %q，实际 %q", base, want, got)
		}
	}
}

// 401 的报错要指到端点：搜索端点是能单独配的，改错了地方很难自己发现
func TestDeepSeekUnauthorized(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		w.Write([]byte(`{"error":{"type":"authentication_error","message":"api key invalid"}}`))
	}))
	defer srv.Close()

	p := NewDeepSeekProvider(DeepSeekOptions{BaseURL: srv.URL, APIKey: "bad"})
	_, err := p.Search(context.Background(), SearchRequest{Query: "q"})
	if err == nil {
		t.Fatal("401 应该报错")
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "api key invalid") {
		t.Fatalf("报错该带上状态码和后端给的原因: %v", err)
	}
}

// 响应里没有搜索结果块 → 带码的 provider 失败，且提示换个后端
func TestDeepSeekSearchWithoutSearchBlocks(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`{"content":[{"type":"text","text":"直接答了"}]}`))
	}))
	defer srv.Close()

	p := NewDeepSeekProvider(DeepSeekOptions{BaseURL: srv.URL, APIKey: "k"})
	_, err := p.Search(context.Background(), SearchRequest{Query: "q"})
	if err == nil {
		t.Fatal("没有来源块应该报错")
	}
	if !strings.Contains(err.Error(), "duckduckgo") {
		t.Fatalf("该提示可以用兜底后端: %v", err)
	}
}
