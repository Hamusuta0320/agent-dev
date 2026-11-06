package web

import (
	"context"
	"errors"
	"strings"
	"testing"
)

// fakeProvider 是测试用的搜索后端：可配是否可用、返回什么、报什么错
type fakeProvider struct {
	id        string
	available bool
	result    SearchResult
	err       error
	// gotReq 记下最后一次收到的请求，用来验证查询和上界有没有传下去
	gotReq SearchRequest
}

func (f *fakeProvider) ID() string      { return f.id }
func (f *fakeProvider) Available() bool { return f.available }

func (f *fakeProvider) Search(ctx context.Context, req SearchRequest) (SearchResult, error) {
	f.gotReq = req
	if f.err != nil {
		return SearchResult{}, f.err
	}
	return f.result, nil
}

// useProviders 把注册表换成给定的一组，测完还原。
// 注册表是进程级的，不隔离会串到别的用例
func useProviders(t *testing.T, providers ...SearchProvider) {
	t.Helper()
	savedPreferred := preferredSearch
	t.Cleanup(func() {
		registryMu.Lock()
		searchProviders = nil
		registryMu.Unlock()
		preferredSearch = savedPreferred
	})
	registryMu.Lock()
	searchProviders = nil
	registryMu.Unlock()
	for _, p := range providers {
		RegisterSearchProvider(p)
	}
}

func sources(urls ...string) []Source {
	out := make([]Source, 0, len(urls))
	for _, u := range urls {
		out = append(out, Source{URL: u, Title: u})
	}
	return out
}

// 没固定后端时按注册顺序挑第一个可用的
func TestPickProviderUsesFirstAvailable(t *testing.T) {
	first := &fakeProvider{id: "first", available: false}
	second := &fakeProvider{id: "second", available: true}
	useProviders(t, first, second)
	preferredSearch = ""

	got, err := pickSearchProvider()
	if err != nil {
		t.Fatalf("应该挑到后端: %v", err)
	}
	if got.ID() != "second" {
		t.Fatalf("应该挑 second，实际 %s", got.ID())
	}
}

// 固定了 id 就必须是它：不可用的不跳过，直接报错。
// 静默换一个后端，用户会以为配的那个在跑
func TestPickProviderPreferredUnavailable(t *testing.T) {
	useProviders(t,
		&fakeProvider{id: "deepseek", available: false},
		&fakeProvider{id: "duckduckgo", available: true},
	)
	preferredSearch = "deepseek"

	_, err := pickSearchProvider()
	if code := ErrorCode(err); code != ErrConfiguredUnavailable {
		t.Fatalf("应该是 %s，实际 %q（err=%v）", ErrConfiguredUnavailable, code, err)
	}
}

// 固定了一个没注册的 id：报 MISSING，并列出有哪些
func TestPickProviderPreferredMissing(t *testing.T) {
	useProviders(t, &fakeProvider{id: "duckduckgo", available: true})
	preferredSearch = "bing"

	_, err := pickSearchProvider()
	if code := ErrorCode(err); code != ErrConfiguredMissing {
		t.Fatalf("应该是 %s，实际 %q（err=%v）", ErrConfiguredMissing, code, err)
	}
	if !strings.Contains(err.Error(), "duckduckgo") {
		t.Fatalf("报错里该说清现有的是谁: %v", err)
	}
}

// 一个可用的都没有：报 UNAVAILABLE
func TestPickProviderNoneAvailable(t *testing.T) {
	useProviders(t, &fakeProvider{id: "deepseek", available: false})
	preferredSearch = ""

	_, err := pickSearchProvider()
	if code := ErrorCode(err); code != ErrUnavailable {
		t.Fatalf("应该是 %s，实际 %q（err=%v）", ErrUnavailable, code, err)
	}
}

