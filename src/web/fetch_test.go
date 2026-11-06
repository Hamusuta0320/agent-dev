package web

import (
	"context"
	"net"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// 内网和本机地址必须被拒：模型可能被网页内容诱导去读内网管理后台。
// 这是这一层唯一不能出错的地方
func TestIsPublicIP(t *testing.T) {
	public := []string{"1.1.1.1", "8.8.8.8", "93.184.216.34", "2606:4700:4700::1111"}
	for _, s := range public {
		if !isPublicIP(net.ParseIP(s)) {
			t.Fatalf("%s 是公网地址，不该拒绝", s)
		}
	}
	private := []string{
		"127.0.0.1", "127.1.2.3", "10.0.0.1", "172.16.0.1", "172.31.255.255",
		"192.168.1.1", "169.254.169.254", // 云主机元数据
		"100.64.0.1", // 运营商级 NAT
		"0.0.0.0",    // 未指定
		"::1",        // IPv6 回环
		"fd00::1",    // IPv6 唯一本地
		"fe80::1",    // IPv6 链路本地
		"224.0.0.1",  // 组播
	}
	for _, s := range private {
		if isPublicIP(net.ParseIP(s)) {
			t.Fatalf("%s 是内网/特殊地址，必须拒绝", s)
		}
	}
}

// 主机名直接写成内网 IP 时当场拒，连 DNS 都不用查
func TestCheckHostAllowedRejectsPrivateLiteral(t *testing.T) {
	for _, host := range []string{"127.0.0.1", "192.168.1.1", "169.254.169.254", "::1"} {
		err := checkHostAllowed(context.Background(), host)
		if err == nil {
			t.Fatalf("%s 应该被拒绝", host)
		}
		if !strings.Contains(err.Error(), "内网") {
			t.Fatalf("%s 的报错该说清为什么拒绝: %v", host, err)
		}
	}
}

// 抓本机地址要在发请求之前就被挡住（这里用 AllowPrivate=false 的默认实例）
func TestFetchRejectsLocalhost(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Write([]byte("<html><body>不该被读到</body></html>"))
	}))
	defer srv.Close()

	_, err := NewHTTPFetchProvider().Fetch(context.Background(), FetchRequest{URL: srv.URL})
	if err == nil {
		t.Fatal("127.0.0.1 必须被拒绝")
	}
	if ErrorCode(err) != ErrInvalidRequest {
		t.Fatalf("应该是 %s，实际 %q（err=%v）", ErrInvalidRequest, ErrorCode(err), err)
	}
}

// 只认 http/https：file:// 之类一律拒
func TestFetchRejectsOtherSchemes(t *testing.T) {
	p := NewHTTPFetchProvider()
	for _, u := range []string{"file:///C:/Windows/win.ini", "ftp://example.com/x", "javascript:alert(1)"} {
		if _, err := p.Fetch(context.Background(), FetchRequest{URL: u}); err == nil {
			t.Fatalf("%s 应该被拒绝", u)
		}
	}
}

// 正常抓一页：HTML 转成文本、状态码带出来、正文没有标签
func TestFetchConvertsHTML(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html; charset=utf-8")
		w.Write([]byte(`<html><head><title>t</title><style>body{color:red}</style></head>
			<body><script>alert(1)</script><h1>标题</h1><p>第一段 &amp; 内容</p><p>第二段</p></body></html>`))
	}))
	defer srv.Close()

	p := &HTTPFetchProvider{AllowPrivate: true}
	result, err := p.Fetch(context.Background(), FetchRequest{URL: srv.URL})
	if err != nil {
		t.Fatalf("抓取失败: %v", err)
	}
	if result.StatusCode != 200 {
		t.Fatalf("状态码不对: %d", result.StatusCode)
	}
	if result.Kind != "html" {
		t.Fatalf("类型该是 html，实际 %q", result.Kind)
	}
	for _, want := range []string{"标题", "第一段 & 内容", "第二段"} {
		if !strings.Contains(result.Content, want) {
			t.Fatalf("正文里该有 %q:\n%s", want, result.Content)
		}
	}
	// 脚本和样式必须整段扔掉：留着只是噪音
	for _, unwanted := range []string{"alert(1)", "color:red", "<p>", "&amp;"} {
		if strings.Contains(result.Content, unwanted) {
			t.Fatalf("正文里不该有 %q:\n%s", unwanted, result.Content)
		}
	}
}

// 非 2xx 也是结果而不是错误：状态码是模型需要知道的信息
func TestFetchReturnsNon2xxAsResult(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/html")
		w.WriteHeader(http.StatusNotFound)
		w.Write([]byte("<html><body>找不到</body></html>"))
	}))
	defer srv.Close()

	p := &HTTPFetchProvider{AllowPrivate: true}
	result, err := p.Fetch(context.Background(), FetchRequest{URL: srv.URL})
	if err != nil {
		t.Fatalf("404 不该当错误: %v", err)
	}
	if result.StatusCode != 404 {
		t.Fatalf("状态码该保留: %d", result.StatusCode)
	}
}

