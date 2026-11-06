package web

import (
	"context"
	"errors"
	"fmt"
	"html"
	"io"
	"net"
	"net/http"
	"net/url"
	"os"
	"strings"
	"time"
)

const (
	// HTTPFetchID 是本地抓取后端在注册表里的 id
	HTTPFetchID = "http"

	// maxFetchURLLen 是 URL 长度上限，防止拿超长串打爆解析器
	maxFetchURLLen = 2048

	// maxFetchBytes 是读响应体的上限（解压后），再大就截断
	maxFetchBytes = 2 << 20

	// maxFetchChars 是转成文本后留给模型的字符数上限。
	// 一个正常文档页 1~3 万字，够读；再长就是列表页或者把整个站抓下来了
	maxFetchChars = 30000

	// fetchTimeout 是单次抓取的上限
	fetchTimeout = 30 * time.Second

	// maxRedirects 是允许跟随的跳转次数
	maxRedirects = 5
)

// fetchClient 是抓取用的客户端。它和搜索的客户端不共用：
// 这边要拦 SSRF、卡跳转、只认 http(s)，策略完全不同
var fetchClient = &http.Client{
	Timeout:       fetchTimeout,
	Transport:     newSafeTransport(),
	CheckRedirect: checkRedirect,
}

// allowPrivateFetchClient 是 AllowPrivate 时用的客户端：普通连接、不校验地址。
// 只有测试会走到这里（httptest 的服务器在 127.0.0.1 上）
var allowPrivateFetchClient = &http.Client{Timeout: fetchTimeout}

// FetchRequest 是一次抓取
type FetchRequest struct {
	URL string
}

// FetchResult 是一次抓取的结果。非 2xx 也是结果而不是错误：
// 状态码是被抓资源状态的一部分，模型需要知道"这个页面 404 了"
type FetchResult struct {
	// URL 是跟随跳转之后的最终地址
	URL        string
	StatusCode int
	// Content 是解码后的正文（HTML 已经转成文本）
	Content string
	// Kind 是正文类型：html 或 text
	Kind string
	// Truncated 说明正文被字符上限截断了
	Truncated bool
	// Provider 是产出这份结果的后端 id
	Provider string
}

// FetchProvider 是一个能抓的后端
type FetchProvider interface {
	ID() string
	Available() bool
	Fetch(ctx context.Context, req FetchRequest) (FetchResult, error)
}

// HTTPFetchProvider 是唯一的抓取后端：本地发 HTTP 请求。
//
// 它是"本机代用户去访问一个 URL"，所以真正的风险不是内容质量而是安全：
// 模型可能被网页内容诱导去读 https://127.0.0.1:8080/admin 或者内网地址。
// 这一层的职责就是把这类请求挡住（见 newSafeTransport）。
type HTTPFetchProvider struct {
	// AllowPrivate 放行内网地址，**只给测试用**（httptest 的服务器就跑在 127.0.0.1）。
	// 生产代码永远不设它：装配时只调 NewHTTPFetchProvider()，拿不到这个字段
	AllowPrivate bool
}

// NewHTTPFetchProvider 造一个本地抓取后端
func NewHTTPFetchProvider() *HTTPFetchProvider { return &HTTPFetchProvider{} }

func (p *HTTPFetchProvider) ID() string { return HTTPFetchID }

// Available 永远是 true：不需要凭据
func (p *HTTPFetchProvider) Available() bool { return true }

// client 按模式给客户端：正常走带 SSRF 防护的那个，测试放行内网
func (p *HTTPFetchProvider) client() *http.Client {
	if p.AllowPrivate {
		return allowPrivateFetchClient
	}
	return fetchClient
}

