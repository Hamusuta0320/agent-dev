package web

import (
	"context"
	"net/http"
	"net/http/httptest"
	"net/url"
	"strings"
	"testing"
)

// ddgPage 是从 html.duckduckgo.com 抓下来的结果页结构（只留了用得上的几层）。
// 第一条带高亮和实体、第二条是广告位、第三条是普通结果
const ddgPage = `<html><body>
<div class="result results_links results_links_deep web-result">
  <div class="links_main links_deep result__body">
    <h2 class="result__title">
      <a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fpkg.go.dev%2Fcontext&amp;rut=66e0">context package - context - Go Packages</a>
    </h2>
    <a class="result__snippet" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fpkg.go.dev%2Fcontext&amp;rut=66e0">Package <b>context</b> defines the <b>Context</b> type, which carries deadlines &amp; signals.</a>
    <div class="result__extras"><span class="result__url">pkg.go.dev/context</span></div>
  </div>
</div>
<div class="result results_links results_links_deep result--ad">
  <a rel="nofollow" class="result__a" href="//duckduckgo.com/y.js?ad_domain=example.com&amp;u3=x">广告位</a>
  <a class="result__snippet">这是广告的摘要</a>
</div>
<div class="result results_links results_links_deep web-result">
  <h2 class="result__title"><a rel="nofollow" class="result__a" href="//duckduckgo.com/l/?uddg=https%3A%2F%2Fgo.dev%2Fblog%2Fcontext&amp;rut=9cbf">Go Concurrency Patterns: Context</a></h2>
  <a class="result__snippet">The Go Blog talks about <i>context</i>.</a>
</div>
</body></html>`

// 结果页要拆成标题、真实地址、摘要三样，广告位不该混进来
func TestParseResults(t *testing.T) {
	got := parseResults(ddgPage)
	if len(got) != 2 {
		t.Fatalf("应该解析出 2 条（广告不算），实际 %d 条: %+v", len(got), got)
	}

	first := got[0]
	if first.Title != "context package - context - Go Packages" {
		t.Fatalf("标题不对: %q", first.Title)
	}
	if first.URL != "https://pkg.go.dev/context" {
		t.Fatalf("跳转链接没还原成真实地址: %q", first.URL)
	}
	// 摘要里的 <b> 高亮要去掉，&amp; 要还原成 &
	if first.Snippet != "Package context defines the Context type, which carries deadlines & signals." {
		t.Fatalf("摘要不对: %q", first.Snippet)
	}

	second := got[1]
	if second.URL != "https://go.dev/blog/context" {
		t.Fatalf("第二条地址不对: %q", second.URL)
	}
	if second.Snippet != "The Go Blog talks about context." {
		t.Fatalf("第二条摘要不对: %q", second.Snippet)
	}
}

// 一条结果都没有的页面返回空切片，由 provider 决定怎么报错
func TestParseResultsEmpty(t *testing.T) {
	if got := parseResults(`<html><body>没有结果</body></html>`); len(got) != 0 {
		t.Fatalf("不该解析出东西: %+v", got)
	}
}

// 请求要带上查询词和像浏览器的 UA，页面能解析出结果
func TestDuckDuckGoRequestsEndpoint(t *testing.T) {
	var gotQuery, gotAgent string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotQuery = r.URL.Query().Get("q")
		gotAgent = r.Header.Get("User-Agent")
		w.Write([]byte(ddgPage))
	}))
	defer srv.Close()

	p := NewDuckDuckGoProvider(srv.URL)
	result, err := p.Search(context.Background(), SearchRequest{Query: "golang context", MaxResults: 5})
	if err != nil {
		t.Fatalf("搜索失败: %v", err)
	}
	if gotQuery != "golang context" {
		t.Fatalf("查询词没传对: %q", gotQuery)
	}
	if !strings.Contains(gotAgent, "Mozilla") {
		t.Fatalf("UA 不像浏览器: %q", gotAgent)
	}
	if len(result.Sources) != 2 {
		t.Fatalf("应该有 2 条来源，实际 %d", len(result.Sources))
	}
	// 这个后端生不出"总结"：它只有网页上的标题摘要，没有答案文本
	if result.Content != "" {
		t.Fatalf("刮网页的后端不该有 Content: %q", result.Content)
	}
}

