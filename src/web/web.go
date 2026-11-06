// Package web 是"联网能力"的接缝（seam）：搜索和抓取都从这里进，
// 调用方不认厂商。具体后端（provider）在 web.go 背后的几个文件里实现：
//
//	duckduckgo.go  搜索，刮 DuckDuckGo 的 HTML 结果页，不需要 key，永远是兜底
//	deepseek.go    搜索，走 DeepSeek 的 Anthropic 兼容端点、模型厂商的服务端搜索
//	fetch.go       抓取，本地 HTTP，带 SSRF 防护
//
// 为什么要有这一层：搜索的"契约"完全取决于后端。刮 HTML 的契约是别人页面上的
// class 名，改一次就全废；模型厂商的服务端搜索给的是结构化结果块，稳但每次
// 消耗一个模型轮次的延迟和 token。两者都留着、能按配置切，就不用二选一。
package web

import (
	"context"
	"errors"
	"fmt"
	"os"
	"strings"
	"sync"
)

// 错误码。调用方（工具层）把它们原样端给模型，让模型知道下一步该干什么：
// 是换个说法重查、还是让用户去配 provider
const (
	// ErrUnavailable 没有任何可用的搜索后端
	ErrUnavailable = "WEB_PROVIDER_UNAVAILABLE"
	// ErrConfiguredMissing 配置指定了某个后端，但它压根没注册
	ErrConfiguredMissing = "WEB_PROVIDER_CONFIGURED_MISSING"
	// ErrConfiguredUnavailable 配置指定的后端注册了，但当前不可用（没配 key 之类）
	ErrConfiguredUnavailable = "WEB_PROVIDER_CONFIGURED_UNAVAILABLE"
	// ErrAborted 调用方取消了（用户按了 Ctrl+C）
	ErrAborted = "WEB_ABORTED"
	// ErrProvider 后端自己失败了，或者结果不符合契约
	ErrProvider = "WEB_PROVIDER_ERROR"
	// ErrInvalidRequest 请求本身不合法（空查询、超长 URL 之类）
	ErrInvalidRequest = "WEB_INVALID_REQUEST"
)

// EnvSearchProvider 用来固定用哪个搜索后端，值是 provider 的 id。
// 配置里的 WebSearch.Provider 也是同一个字段，环境变量优先——
// 临时换一下不用改文件
const EnvSearchProvider = "AGENT_WEB_SEARCH_PROVIDER"

// SearchRequest 是一次搜索。每个请求带一个查询；要查多个说法就发多个请求，
// 由工具层并发发出去再合并。
type SearchRequest struct {
	Query string
	// MaxResults 是返回来源数的上限，由接缝负责截断（provider 可以自己先用上，
	// 但就算它不理，接缝也会截）。0 表示不限
	MaxResults int
}

// Source 是一条可引用的来源。URL 一定在；其余都是可选的——
// 后端不返回就不填，逼适配器编一个出来等于让这一层说谎
type Source struct {
	URL     string
	Title   string
	Snippet string
	// PublishedAt 是后端给的发布/抓取时间（ISO-8601 字符串），没有就留空
	PublishedAt string
}

// SearchResult 是规范化的搜索结果。Content 是后端生成的答案或摘要
// （刮 HTML 那家没有，模型厂商那家有），Sources 是可引用来源。
type SearchResult struct {
	Content string
	Sources []Source
	// Truncated 说明接缝为了满足 MaxResults 砍掉了一些来源
	Truncated bool
	// Provider 是产出这份结果的后端 id。调用方把它写进工具输出：
	// 结果不对劲时，"是谁给的"是排查的第一条线索
	Provider string
}

// SearchProvider 是一个能搜的后端
type SearchProvider interface {
	// ID 是稳定的标识，配置和环境变量按它指定用哪个
	ID() string
	// Available 只做本地判断（配没配 key 之类），不许发网络请求
	Available() bool
	// Search 搜一次。ctx 取消时要尽快返回 ErrAborted
	Search(ctx context.Context, req SearchRequest) (SearchResult, error)
}