func (p *HTTPFetchProvider) Fetch(ctx context.Context, req FetchRequest) (FetchResult, error) {
	target := strings.TrimSpace(req.URL)
	if target == "" {
		return FetchResult{}, newError(ErrInvalidRequest, "url 是空的")
	}
	if len(target) > maxFetchURLLen {
		return FetchResult{}, newError(ErrInvalidRequest, "url 太长（%d 字符，上限 %d）", len(target), maxFetchURLLen)
	}
	// 用户和模型经常少写协议头，补一个再判断，比直接报错好用
	if !strings.Contains(target, "://") {
		target = "https://" + target
	}
	parsed, err := url.Parse(target)
	if err != nil {
		return FetchResult{}, wrapError(ErrInvalidRequest, err, "url 解析失败")
	}
	if parsed.Scheme != "http" && parsed.Scheme != "https" {
		return FetchResult{}, newError(ErrInvalidRequest, "只支持 http / https，不支持 %s", parsed.Scheme)
	}
	if strings.TrimSpace(parsed.Host) == "" {
		return FetchResult{}, newError(ErrInvalidRequest, "url 里没有主机名")
	}
	if ctx == nil {
		ctx = context.Background()
	}
	// 地址先过一遍校验：私网地址没必要发出去再被 Dial 拦，
	// 这里报错能说清楚"是地址不合规"，而不是"连不上"
	if !p.AllowPrivate {
		if err := checkHostAllowed(ctx, parsed.Hostname()); err != nil {
			return FetchResult{}, err
		}
	}

	httpReq, err := http.NewRequestWithContext(ctx, http.MethodGet, parsed.String(), nil)
	if err != nil {
		return FetchResult{}, wrapError(ErrInvalidRequest, err, "拼抓取请求失败")
	}
	httpReq.Header.Set("User-Agent", userAgent)
	httpReq.Header.Set("Accept", "text/html,application/xhtml+xml,text/plain;q=0.9,*/*;q=0.5")
	httpReq.Header.Set("Accept-Language", "zh-CN,zh;q=0.9,en;q=0.8")

	resp, err := p.client().Do(httpReq)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return FetchResult{}, wrapError(ErrAborted, cerr, "抓取已取消")
		}
		var blocked *WebError
		if errors.As(err, &blocked) {
			return FetchResult{}, blocked
		}
		return FetchResult{}, wrapError(ErrProvider, err, "抓取 %s 失败", parsed.Host)
	}
	defer resp.Body.Close()

	finalURL := parsed.String()
	if resp.Request != nil && resp.Request.URL != nil {
		finalURL = resp.Request.URL.String()
	}

	contentType := strings.ToLower(resp.Header.Get("Content-Type"))
	kind := classifyContent(contentType)
	if kind == "" {
		return FetchResult{}, newError(ErrProvider,
			"%s 不是可读的网页或文本（Content-Type: %s）；二进制文件（PDF/图片/压缩包）这里读不了",
			parsed.Host, firstNonEmpty(contentType, "未声明"))
	}

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxFetchBytes))
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return FetchResult{}, wrapError(ErrAborted, cerr, "抓取已取消")
		}
		return FetchResult{}, wrapError(ErrProvider, err, "读取 %s 的响应失败", parsed.Host)
	}
	bodyTruncated := int64(len(raw)) >= maxFetchBytes

	text := string(raw)
	if kind == "html" {
		text = htmlToText(text)
	}
	text = strings.TrimSpace(text)
	text, charTruncated := truncateRunesMaybe(text, maxFetchChars)

	return FetchResult{
		URL:        finalURL,
		StatusCode: resp.StatusCode,
		Content:    text,
		Kind:       kind,
		Truncated:  bodyTruncated || charTruncated,
		Provider:   HTTPFetchID,
	}, nil
}

// classifyContent 按 Content-Type 判断正文类型。认不出的返回空串（调用方报错）：
// 把二进制当文本塞给模型只会得到一堆乱码
func classifyContent(contentType string) string {
	switch {
	case contentType == "":
		// 有的站点不声明，按 HTML 试一次：绝大多数没声明的是网页
		return "html"
	case strings.Contains(contentType, "text/html"),
		strings.Contains(contentType, "application/xhtml"):
		return "html"
	case strings.HasPrefix(contentType, "text/"),
		strings.Contains(contentType, "json"),
		strings.Contains(contentType, "xml"):
		return "text"
	}
	return ""
}

