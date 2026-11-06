package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"strings"
	"sync"
	"testing"

	"hamusuta/agent-dev/src/web"
)

// fakeSearchProvider 是测试用的搜索后端。按查询词返回预设结果，
// 记下每次调用（验证并发/去重）
type fakeSearchProvider struct {
	id string
	// byQuery 按查询词给结果；没有对应项时返回空的 Sources
	byQuery map[string]web.SearchResult
	// failOn 命中这些查询词就报错
	failOn map[string]bool
	// delay 每个查询的阻塞时长，用来确认调用是并发出去的
	mu    sync.Mutex
	calls []string
}

func (f *fakeSearchProvider) ID() string      { return f.id }
func (f *fakeSearchProvider) Available() bool { return true }

func (f *fakeSearchProvider) Search(ctx context.Context, req web.SearchRequest) (web.SearchResult, error) {
	f.mu.Lock()
	f.calls = append(f.calls, req.Query)
	f.mu.Unlock()

	if f.failOn[req.Query] {
		return web.SearchResult{}, fmt.Errorf("假装 %q 搜不了", req.Query)
	}
	result := f.byQuery[req.Query]
	result.Provider = f.id
	return result, nil
}

func (f *fakeSearchProvider) callCount() int {
	f.mu.Lock()
	defer f.mu.Unlock()
	return len(f.calls)
}

// useSearchProvider 装一个假后端并让接缝只认它，测完清理干净
func useSearchProvider(t *testing.T, p *fakeSearchProvider) *fakeSearchProvider {
	t.Helper()
	web.RegisterSearchProvider(p)
	// 固定 id，免得注册表里还有什么别的后端抢在前面
	t.Setenv(web.EnvSearchProvider, p.ID())
	t.Cleanup(func() { web.UnregisterSearchProvider(p.ID()) })
	return p
}

func runWebSearch(t *testing.T, args map[string]any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("序列化参数失败: %v", err)
	}
	return WebSearchTool{}.Execute(context.Background(), raw)
}

func srcList(prefix string, n int) []web.Source {
	out := make([]web.Source, 0, n)
	for i := 1; i <= n; i++ {
		out = append(out, web.Source{
			Title:   fmt.Sprintf("%s-%d", prefix, i),
			URL:     fmt.Sprintf("https://%s.example/%d", prefix, i),
			Snippet: fmt.Sprintf("%s 第 %d 条的摘要", prefix, i),
		})
	}
	return out
}

// 正常一条查询：输出里要有查询词、后端 id、来源标题和地址
func TestWebSearchReturnsSources(t *testing.T) {
	useSearchProvider(t, &fakeSearchProvider{
		id: "fake",
		byQuery: map[string]web.SearchResult{
			"golang context": {Sources: srcList("ctx", 2)},
		},
	})

	out, err := runWebSearch(t, map[string]any{"intent": "查 context", "queries": []string{"golang context"}})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	for _, want := range []string{"「golang context」", "后端 fake", "ctx-1", "https://ctx.example/1", "ctx 第 1 条的摘要"} {
		if !strings.Contains(out, want) {
			t.Fatalf("输出里应该有 %q:\n%s", want, out)
		}
	}
	// 得提醒模型摘要不是结论
	if !strings.Contains(out, "引用内容时在回答里标出 URL") {
		t.Fatalf("缺少引用提示:\n%s", out)
	}
}

// 后端给了总结时单独标一节，并写明"不是原文"
func TestWebSearchShowsProviderAnswer(t *testing.T) {
	useSearchProvider(t, &fakeSearchProvider{
		id: "fake",
		byQuery: map[string]web.SearchResult{
			"q": {Content: "WithTimeout 返回带超时的子 Context。", Sources: srcList("a", 1)},
		},
	})

	out, err := runWebSearch(t, map[string]any{"queries": []string{"q"}})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "搜索后端给的总结") || !strings.Contains(out, "WithTimeout 返回带超时的子 Context。") {
		t.Fatalf("总结该单独一节:\n%s", out)
	}
	if !strings.Contains(out, "不是原文") {
		t.Fatalf("该说明总结不是原文:\n%s", out)
	}
}

