package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"testing"

	"hamusuta/agent-dev/src/web"
)

// fakeFetchProvider 是测试用的抓取后端：按 URL 返回预设结果
type fakeFetchProvider struct {
	result web.FetchResult
	err    error
	// gotURL 记下最后一次请求的地址
	gotURL string
}

func (f *fakeFetchProvider) ID() string      { return "fakefetch" }
func (f *fakeFetchProvider) Available() bool { return true }

func (f *fakeFetchProvider) Fetch(ctx context.Context, req web.FetchRequest) (web.FetchResult, error) {
	f.gotURL = req.URL
	if f.err != nil {
		return web.FetchResult{}, f.err
	}
	result := f.result
	result.Provider = "fakefetch"
	return result, nil
}

// useFetchProvider 装一个假抓取后端，测完清理
func useFetchProvider(t *testing.T, p *fakeFetchProvider) *fakeFetchProvider {
	t.Helper()
	web.RegisterFetchProvider(p)
	t.Cleanup(func() { web.UnregisterFetchProvider(p.ID()) })
	return p
}

func runWebFetch(t *testing.T, args map[string]any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("序列化参数失败: %v", err)
	}
	return WebFetchTool{}.Execute(context.Background(), raw)
}

// 正常抓一页：输出要有最终地址、状态码、类型和正文
func TestWebFetchReturnsContent(t *testing.T) {
	p := useFetchProvider(t, &fakeFetchProvider{
		result: web.FetchResult{
			URL:        "https://pkg.go.dev/context",
			StatusCode: 200,
			Kind:       "html",
			Content:    "context 包定义了 Context 类型。",
		},
	})

	out, err := runWebFetch(t, map[string]any{"intent": "读 context 文档", "url": "https://pkg.go.dev/context"})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	for _, want := range []string{"https://pkg.go.dev/context", "状态码：200", "html", "context 包定义了 Context 类型。"} {
		if !strings.Contains(out, want) {
			t.Fatalf("输出里应该有 %q:\n%s", want, out)
		}
	}
	if p.gotURL != "https://pkg.go.dev/context" {
		t.Fatalf("地址没传到底层: %q", p.gotURL)
	}
}

// 非 2xx 要明确提示"这个地址可能失效了"，而不是让模型以为拿到的是正文
func TestWebFetchFlagsBadStatus(t *testing.T) {
	useFetchProvider(t, &fakeFetchProvider{
		result: web.FetchResult{
			URL:        "https://x.example/gone",
			StatusCode: 404,
			Kind:       "html",
			Content:    "Not Found",
		},
	})

	out, err := runWebFetch(t, map[string]any{"url": "https://x.example/gone"})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "状态码：404") || !strings.Contains(out, "不是成功响应") {
		t.Fatalf("404 该被标出来:\n%s", out)
	}
}

// 正文为空（比如纯前端渲染的页面）要说清楚原因
func TestWebFetchEmptyBody(t *testing.T) {
	useFetchProvider(t, &fakeFetchProvider{
		result: web.FetchResult{URL: "https://x.example/spa", StatusCode: 200, Kind: "html"},
	})

	out, err := runWebFetch(t, map[string]any{"url": "https://x.example/spa"})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "JavaScript") {
		t.Fatalf("空正文该提示可能是前端渲染:\n%s", out)
	}
}

// 截断要说出来，否则模型会以为那就是全文
func TestWebFetchMentionsTruncation(t *testing.T) {
	useFetchProvider(t, &fakeFetchProvider{
		result: web.FetchResult{
			URL: "https://x.example/long", StatusCode: 200, Kind: "html",
			Content: "很长的一页", Truncated: true,
		},
	})

	out, err := runWebFetch(t, map[string]any{"url": "https://x.example/long"})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "已经截断") {
		t.Fatalf("该说明正文被截断了:\n%s", out)
	}
}

// url 是必填的
func TestWebFetchRejectsEmptyURL(t *testing.T) {
	useFetchProvider(t, &fakeFetchProvider{})

	if _, err := runWebFetch(t, map[string]any{"intent": "读一下"}); err == nil {
		t.Fatal("没给 url 应该报错")
	}
}

// 超过长度上限直接拦，别拿超长串去解析
func TestWebFetchRejectsLongURL(t *testing.T) {
	useFetchProvider(t, &fakeFetchProvider{})

	long := "https://x.example/" + strings.Repeat("a", 3000)
	if _, err := runWebFetch(t, map[string]any{"url": long}); err == nil {
		t.Fatal("超长 url 应该报错")
	}
}

// 底层报错（比如内网地址被拒）原样传给模型：那是它能据以行动的提示
func TestWebFetchSurfacesError(t *testing.T) {
	useFetchProvider(t, &fakeFetchProvider{
		err: fmt.Errorf("127.0.0.1 是内网地址，不允许抓取"),
	})

	_, err := runWebFetch(t, map[string]any{"url": "http://127.0.0.1/admin"})
	if err == nil {
		t.Fatal("底层报错该冒上来")
	}
	if !strings.Contains(err.Error(), "内网") {
		t.Fatalf("错误原因该保留: %v", err)
	}
}

// 坏参数要报出来
func TestWebFetchBadArgs(t *testing.T) {
	if _, err := (WebFetchTool{}).Execute(context.Background(), json.RawMessage(`{"url":`)); err == nil {
		t.Fatal("坏参数应该报错")
	}
}