// checkHostAllowed 确认主机名解析出来的每个地址都是公网地址。
//
// 只要有一个解析结果是内网地址就整个拒绝：攻击者可以返回 [公网, 私网] 两个 A 记录，
// 让拨号时随机挑，挑中哪个不由我们控制
func checkHostAllowed(ctx context.Context, host string) error {
	if host == "" {
		return newError(ErrInvalidRequest, "url 里没有主机名")
	}
	// 主机名写成 IP 时不必解析
	if ip := net.ParseIP(host); ip != nil {
		if !isPublicIP(ip) {
			return newError(ErrInvalidRequest,
				"%s 是内网/本机地址，不允许抓取（这一层不允许访问内网，防止被网页内容诱导）", host)
		}
		return nil
	}
	ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
	if err != nil {
		return wrapError(ErrProvider, err, "解析 %s 失败", host)
	}
	if len(ips) == 0 {
		return newError(ErrProvider, "%s 解析不出任何地址", host)
	}
	for _, addr := range ips {
		if !isPublicIP(addr.IP) {
			return newError(ErrInvalidRequest,
				"%s 解析到内网/本机地址 %s，不允许抓取", host, addr.IP)
		}
	}
	return nil
}

// isPublicIP 判断一个地址能不能作为抓取目标。
// 拒绝：回环、私有网段、链路本地（含云元数据 169.254.169.254）、组播、
// 未指定地址、IPv6 唯一本地地址
func isPublicIP(ip net.IP) bool {
	if ip == nil {
		return false
	}
	if v4 := ip.To4(); v4 != nil {
		ip = v4
	}
	if ip.IsLoopback() || ip.IsPrivate() || ip.IsUnspecified() ||
		ip.IsLinkLocalUnicast() || ip.IsLinkLocalMulticast() ||
		ip.IsInterfaceLocalMulticast() || ip.IsMulticast() {
		return false
	}
	// 100.64.0.0/10 运营商级 NAT，IsPrivate 不管它
	if v4 := ip.To4(); v4 != nil && v4[0] == 100 && v4[1] >= 64 && v4[1] <= 127 {
		return false
	}
	// 0.0.0.0/8
	if v4 := ip.To4(); v4 != nil && v4[0] == 0 {
		return false
	}
	return true
}

// newSafeTransport 造一个"只在已校验的地址上拨号"的 Transport。
//
// 关键在 DialContext：它自己解析域名、逐个校验，然后连到校验过的那个 IP。
// 不能只靠请求前校验一次域名——DNS rebinding 就是先返回公网地址让你过校验，
// 等你真去连的时候再返回 127.0.0.1。校验和拨号用同一次解析结果才能挡住它。
//
// **走环境代理时这条防线要退一步**：配了 HTTP(S)_PROXY 的话，拨号目标是代理
// （通常是 127.0.0.1:7897 这种本机端口），既不是真实站点、也查不到它解析成什么。
// 这种地址放行（它是用户自己配的），改由拨号前的 checkHostAllowed 校验**真实目标**
// 的公网性——"抓内网"照样挡得住，只是防 DNS rebinding 的那一层由代理负责了。
// 不这么做的话，凡是用代理的机器 WebFetch 会 100% 失败（实测就是踩了这个）
func newSafeTransport() *http.Transport {
	dialer := &net.Dialer{Timeout: 10 * time.Second, KeepAlive: 30 * time.Second}
	return &http.Transport{
		Proxy: http.ProxyFromEnvironment,
		DialContext: func(ctx context.Context, network, addr string) (net.Conn, error) {
			// 代理地址是用户自己配的，放行
			if isEnvProxyAddr(addr) {
				return dialer.DialContext(ctx, network, addr)
			}
			host, port, err := net.SplitHostPort(addr)
			if err != nil {
				return nil, err
			}
			ips, err := net.DefaultResolver.LookupIPAddr(ctx, host)
			if err != nil {
				return nil, err
			}
			if len(ips) == 0 {
				return nil, fmt.Errorf("%s 解析不出任何地址", host)
			}
			for _, candidate := range ips {
				if !isPublicIP(candidate.IP) {
					return nil, &WebError{
						Code: ErrInvalidRequest,
						Msg:  fmt.Sprintf("%s 解析到内网/本机地址 %s，不允许抓取", host, candidate.IP),
					}
				}
			}
			// 连第一个校验过的地址，而不是把 host 交回给系统再去解析一次
			return dialer.DialContext(ctx, network, net.JoinHostPort(ips[0].IP.String(), port))
		},
		TLSHandshakeTimeout:   10 * time.Second,
		ResponseHeaderTimeout: 20 * time.Second,
		MaxIdleConns:          10,
		IdleConnTimeout:       30 * time.Second,
	}
}

