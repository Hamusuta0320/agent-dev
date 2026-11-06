package web

import (
	"context"
	"fmt"
	"html"
	"io"
	"net/http"
	"net/url"
	"strings"
	"time"
)

const (
	// DuckDuckGoDefaultEndpoint 是 DuckDuckGo 的 HTML 端点。选它做兜底是因为
	// 不用 API key、结果页是朴素的服务端渲染 HTML，好解析；那个靠 JavaScript
	// 拼结果的首页抓不到东西
	DuckDuckGoDefaultEndpoint = "https://html.duckduckgo.com/html/"

	// DuckDuckGoID 是它在注册表里的 id
	DuckDuckGoID = "duckduckgo"

	// userAgent 装成普通浏览器：Go 默认的 Go-http-client 很容易被判成爬虫直接拦掉
	userAgent = "Mozilla/5.0 (Windows NT 10.0; Win64; x64) AppleWebKit/537.36 " +
		"(KHTML, like Gecko) Chrome/122.0.0.0 Safari/537.36"

	// maxPageBytes 是单次响应最多读多少：正常结果页 30~60 KB，
	// 异常大的页面没必要整份读进内存
	maxPageBytes = 4 << 20

	// searchRequestTimeout 是单次搜索的上限（ctx 那边还有自己的取消）
	searchRequestTimeout = 20 * time.Second

	// maxSnippetRunes 是一条摘要最多留多少字：摘要只是引子，
	// 太长会把上下文吃光，要看正文本来也该抓链接
	maxSnippetRunes = 300

	// 结果页里定位用的两个 class，DDG 用了十几年没变过。
	// 这是这个后端全部的"契约"，也是它脆的原因：人家改了这里就全废
	resultTitleClass   = `class="result__a"`
	resultSnippetClass = `class="result__snippet"`
)

// httpClient 给搜索用的客户端，超时靠它和 ctx 两层兜着。
// 抓取（fetch.go）另有自己的客户端：那边要拦 SSRF，策略不一样
var searchClient = &http.Client{Timeout: searchRequestTimeout}

// SearchProvider 的兜底实现：刮 DuckDuckGo 的 HTML 结果页。
//
// 它的价值是"什么都不用配就能搜"：没 key、没额度、不会因为你换了模型就失效。
// 代价很实在：没有摘要以外的结构（没有后端生成的答案）、随时可能被页面改版打废。
// 所以它排在优先级最后，只在前面的都不用时才轮到它。
type DuckDuckGoProvider struct {
	// Endpoint 留空走 DuckDuckGoDefaultEndpoint；测试和自建镜像可以覆盖
	Endpoint string
}

// NewDuckDuckGoProvider 造一个兜底后端
func NewDuckDuckGoProvider(endpoint string) *DuckDuckGoProvider {
	return &DuckDuckGoProvider{Endpoint: strings.TrimSpace(endpoint)}
}

func (p *DuckDuckGoProvider) ID() string { return DuckDuckGoID }

// Available 永远是 true：它不需要任何凭据，能不能连上是执行时才知道的事，
// 这里不许发网络请求去探
func (p *DuckDuckGoProvider) Available() bool { return true }

func (p *DuckDuckGoProvider) Search(ctx context.Context, req SearchRequest) (SearchResult, error) {
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return SearchResult{}, newError(ErrInvalidRequest, "搜索词是空的")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// 没写上界时给个不太离谱的默认值，免得一口气把整页结果都带回去
	limit := req.MaxResults
	if limit <= 0 {
		limit = 10
	}

	base := strings.TrimSpace(p.Endpoint)
	if base == "" {
		base = DuckDuckGoDefaultEndpoint
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, withQuery(base, query), nil)
	if err != nil {
		return SearchResult{}, wrapError(ErrProvider, err, "拼搜索请求失败")
	}
	httpReq.Header.Set("User-Agent", userAgent)
	httpReq.Header.Set("Accept", "text/html,application/xhtml+xml")
	httpReq.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")

	resp, err := searchClient.Do(httpReq)
	if err != nil {
		// 用户按了 Ctrl+C：把取消原样传出去，别包成"网络失败"
		if cerr := ctx.Err(); cerr != nil {
			return SearchResult{}, wrapError(ErrAborted, cerr, "搜索已取消")
		}
		return SearchResult{}, wrapError(ErrProvider, err, "连不上 DuckDuckGo")
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes))
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return SearchResult{}, wrapError(ErrAborted, cerr, "搜索已取消")
		}
		return SearchResult{}, wrapError(ErrProvider, err, "读取搜索结果失败")
	}
	if resp.StatusCode != http.StatusOK {
		return SearchResult{}, newError(ErrProvider,
			"DuckDuckGo 返回 %d %s", resp.StatusCode, http.StatusText(resp.StatusCode))
	}

	sources := parseResults(string(body))
	if len(sources) == 0 {
		// 解析不出东西的两种情况：真的没结果，或者被当成爬虫丢了一个验证页。
		// 后者分不出来（页面结构随时会变），所以把两种可能都说给模型听
		return SearchResult{}, newError(ErrProvider,
			"DuckDuckGo 没解析到任何结果（可能这个查询确实没有结果，也可能被限流了，换个说法再试）")
	}
	if len(sources) > limit {
		sources = sources[:limit]
	}
	return SearchResult{Sources: sources}, nil
}