// 多个查询一起发：每个都查到，来源去重后合在一起
func TestWebSearchRunsAllQueries(t *testing.T) {
	p := useSearchProvider(t, &fakeSearchProvider{
		id: "fake",
		byQuery: map[string]web.SearchResult{
			"a": {Sources: []web.Source{{Title: "共享", URL: "https://same.example/x"}}},
			"b": {Sources: []web.Source{
				{Title: "共享", URL: "https://same.example/x"},
				{Title: "独有", URL: "https://b.example/2"},
			}},
		},
	})

	out, err := runWebSearch(t, map[string]any{"queries": []string{"a", "b"}})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if p.callCount() != 2 {
		t.Fatalf("两个查询都该发出去，实际 %d 次", p.callCount())
	}
	// 同一 URL 只出现一次
	if got := strings.Count(out, "https://same.example/x"); got != 1 {
		t.Fatalf("重复的 URL 该去重，出现了 %d 次:\n%s", got, out)
	}
	if !strings.Contains(out, "https://b.example/2") {
		t.Fatalf("第二个查询的独有来源该在:\n%s", out)
	}
}

// 完全相同的查询只查一次
func TestWebSearchDedupesQueries(t *testing.T) {
	p := useSearchProvider(t, &fakeSearchProvider{id: "fake"})

	if _, err := runWebSearch(t, map[string]any{"queries": []string{"同一个", "同一个"}}); err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if p.callCount() != 1 {
		t.Fatalf("重复查询只该发一次，实际 %d 次", p.callCount())
	}
}

// 查询是必填的：空数组、全空白都报错
func TestWebSearchRejectsEmptyQueries(t *testing.T) {
	useSearchProvider(t, &fakeSearchProvider{id: "fake"})

	if _, err := runWebSearch(t, map[string]any{"intent": "查一下"}); err == nil {
		t.Fatal("没给 queries 应该报错")
	}
	if _, err := runWebSearch(t, map[string]any{"queries": []string{"  ", ""}}); err == nil {
		t.Fatal("全是空白应该报错")
	}
}

// 一次最多 4 个查询：再多就让模型砍掉几个，别一次灌一屏
func TestWebSearchRejectsTooManyQueries(t *testing.T) {
	useSearchProvider(t, &fakeSearchProvider{id: "fake"})

	_, err := runWebSearch(t, map[string]any{"queries": []string{"a", "b", "c", "d", "e"}})
	if err == nil {
		t.Fatal("超过 4 个查询应该报错")
	}
	if !strings.Contains(err.Error(), "4") {
		t.Fatalf("报错该说清上限是几: %v", err)
	}
}

// 单个查询太长直接拦住：模型把整段代码粘进来时搜索引擎也帮不上忙
func TestWebSearchRejectsLongQuery(t *testing.T) {
	useSearchProvider(t, &fakeSearchProvider{id: "fake"})

	long := strings.Repeat("很长的搜索词", 100)
	if _, err := runWebSearch(t, map[string]any{"queries": []string{long}}); err == nil {
		t.Fatal("超长查询应该报错")
	}
}

// 任何一个查询失败就整体失败：一半成功一半失败最难解释，
// 模型不知道该信哪部分
func TestWebSearchFailsWholeBatch(t *testing.T) {
	useSearchProvider(t, &fakeSearchProvider{
		id:      "fake",
		failOn:  map[string]bool{"坏查询": true},
		byQuery: map[string]web.SearchResult{"好查询": {Sources: srcList("ok", 1)}},
	})

	_, err := runWebSearch(t, map[string]any{"queries": []string{"好查询", "坏查询"}})
	if err == nil {
		t.Fatal("有一个失败就该整体失败")
	}
	if !strings.Contains(err.Error(), "坏查询") {
		t.Fatalf("报错该指出是哪个查询失败了: %v", err)
	}
}