// isEnvProxyAddr 判断一个 host:port 是不是环境变量里配的代理。
// 只认 HTTP_PROXY / HTTPS_PROXY 声明的地址——别的内网地址一律照拦
func isEnvProxyAddr(addr string) bool {
	addr = strings.ToLower(strings.TrimSpace(addr))
	if addr == "" {
		return false
	}
	for _, key := range []string{"HTTPS_PROXY", "https_proxy", "HTTP_PROXY", "http_proxy"} {
		raw := strings.TrimSpace(os.Getenv(key))
		if raw == "" {
			continue
		}
		// 有的环境变量不带协议头（如 "127.0.0.1:7897"），补一个才能当 URL 解析
		if !strings.Contains(raw, "://") {
			raw = "http://" + raw
		}
		u, err := url.Parse(raw)
		if err != nil || u.Host == "" {
			continue
		}
		host := strings.ToLower(u.Host)
		// 没写端口时按协议补默认端口，好和拨号地址对上
		if u.Port() == "" {
			port := "80"
			if strings.HasPrefix(strings.ToLower(raw), "https://") {
				port = "443"
			}
			host = net.JoinHostPort(u.Hostname(), port)
		}
		if host == addr {
			return true
		}
	}
	return false
}

// checkRedirect 限制跳转次数，并且每一跳都重新校验目标地址：
// 不然一个公网地址 302 到 127.0.0.1 就把前面的校验绕过去了
func checkRedirect(req *http.Request, via []*http.Request) error {
	if len(via) >= maxRedirects {
		return &WebError{Code: ErrProvider,
			Msg: fmt.Sprintf("跳转超过 %d 次，放弃了", maxRedirects)}
	}
	if req.URL == nil {
		return &WebError{Code: ErrProvider, Msg: "跳转目标没有地址"}
	}
	if req.URL.Scheme != "http" && req.URL.Scheme != "https" {
		return &WebError{Code: ErrInvalidRequest,
			Msg: "跳转到不支持的协议 " + req.URL.Scheme}
	}
	if err := checkHostAllowed(req.Context(), req.URL.Hostname()); err != nil {
		return err
	}
	return nil
}

// htmlToText 把 HTML 变成能读的纯文本。
//
// 不做完整的 DOM 解析：抓下来的页面里，真正有用的是正文，而正文就是"去掉标签、
// 把块级元素换行、把脚本样式整段扔掉"。引一个 HTML 解析库能做得更准，
// 但那要维护一棵树、还得处理各种畸形标签，收益不明显
func htmlToText(page string) string {
	var sb strings.Builder
	sb.Grow(len(page) / 2)

	lower := strings.ToLower(page)
	i := 0
	for i < len(page) {
		if page[i] != '<' {
			// 普通文本：整段拷过去
			next := strings.IndexByte(page[i:], '<')
			if next < 0 {
				sb.WriteString(page[i:])
				break
			}
			sb.WriteString(page[i : i+next])
			i += next
			continue
		}

		end := strings.IndexByte(page[i:], '>')
		if end < 0 {
			// 没有闭合的 < ：剩下的当文本
			sb.WriteString(page[i:])
			break
		}
		tag := lower[i+1 : i+end]
		tagName := tag
		if sp := strings.IndexAny(tagName, " \t\r\n/"); sp >= 0 {
			tagName = tagName[:sp]
		}

		// script / style / noscript：整段连内容一起跳过
		if tagName == "script" || tagName == "style" || tagName == "noscript" {
			closeTag := "</" + tagName
			if at := strings.Index(lower[i:], closeTag); at >= 0 {
				after := i + at
				if gt := strings.IndexByte(lower[after:], '>'); gt >= 0 {
					i = after + gt + 1
					continue
				}
				break
			}
			// 没找到闭合标签，剩下一律当脚本内容丢掉
			break
		}
		// 注释整段丢掉
		if strings.HasPrefix(tag, "!--") {
			if at := strings.Index(page[i:], "-->"); at >= 0 {
				i += at + 3
				continue
			}
			break
		}

		// 块级元素前后换行：不换的话整页会挤成一行，读起来没法看
		if isBlockTag(tagName) {
			sb.WriteByte('\n')
		}
		i += end + 1
	}

	return normalizeText(html.UnescapeString(sb.String()))
}