// withQuery 把查询串接到端点上。endpoint 已经带 query（自建镜像常见）时按 & 续上
func withQuery(base, query string) string {
	sep := "?"
	if strings.Contains(base, "?") {
		sep = "&"
	}
	return base + sep + "q=" + url.QueryEscape(query)
}

// parseResults 从 DuckDuckGo 的 HTML 结果页里拆出标题、链接和摘要。
// 没引 HTML 解析库：这个页面只要靠两个 class 名定位，为这一点加一个依赖不值
func parseResults(page string) []Source {
	var out []Source
	rest := page
	for {
		titleAt := strings.Index(rest, resultTitleClass)
		if titleAt < 0 {
			break
		}
		// 开标签可能是 <a rel="nofollow" class="result__a" href="...">，
		// 往前找最近的 < 拿到整个标签，往后找第一个 > 拿到标签结尾
		tagStart := strings.LastIndex(rest[:titleAt], "<")
		closeAt := strings.Index(rest[titleAt:], ">")
		if tagStart < 0 || closeAt < 0 {
			break
		}
		openTag := rest[tagStart : titleAt+closeAt+1]
		bodyStart := titleAt + closeAt + 1
		bodyEnd := strings.Index(rest[bodyStart:], "</a>")
		if bodyEnd < 0 {
			break
		}
		title := plainText(rest[bodyStart : bodyStart+bodyEnd])
		href := attrValue(openTag, "href")
		rest = rest[bodyStart+bodyEnd+len("</a>"):]

		// 这一条到下一 result__a 之间是本条的地盘，摘要只在里面找，
		// 不然会串到下一条去
		span := rest
		if next := strings.Index(rest, resultTitleClass); next >= 0 {
			span = rest[:next]
		}
		snippet := ""
		if at := strings.Index(span, resultSnippetClass); at >= 0 {
			if txt, ok := snippetAt(span[at:]); ok {
				snippet = txt
			}
		}

		// 广告位（/y.js?ad_domain=）不是真实结果，别混进去占名额
		if href == "" || strings.Contains(href, "/y.js?") {
			continue
		}
		out = append(out, Source{
			Title:   title,
			URL:     targetURL(href),
			Snippet: truncateRunes(snippet, maxSnippetRunes),
		})
	}
	return out
}

// snippetAt 从 `class="result__snippet"` 处往后取摘要正文：
// 跳开标签、读到 </a> 为止
func snippetAt(seg string) (string, bool) {
	closeAt := strings.Index(seg, ">")
	if closeAt < 0 {
		return "", false
	}
	body := seg[closeAt+1:]
	end := strings.Index(body, "</a>")
	if end < 0 {
		return "", false
	}
	return plainText(body[:end]), true
}

// targetURL 把结果页上的跳转链接还原成真正要去的地址：
// 每条结果的 href 都是 //duckduckgo.com/l/?uddg=<转义过的目标地址>
func targetURL(href string) string {
	if at := strings.Index(href, "uddg="); at >= 0 {
		v := href[at+len("uddg="):]
		if cut := strings.Index(v, "&"); cut >= 0 {
			v = v[:cut]
		}
		if u, err := url.QueryUnescape(v); err == nil && strings.TrimSpace(u) != "" {
			return u
		}
	}
	if strings.HasPrefix(href, "//") {
		return "https:" + href
	}
	return href
}

// attrValue 从一段开标签里取属性值（已经反转义，href 里的 &amp; 要还原成 &）
func attrValue(tag, name string) string {
	key := name + `="`
	at := strings.Index(tag, key)
	if at < 0 {
		return ""
	}
	rest := tag[at+len(key):]
	if end := strings.Index(rest, `"`); end >= 0 {
		return html.UnescapeString(rest[:end])
	}
	return ""
}

// plainText 把一小段 HTML 变成纯文本：去掉标签（标题里的 <b> 高亮）、
// 反转义实体（&amp; &#x27; 之类）、把换行和连续空白压成一个空格
func plainText(s string) string {
	var sb strings.Builder
	inTag := false
	for _, r := range s {
		switch {
		case r == '<':
			inTag = true
		case r == '>':
			inTag = false
		case !inTag:
			sb.WriteRune(r)
		}
	}
	return strings.Join(strings.Fields(html.UnescapeString(sb.String())), " ")
}

// truncateRunes 按字符（不是字节）截断，超出的部分用省略号替掉，
// 免得把一个多字节汉字劈成半个
func truncateRunes(s string, limit int) string {
	runes := []rune(s)
	if len(runes) <= limit {
		return s
	}
	return string(runes[:limit]) + "…"
}

// 编译期确认它满足接口
var _ SearchProvider = (*DuckDuckGoProvider)(nil)

// describeDuckDuckGo 给提示词用的一句话说明
func describeDuckDuckGo() string {
	return fmt.Sprintf("%s（刮 DuckDuckGo 网页，不用 key，只有标题/链接/摘要）", DuckDuckGoID)
}