// 结果条数按 max_results 截断，并提示"还有更多"
func TestWebSearchHonorsMaxResults(t *testing.T) {
	useSearchProvider(t, &fakeSearchProvider{
		id:      "fake",
		byQuery: map[string]web.SearchResult{"q": {Sources: srcList("a", 10)}},
	})

	out, err := runWebSearch(t, map[string]any{"queries": []string{"q"}, "max_results": 3})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "来源 3 条") {
		t.Fatalf("该截到 3 条:\n%s", out)
	}
	if strings.Contains(out, "a-4") {
		t.Fatalf("第 4 条不该出现:\n%s", out)
	}
	if !strings.Contains(out, "还有更多结果没列出来") {
		t.Fatalf("截断了该提示:\n%s", out)
	}
}

// 一个来源都没有时说清换个说法，别返回一片空白
func TestWebSearchNoSources(t *testing.T) {
	useSearchProvider(t, &fakeSearchProvider{id: "fake"})

	out, err := runWebSearch(t, map[string]any{"queries": []string{"查不到的东西"}})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "没有来源") {
		t.Fatalf("该提示没有来源:\n%s", out)
	}
}

// 后端报错时把码和原因带到工具错误里，模型据此决定换说法还是让用户配
func TestWebSearchSurfacesProviderError(t *testing.T) {
	web.RegisterSearchProvider(&unavailableProvider{})
	t.Setenv(web.EnvSearchProvider, "unavailable")
	t.Cleanup(func() { web.UnregisterSearchProvider("unavailable") })

	_, err := runWebSearch(t, map[string]any{"queries": []string{"q"}})
	if err == nil {
		t.Fatal("没有可用后端时该报错")
	}
	if !strings.Contains(err.Error(), "WEB_PROVIDER") {
		t.Fatalf("报错里该带错误码: %v", err)
	}
}

// unavailableProvider 永远不可用，用来测"一个后端都用不了"
type unavailableProvider struct{}

func (p *unavailableProvider) ID() string      { return "unavailable" }
func (p *unavailableProvider) Available() bool { return false }
func (p *unavailableProvider) Search(ctx context.Context, req web.SearchRequest) (web.SearchResult, error) {
	return web.SearchResult{}, errors.New("不该被调用")
}

// 坏参数要报出来，不能当没传参
func TestWebSearchBadArgs(t *testing.T) {
	if _, err := (WebSearchTool{}).Execute(context.Background(), json.RawMessage(`{"queries":`)); err == nil {
		t.Fatal("坏参数应该报错")
	}
}

// 合并时按轮询交错，而不是把第一个查询的排完再接第二个：
// 每个查询的第一条都代表各自的相关性，谁都不该被挤掉
func TestMergeSourcesRoundRobin(t *testing.T) {
	results := []web.SearchResult{
		{Sources: srcList("a", 3)},
		{Sources: srcList("b", 3)},
	}
	merged := mergeSources(results, 4)
	want := []string{
		"https://a.example/1", "https://b.example/1",
		"https://a.example/2", "https://b.example/2",
	}
	if len(merged) != len(want) {
		t.Fatalf("该合并成 %d 条，实际 %d: %+v", len(want), len(merged), merged)
	}
	for i, url := range want {
		if merged[i].URL != url {
			t.Fatalf("第 %d 条该是 %s，实际 %s", i+1, url, merged[i].URL)
		}
	}
}

// 一个查询来源很少时，后面的空位由别的查询补上，不留空
func TestMergeSourcesFillsShortLists(t *testing.T) {
	results := []web.SearchResult{
		{Sources: srcList("a", 1)},
		{Sources: srcList("b", 3)},
	}
	merged := mergeSources(results, 4)
	if len(merged) != 4 {
		t.Fatalf("该凑满 4 条，实际 %d: %+v", len(merged), merged)
	}
	if merged[1].URL != "https://b.example/1" || merged[2].URL != "https://b.example/2" {
		t.Fatalf("短的查询结束后该由另一个补上: %+v", merged)
	}
}