// isBlockTag 判断是不是块级元素（这些标签前后要断开）
func isBlockTag(tag string) bool {
	switch tag {
	case "p", "div", "br", "hr", "li", "tr", "td", "th", "section", "article",
		"header", "footer", "nav", "main", "aside", "blockquote", "pre",
		"h1", "h2", "h3", "h4", "h5", "h6", "table", "ul", "ol", "dl", "dt", "dd",
		"form", "fieldset", "figure", "figcaption", "details", "summary":
		return true
	}
	return false
}

// normalizeText 把连续空白压紧：行内的多个空格合成一个，连续空行最多留一行。
// 缩进用的空格在这里没有意义，留着只会白占上下文
func normalizeText(s string) string {
	lines := strings.Split(s, "\n")
	out := make([]string, 0, len(lines))
	blank := 0
	for _, line := range lines {
		line = strings.Join(strings.Fields(line), " ")
		if line == "" {
			blank++
			// 开头不留空行，中间最多留一行
			if blank > 1 || len(out) == 0 {
				continue
			}
			out = append(out, "")
			continue
		}
		blank = 0
		out = append(out, line)
	}
	return strings.TrimSpace(strings.Join(out, "\n"))
}

// truncateRunesMaybe 按字符截断，返回结果和"有没有截"
func truncateRunesMaybe(s string, limit int) (string, bool) {
	runes := []rune(s)
	if len(runes) <= limit {
		return s, false
	}
	return string(runes[:limit]) + "\n…（正文已截断）", true
}

// firstNonEmpty 取第一个非空串
func firstNonEmpty(values ...string) string {
	for _, v := range values {
		if strings.TrimSpace(v) != "" {
			return v
		}
	}
	return ""
}

// 编译期确认它满足接口
var _ FetchProvider = (*HTTPFetchProvider)(nil)

// Fetch 抓一个 URL。和 Search 一样是接缝入口：调用方不认后端
func Fetch(ctx context.Context, req FetchRequest) (FetchResult, error) {
	if ctx == nil {
		ctx = context.Background()
	}
	if err := ctx.Err(); err != nil {
		return FetchResult{}, wrapError(ErrAborted, err, "抓取已取消")
	}
	provider, err := pickFetchProvider()
	if err != nil {
		return FetchResult{}, err
	}
	result, err := provider.Fetch(ctx, req)
	if err != nil {
		return FetchResult{}, classify(provider.ID(), err)
	}
	result.Provider = provider.ID()
	return result, nil
}

// RegisterFetchProvider 注册一个抓取后端
func RegisterFetchProvider(p FetchProvider) {
	if p == nil {
		return
	}
	registryMu.Lock()
	defer registryMu.Unlock()
	for i, old := range fetchProviders {
		if old.ID() == p.ID() {
			fetchProviders[i] = p
			return
		}
	}
	fetchProviders = append(fetchProviders, p)
}

// fetchProviders 是抓取后端的注册表，按注册顺序就是优先级
var fetchProviders []FetchProvider

// UnregisterFetchProvider 摘掉一个抓取后端，测试里清理用
func UnregisterFetchProvider(id string) {
	registryMu.Lock()
	defer registryMu.Unlock()
	out := fetchProviders[:0]
	for _, p := range fetchProviders {
		if p.ID() != id {
			out = append(out, p)
		}
	}
	fetchProviders = out
}

// pickFetchProvider 挑一个抓取后端。规则和搜索一样：固定 id 时不猜，
// 没固定时按顺序取第一个可用的
func pickFetchProvider() (FetchProvider, error) {
	registryMu.RLock()
	providers := append([]FetchProvider(nil), fetchProviders...)
	registryMu.RUnlock()
	return pickProvider(providers, preferredFetch, "抓取")
}

// preferredFetch 是配置里固定的抓取后端 id（现在只有 http 一个实现）
var preferredFetch string