// 环境变量优先于配置文件：临时换后端不该要改文件再改回来
func TestPreferredProviderEnvWins(t *testing.T) {
	saved := preferredSearch
	t.Cleanup(func() { preferredSearch = saved })
	preferredSearch = "duckduckgo"

	t.Setenv(EnvSearchProvider, "deepseek")
	if got := PreferredSearchProvider(); got != "deepseek" {
		t.Fatalf("环境变量应该赢，实际 %q", got)
	}

	t.Setenv(EnvSearchProvider, "")
	if got := PreferredSearchProvider(); got != "duckduckgo" {
		t.Fatalf("没有环境变量时用配置里的，实际 %q", got)
	}
}

// auto 等于"没固定"：按顺序挑
func TestPickProviderAutoIsNotAFixedIDS(t *testing.T) {
	useProviders(t,
		&fakeProvider{id: "deepseek", available: true},
		&fakeProvider{id: "duckduckgo", available: true},
	)
	preferredSearch = SearchProviderAuto

	got, err := pickSearchProvider()
	if err != nil {
		t.Fatalf("auto 不该报错: %v", err)
	}
	if got.ID() != "deepseek" {
		t.Fatalf("auto 应该挑注册顺序里的第一个，实际 %s", got.ID())
	}
}

// Search 是接缝入口：空查询挡在前面，不走后端
func TestSearchRejectsEmptyQuery(t *testing.T) {
	fake := &fakeProvider{id: "fake", available: true}
	useProviders(t, fake)

	if _, err := Search(context.Background(), SearchRequest{Query: "   "}); err == nil {
		t.Fatal("空查询应该报错")
	}
	if fake.gotReq.Query != "" {
		t.Fatalf("空查询不该传到后端: %q", fake.gotReq.Query)
	}
}

// 取消要在挑后端之前生效：用户按了 Ctrl+C 没必要再连一次网络
func TestSearchHonorsCancelledContext(t *testing.T) {
	fake := &fakeProvider{id: "fake", available: true}
	useProviders(t, fake)

	ctx, cancel := context.WithCancel(context.Background())
	cancel()
	if _, err := Search(ctx, SearchRequest{Query: "x"}); ErrorCode(err) != ErrAborted {
		t.Fatalf("应该是 %s，实际 %v", ErrAborted, err)
	}
}

// 后端返回的内容原样透传，Provider 字段由接缝填上
func TestSearchFillsProvider(t *testing.T) {
	fake := &fakeProvider{
		id:        "fake",
		available: true,
		result:    SearchResult{Content: "总结", Sources: sources("https://a")},
	}
	useProviders(t, fake)

	got, err := Search(context.Background(), SearchRequest{Query: "q"})
	if err != nil {
		t.Fatalf("搜索失败: %v", err)
	}
	if got.Provider != "fake" {
		t.Fatalf("Provider 应该是 fake，实际 %q", got.Provider)
	}
	if got.Content != "总结" || len(got.Sources) != 1 {
		t.Fatalf("结果没透传: %+v", got)
	}
}

// 接缝自己按 MaxResults 截断，并且置 Truncated：
// 后端可以不理这个上界，但截断的保证在这一层
func TestSearchCapsSources(t *testing.T) {
	fake := &fakeProvider{
		id:        "fake",
		available: true,
		result:    SearchResult{Sources: sources("https://1", "https://2", "https://3")},
	}
	useProviders(t, fake)

	got, err := Search(context.Background(), SearchRequest{Query: "q", MaxResults: 2})
	if err != nil {
		t.Fatalf("搜索失败: %v", err)
	}
	if len(got.Sources) != 2 {
		t.Fatalf("该截到 2 条，实际 %d", len(got.Sources))
	}
	if !got.Truncated {
		t.Fatal("截断了就该置 Truncated")
	}
}