// WebError 是这一层抛给调用方的错误，Code 让模型能按情况决定下一步
type WebError struct {
	Code string
	Msg  string
	Err  error
}

func (e *WebError) Error() string {
	msg := e.Msg
	if e.Err != nil {
		msg += ": " + e.Err.Error()
	}
	// 码写在最前面：模型看到的工具报错就是这一整句，
	// 有了码它才能判断"换个说法重试"还是"去让用户配后端"
	if e.Code != "" {
		return e.Code + ": " + msg
	}
	return msg
}

func (e *WebError) Unwrap() error { return e.Err }

// newError 造一个带码的错误
func newError(code, format string, args ...any) *WebError {
	return &WebError{Code: code, Msg: fmt.Sprintf(format, args...)}
}

// wrapError 在带码的错误上挂住底层原因，方便往上翻
func wrapError(code string, err error, format string, args ...any) *WebError {
	return &WebError{Code: code, Msg: fmt.Sprintf(format, args...), Err: err}
}

// ErrorCode 取出错误的码；不是 WebError 就给空串
func ErrorCode(err error) string {
	var we *WebError
	if errors.As(err, &we) {
		return we.Code
	}
	return ""
}

// 注册表。和 src/tool 里的后台任务表一个路子：进程级一份，
// 启动时由装配代码（Configure）往里放 provider，测试里放假的
var (
	registryMu sync.RWMutex
	// searchProviders 按注册顺序存：配置里写了 auto（或者没写）时，
	// 选第一个 Available 的。顺序就是优先级，不是随机挑
	searchProviders []SearchProvider
)