// MaxResults 是这个后端自己的上界（接缝后面还会再截一次）
func TestDuckDuckGoHonorsLimit(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(ddgPage))
	}))
	defer srv.Close()

	result, err := NewDuckDuckGoProvider(srv.URL).Search(context.Background(),
		SearchRequest{Query: "context", MaxResults: 1})
	if err != nil {
		t.Fatalf("搜索失败: %v", err)
	}
	if len(result.Sources) != 1 {
		t.Fatalf("limit=1 应该只回一条，实际 %d", len(result.Sources))
	}
}

// 搜索服务报错时把状态码带出来，别静默返回空结果
func TestDuckDuckGoHTTPError(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusTooManyRequests)
	}))
	defer srv.Close()

	_, err := NewDuckDuckGoProvider(srv.URL).Search(context.Background(), SearchRequest{Query: "context"})
	if err == nil {
		t.Fatal("429 应该报错")
	}
	if !strings.Contains(err.Error(), "429") {
		t.Fatalf("错误里应该有状态码: %v", err)
	}
}

// 解析不出结果的页面要说清楚可能是被限流，不能回一个空列表糊弄过去
func TestDuckDuckGoNoResults(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte(`<html><body>请完成验证</body></html>`))
	}))
	defer srv.Close()

	_, err := NewDuckDuckGoProvider(srv.URL).Search(context.Background(), SearchRequest{Query: "context"})
	if err == nil {
		t.Fatal("解析不出结果应该报错")
	}
	if !strings.Contains(err.Error(), "限流") {
		t.Fatalf("报错里该提示可能是限流: %v", err)
	}
}

// 兜底后端永远可用：它不要凭据
func TestDuckDuckGoAlwaysAvailable(t *testing.T) {
	if !NewDuckDuckGoProvider("").Available() {
		t.Fatal("刮网页的后端不该需要凭据")
	}
}

// 目标地址还原：uddg 参数优先，没有就补 / 开头的协议
func TestTargetURL(t *testing.T) {
	cases := map[string]string{
		"//duckduckgo.com/l/?uddg=https%3A%2F%2Fa.example%2Fx%3Fy%3D1&rut=abc": "https://a.example/x?y=1",
		"//a.example/page":  "https://a.example/page",
		"/local/page":       "/local/page",
		"https://a.example": "https://a.example",
	}
	for href, want := range cases {
		if got := targetURL(href); got != want {
			t.Fatalf("targetURL(%q) = %q，应该是 %q", href, got, want)
		}
	}
}

// 属性取值要反转义实体：结果页里 href 的 &amp; 是常见的
func TestAttrValue(t *testing.T) {
	tag := `<a rel="nofollow" class="result__a" href="//x.example/?a=1&amp;b=2">`
	if got := attrValue(tag, "href"); got != "//x.example/?a=1&b=2" {
		t.Fatalf("属性值不对: %q", got)
	}
	if got := attrValue(tag, "title"); got != "" {
		t.Fatalf("没有的属性应该给空串: %q", got)
	}
}

// 纯文本化：标签去掉、实体还原、空白压成一个空格
func TestPlainText(t *testing.T) {
	got := plainText("  Package <b>context</b>\n  defines &amp; &#x27;carries&#x27;  ")
	if got != "Package context defines & 'carries'" {
		t.Fatalf("纯文本化不对: %q", got)
	}
}

// 摘要超长时按字符截断，不能把汉字劈成半个
func TestTruncateRunes(t *testing.T) {
	if got := truncateRunes("短摘要", 10); got != "短摘要" {
		t.Fatalf("没超长不该动: %q", got)
	}
	got := truncateRunes("一二三四五", 3)
	if got != "一二三…" {
		t.Fatalf("截断结果不对: %q", got)
	}
}

// endpoint 自带 query 时要用 & 续参数，不能拼出两个 ?
func TestWithQuery(t *testing.T) {
	if got := withQuery(DuckDuckGoDefaultEndpoint, "a b"); got != DuckDuckGoDefaultEndpoint+"?q=a+b" {
		t.Fatalf("拼出来的地址不对: %q", got)
	}
	if got := withQuery("https://x.example/search?kl=cn", "a"); got != "https://x.example/search?kl=cn&q=a" {
		t.Fatalf("带 query 的 endpoint 拼错了: %q", got)
	}
	// 查询词里的特殊字符要转义，别把参数结构撑坏
	got := withQuery(DuckDuckGoDefaultEndpoint, "a&b=c")
	if u, err := url.Parse(got); err != nil || u.Query().Get("q") != "a&b=c" {
		t.Fatalf("特殊字符没转义好: %q", got)
	}
}