// 后端报错时要带上是谁失败的：不说的话排查无从下手
func TestSearchWrapsProviderError(t *testing.T) {
	fake := &fakeProvider{id: "fake", available: true, err: errors.New("连不上")}
	useProviders(t, fake)

	_, err := Search(context.Background(), SearchRequest{Query: "q"})
	if code := ErrorCode(err); code != ErrProvider {
		t.Fatalf("应该是 %s，实际 %q（err=%v）", ErrProvider, code, err)
	}
	if !strings.Contains(err.Error(), "fake") {
		t.Fatalf("报错里该带后端 id: %v", err)
	}
}

// 后端自己就是取消：透传成 ABORTED，别包成 provider 失败
func TestSearchClassifiesCancellation(t *testing.T) {
	fake := &fakeProvider{id: "fake", available: true, err: context.Canceled}
	useProviders(t, fake)

	_, err := Search(context.Background(), SearchRequest{Query: "q"})
	if code := ErrorCode(err); code != ErrAborted {
		t.Fatalf("应该是 %s，实际 %q（err=%v）", ErrAborted, code, err)
	}
}

// 后端自己抛的带码错误（比如地址校验报"内网不许抓"）要原样透传，
// 且不能把它自己塞进自己的 Err —— 那样 Error() 会无限递归直接栈溢出
func TestClassifyKeepsProviderWebError(t *testing.T) {
	original := newError(ErrInvalidRequest, "内网地址不许抓")
	got := classify("http", error(original))

	if got != error(original) {
		t.Fatalf("带码的错误该原样返回，实际 %v", got)
	}
	if ErrorCode(got) != ErrInvalidRequest {
		t.Fatalf("码该保留，实际 %q", ErrorCode(got))
	}
	// 关键：能安全地取错误文本（自引用的话这里会栈溢出）
	if text := got.Error(); !strings.Contains(text, "内网地址不许抓") {
		t.Fatalf("错误文本不对: %q", text)
	}
}

// 抓内网地址时，provider 抛的就是带码错误，走一遍完整的接缝不能崩
func TestFetchLocalhostDoesNotRecurse(t *testing.T) {
	useProviders(t)
	// 只注册抓取后端，搜索表清空（这个用例只关心 Fetch）
	UnregisterFetchProvider(HTTPFetchID)
	RegisterFetchProvider(NewHTTPFetchProvider())
	t.Cleanup(func() { UnregisterFetchProvider(HTTPFetchID) })

	_, err := Fetch(context.Background(), FetchRequest{URL: "http://127.0.0.1:1/x"})
	if err == nil {
		t.Fatal("本机地址该被拒绝")
	}
	if ErrorCode(err) != ErrInvalidRequest {
		t.Fatalf("应该是 %s，实际 %q（err=%v）", ErrInvalidRequest, ErrorCode(err), err)
	}
}

// 去重按 URL，保留首次出现的顺序；空 URL 直接丢
func TestDedupeSources(t *testing.T) {
	got := dedupeSources([]Source{
		{URL: "https://b"},
		{URL: "https://a"},
		{URL: "https://b"},
		{URL: ""},
	})
	if len(got) != 2 || got[0].URL != "https://b" || got[1].URL != "https://a" {
		t.Fatalf("去重结果不对: %+v", got)
	}
}

// 同 id 重复注册是替换，不是追加：配置改了端点之后重新装配，
// 不该出现两个同 id 的后端（那样顺序就没意义了）
func TestRegisterReplacesSameID(t *testing.T) {
	useProviders(t, &fakeProvider{id: "dup", available: false})
	RegisterSearchProvider(&fakeProvider{id: "dup", available: true})

	got := SearchProviderIDs()
	if len(got) != 1 || got[0] != "dup" {
		t.Fatalf("同 id 应该替换，实际 %v", got)
	}
	provider, err := pickSearchProvider()
	if err != nil {
		t.Fatalf("替换后的应该可用: %v", err)
	}
	if provider.ID() != "dup" {
		t.Fatalf("挑到的是 %s", provider.ID())
	}
}