// RegisterSearchProvider 注册一个搜索后端。同 id 重复注册会替换掉旧的：
// 配置改了端点之后重新注册，不该出现两个同 id 的
func RegisterSearchProvider(p SearchProvider) {
	if p == nil {
		return
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	for i, old := range searchProviders {
		if old.ID() == p.ID() {
			searchProviders[i] = p
			return
		}
	}
	searchProviders = append(searchProviders, p)
}

// SearchProviderIDs 列出已注册的搜索后端 id，按优先级排。
// 报错信息和提示词里用它
func SearchProviderIDs() []string {
	registryMu.RLock()
	defer registryMu.RUnlock()
	out := make([]string, 0, len(searchProviders))
	for _, p := range searchProviders {
		out = append(out, p.ID())
	}
	return out
}

// UnregisterSearchProvider 摘掉一个搜索后端。
// 测试里注册假的之后要用它清理：注册表是进程级的，不清理会串到别的用例
func UnregisterSearchProvider(id string) {
	registryMu.Lock()
	defer registryMu.Unlock()
	out := searchProviders[:0]
	for _, p := range searchProviders {
		if p.ID() != id {
			out = append(out, p)
		}
	}
	searchProviders = out
}

// pickSearchProvider 按"此刻"的配置挑一个后端
func pickSearchProvider() (SearchProvider, error) {
	registryMu.RLock()
	providers := append([]SearchProvider(nil), searchProviders...)
	registryMu.RUnlock()
	return pickProvider(providers, PreferredSearchProvider(), "搜索")
}

// providerBase 是所有后端共有的一点：id，以及"本地看它能不能用"
type providerBase interface {
	ID() string
	Available() bool
}

// pickProvider 是搜索和抓取共用的选择策略。这套规则只有一处定义：
//
//	配置/环境变量指定了 id  → 必须是它；没注册报 MISSING，不可用报 UNAVAILABLE
//	没指定（空或 auto）     → 按注册顺序取第一个 Available 的；一个都没有报 UNAVAILABLE
//
// 不在多个可用后端之间"智能选择"：多一个分支就多一种线上行为和本地不一致的可能。
// 注册顺序就是优先级，而优先级是装配代码写死的，不是运行时猜的
func pickProvider[T providerBase](providers []T, preferred, kind string) (T, error) {
	var zero T
	want := strings.TrimSpace(preferred)
	if want != "" && !strings.EqualFold(want, SearchProviderAuto) {
		for _, p := range providers {
			if !strings.EqualFold(p.ID(), want) {
				continue
			}
			if !p.Available() {
				return zero, newError(ErrConfiguredUnavailable,
					"配置指定的%s后端 %s 当前不可用（检查一下它的配置，比如 API key）", kind, p.ID())
			}
			return p, nil
		}
		return zero, newError(ErrConfiguredMissing,
			"配置指定的%s后端 %s 没有注册，现有：%s", kind, want, joinedIDs(providers))
	}

	for _, p := range providers {
		if p.Available() {
			return p, nil
		}
	}
	return zero, newError(ErrUnavailable,
		"没有可用的%s后端（已注册：%s，但都不可用）", kind, joinedIDs(providers))
}

// joinedIDs 把已注册的 id 拼成一句人话
func joinedIDs[T providerBase](providers []T) string {
	if len(providers) == 0 {
		return "无"
	}
	out := make([]string, 0, len(providers))
	for _, p := range providers {
		out = append(out, p.ID())
	}
	return strings.Join(out, "、")
}

// PreferredSearchProvider 返回配置里固定的搜索后端 id。
// 环境变量优先于配置文件：临时换个后端试一下不该要改文件再改回来
func PreferredSearchProvider() string {
	if v := strings.TrimSpace(os.Getenv(EnvSearchProvider)); v != "" {
		return v
	}
	return strings.TrimSpace(preferredSearch)
}

// preferredSearch 是装配时从配置里读进来的固定后端 id（Configure 写）。
// 不放在 provider 身上是因为它是"选择策略"，属于这一层
var preferredSearch string

// Search 搜一次。这是给调用方（工具层）用的唯一入口：
// 它负责挑后端、催取消、按 MaxResults 截断，调用方看不到 provider 的存在
func Search(ctx context.Context, req SearchRequest) (SearchResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	req.Query = strings.TrimSpace(req.Query)
	if req.Query == "" {
		return SearchResult{}, newError(ErrInvalidRequest, "搜索词是空的")
	}
	// 取消在挑后端之前就检查：用户已经按了 Ctrl+C，没必要再去连一次网络
	if err := ctx.Err(); err != nil {
		return SearchResult{}, wrapError(ErrAborted, err, "搜索已取消")
	}

	provider, err := pickSearchProvider()
	if err != nil {
		return SearchResult{}, err
	}

	result, err := provider.Search(ctx, req)
	if err != nil {
		return SearchResult{}, classify(provider.ID(), err)
	}
	result.Provider = provider.ID()
	return capSources(result, req.MaxResults), nil
}

// classify 把 provider 返回的错误统一挂上码。provider 自己已经带码的（比如
// 它主动报 ErrAborted，或者地址校验报 ErrInvalidRequest）就原样传，
// 其余一律算 provider 失败并带上是谁失败的——不说是哪家，模型和用户都无从下手
func classify(providerID string, err error) error {
	var we *WebError
	if errors.As(err, &we) {
		// 原样返回，绝不能往 we.Err 里再塞一遍 err：
		// err 本身就是 we 时，那样会让 Error() 无限递归到栈溢出
		return we
	}
	if errors.Is(err, context.Canceled) || errors.Is(err, context.DeadlineExceeded) {
		return wrapError(ErrAborted, err, "搜索已取消")
	}
	return wrapError(ErrProvider, err, "搜索后端 %s 失败", providerID)
}

// capSources 把来源截到 maxResults。截了就把 Truncated 置上：
// 工具层据此提示模型"还有更多，换个更准的说法再查"
func capSources(result SearchResult, maxResults int) SearchResult {
	if maxResults <= 0 || len(result.Sources) <= maxResults {
		return result
	}
	result.Sources = result.Sources[:maxResults]
	result.Truncated = true
	return result
}

// dedupeSources 按 URL 去重（保留首次出现的位置）。多查询合并时要用：
// 同一篇文档在不同说法下经常一起命中
func dedupeSources(sources []Source) []Source {
	seen := make(map[string]bool, len(sources))
	out := make([]Source, 0, len(sources))
	for _, s := range sources {
		key := strings.TrimSpace(s.URL)
		if key == "" || seen[key] {
			continue
		}
		seen[key] = true
		out = append(out, s)
	}
	return out
}