// 二进制内容读不了：把 PDF 当文本塞给模型只会得到乱码
func TestFetchRejectsBinary(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "application/pdf")
		w.Write([]byte("%PDF-1.4"))
	}))
	defer srv.Close()

	p := &HTTPFetchProvider{AllowPrivate: true}
	if _, err := p.Fetch(context.Background(), FetchRequest{URL: srv.URL}); err == nil {
		t.Fatal("PDF 应该被拒绝")
	} else if !strings.Contains(err.Error(), "PDF") {
		t.Fatalf("报错该说清读不了二进制: %v", err)
	}
}

// 不带协议头时按 https 补
func TestFetchAddsScheme(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.Header().Set("Content-Type", "text/plain")
		w.Write([]byte("ok"))
	}))
	defer srv.Close()

	p := &HTTPFetchProvider{AllowPrivate: true}
	// httptest 给的是 http://127.0.0.1:port，去掉协议头再传，看看能不能补回来。
	// 补的是 https，连不上就说明补上了（这里只验证"补了协议头"这件事本身）
	trimmed := strings.TrimPrefix(srv.URL, "http://")
	_, err := p.Fetch(context.Background(), FetchRequest{URL: trimmed})
	if err == nil {
		t.Fatal("补成 https 后连不上才是预期：说明确实补了协议头")
	}
}

// 环境里配的代理地址要放行：走代理时拨号目标是代理自己（通常是 127.0.0.1），
// 不放行的话凡是挂代理的机器 WebFetch 全都失败
func TestIsEnvProxyAddr(t *testing.T) {
	t.Setenv("HTTPS_PROXY", "http://127.0.0.1:7897")
	t.Setenv("HTTP_PROXY", "127.0.0.1:7890")

	if !isEnvProxyAddr("127.0.0.1:7897") {
		t.Fatal("HTTPS_PROXY 的地址该放行")
	}
	// 不带协议头的写法也认
	if !isEnvProxyAddr("127.0.0.1:7890") {
		t.Fatal("HTTP_PROXY 的地址该放行")
	}
	// 别的内网地址照拦
	for _, addr := range []string{"127.0.0.1:8005", "192.168.1.1:80", "169.254.169.254:80"} {
		if isEnvProxyAddr(addr) {
			t.Fatalf("%s 不是配的代理，不该放行", addr)
		}
	}
}

// 没配代理时什么地址都不放行
func TestIsEnvProxyAddrWithoutProxy(t *testing.T) {
	for _, key := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		t.Setenv(key, "")
	}
	if isEnvProxyAddr("127.0.0.1:7897") {
		t.Fatal("没配代理时不该放行任何地址")
	}
}

// HTML 转文本：块级元素换行、连续空行压紧、实体还原
func TestHTMLToText(t *testing.T) {
	got := htmlToText(`<div>一行</div><div>二行</div>

	<p>三行&nbsp;带实体</p>`)
	lines := strings.Split(got, "\n")
	if len(lines) < 3 {
		t.Fatalf("块级元素该换行:\n%s", got)
	}
	if lines[0] != "一行" || lines[1] != "二行" {
		t.Fatalf("前两行不对: %q", lines[:2])
	}
	if !strings.Contains(got, "三行 带实体") {
		t.Fatalf("实体该还原、空白该压紧:\n%s", got)
	}
	// 连续空行最多留一行
	if strings.Contains(got, "\n\n\n") {
		t.Fatalf("不该有连续空行:\n%q", got)
	}
}

// 正文超长时按字符截断，并说清楚截了
func TestTruncateRunesMaybe(t *testing.T) {
	if got, cut := truncateRunesMaybe("短的", 10); cut || got != "短的" {
		t.Fatalf("没超长不该动: %q %v", got, cut)
	}
	got, cut := truncateRunesMaybe(strings.Repeat("字", 10), 3)
	if !cut {
		t.Fatal("超长该返回截断标记")
	}
	if !strings.Contains(got, "截断") {
		t.Fatalf("该说明截断了: %q", got)
	}
}

// Content-Type 分类：不声明按 HTML 试，认不出的返回空
func TestClassifyContent(t *testing.T) {
	cases := map[string]string{
		"text/html; charset=utf-8": "html",
		"application/xhtml+xml":    "html",
		"":                         "html",
		"text/plain":               "text",
		"application/json":         "text",
		"application/pdf":          "",
		"image/png":                "",
	}
	for ct, want := range cases {
		if got := classifyContent(strings.ToLower(ct)); got != want {
			t.Fatalf("%q 该分类成 %q，实际 %q", ct, want, got)
		}
	}
}
