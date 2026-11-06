package web

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// DeepSeekID 是 DeepSeek 搜索后端在注册表里的 id
	DeepSeekID = "deepseek"

	// DeepSeekDefaultBaseURL 是 DeepSeek 的 Anthropic 兼容端点基址，后面拼 /messages。
	// 走这个端点是因为它认 Anthropic 的服务端搜索工具 web_search_20250305：
	// 搜索在厂商那边执行，回来的结果是结构化的块，不用解析任何网页。
	// 实测（2026-11-06）：这条路径可用，返回 web_search_tool_result 块
	DeepSeekDefaultBaseURL = "https://api.deepseek.com/anthropic/v1"

	// DeepSeekDefaultModel 是走 Anthropic 协议的模型名。用会话模型的模型名也行，
	// 但那是 OpenAI 兼容协议的名字，别默认往这儿塞
	DeepSeekDefaultModel = "deepseek-v4-flash"

	// deepSeekAPIVersion 是 Anthropic 协议必带的版本头
	deepSeekAPIVersion = "2023-06-01"

	// deepSeekMaxTokens 是这次辅助调用的生成上限。它只用来生成一段总结和来源，
	// 不需要很长；给太大只是白花钱
	deepSeekMaxTokens = 4096

	// deepSeekDefaultMaxUses 是一次请求里允许厂商服务端搜几次。
	// 我们一个请求只发一个查询，多出来的额度是留给模型自己改写查询再搜的
	deepSeekDefaultMaxUses = 4

	// deepSeekTimeout 是单次搜索的上限。一次搜索是一个完整的模型轮次，
	// 比刮网页慢得多，20 秒肯定不够
	deepSeekTimeout = 120 * time.Second

	// maxDeepSeekAnswerRunes 是后端生成的那段总结最多留多少字。
	// 它是"顺带给的"，真正要正文还得抓链接，全留下会把上下文吃光
	maxDeepSeekAnswerRunes = 4000

	// maxErrorBodyBytes 是失败时最多读多少响应体用来组装报错
	maxErrorBodyBytes = 4 << 10
)

// deepSeekClient 给搜索用的客户端。抓取另有客户端（要拦 SSRF），策略不一样
var deepSeekClient = &http.Client{Timeout: deepSeekTimeout}

// DeepSeekOptions 是装配这个后端需要的东西。全部留空时用内置默认值，
// 但没有 APIKey 就直接不可用（Available 返回 false，由接缝跳过它）
type DeepSeekOptions struct {
	// BaseURL 是 Anthropic 兼容端点基址，/messages 由这里追加。
	// 已经是完整 .../messages 的也认，不重复拼
	BaseURL string
	// Model 是走 Anthropic 协议的模型名
	Model string
	// APIKey 是 DeepSeek 的 key
	APIKey string
	// MaxUses 是一次请求里允许厂商搜几次，非正数按默认值
	MaxUses int
}

// DeepSeekProvider 走 DeepSeek 的服务端搜索（Anthropic 兼容协议）。
//
// 它是主路径：回来的是一段带来源的总结，比刮网页拿到的 8 条裸链接有用得多。
// 代价也很实在——一次搜索等于一次完整的模型调用，有延迟也有 token 成本，
// 所以它不该被当成"免费的联网开关"。
type DeepSeekProvider struct {
	opts DeepSeekOptions
}

// NewDeepSeekProvider 造一个 DeepSeek 搜索后端
func NewDeepSeekProvider(opts DeepSeekOptions) *DeepSeekProvider {
	if strings.TrimSpace(opts.BaseURL) == "" {
		opts.BaseURL = DeepSeekDefaultBaseURL
	}
	if strings.TrimSpace(opts.Model) == "" {
		opts.Model = DeepSeekDefaultModel
	}
	if opts.MaxUses <= 0 {
		opts.MaxUses = deepSeekDefaultMaxUses
	}
	return &DeepSeekProvider{opts: opts}
}

func (p *DeepSeekProvider) ID() string { return DeepSeekID }

// Available 只看本地配置：没 key 就是不可用，不去发请求试探。
// 有 key 但 key 过期这种事，只有真搜一次才知道
func (p *DeepSeekProvider) Available() bool {
	return strings.TrimSpace(p.opts.APIKey) != ""
}

// messagesURL 拼出 Messages 端点：基址是 .../v1 时拼 /messages，
// 已经是完整的 .../messages 就原样用
func (p *DeepSeekProvider) messagesURL() string {
	base := strings.TrimRight(strings.TrimSpace(p.opts.BaseURL), "/")
	if strings.HasSuffix(base, "/messages") {
		return base
	}
	return base + "/messages"
}

// deepSeekRequest 是发给 Messages 端点的请求体。字段名按 Anthropic 协议
type deepSeekRequest struct {
	Model     string                `json:"model"`
	MaxTokens int                   `json:"max_tokens"`
	Messages  []deepSeekMessage     `json:"messages"`
	Tools     []deepSeekTool        `json:"tools"`
	Thinking  *deepSeekThinkingConf `json:"thinking,omitempty"`
}

type deepSeekMessage struct {
	Role    string            `json:"role"`
	Content []deepSeekContent `json:"content"`
}

type deepSeekContent struct {
	Type string `json:"type"`
	Text string `json:"text"`
}

// deepSeekTool 声明厂商服务端的搜索工具。type 是 Anthropic 的版本化名字，
// 不是我们自己起的名字
type deepSeekTool struct {
	Type    string `json:"type"`
	Name    string `json:"name"`
	MaxUses int    `json:"max_uses"`
}

// deepSeekThinkingConf 关掉思考：这次调用的产出是一段总结，
// 推理 token 没有人消费，开着纯属加钱加延迟
type deepSeekThinkingConf struct {
	Type string `json:"type"`
}

// deepSeekResponse 是响应体里我们用得上的部分。
// Content 是块数组，靠 type 区分——text / server_tool_use / web_search_tool_result
type deepSeekResponse struct {
	Content []deepSeekBlock `json:"content"`
	Usage   struct {
		ServerToolUse struct {
			WebSearchRequests int `json:"web_search_requests"`
		} `json:"server_tool_use"`
	} `json:"usage"`
	Error *struct {
		Type    string `json:"type"`
		Message string `json:"message"`
	} `json:"error"`
}

// deepSeekBlock 是响应里的一个内容块。字段并集，按 Type 取用：
//
//	text                    → Text
//	web_search_tool_result  → Content（来源数组；失败时是错误对象，解不出来）
type deepSeekBlock struct {
	Type    string          `json:"type"`
	Text    string          `json:"text"`
	Content json.RawMessage `json:"content"`
}

// deepSeekHit 是 web_search_tool_result 里的一条结果。
// 实测没有任何摘要字段——真 Anthropic 会在 text 块的 citations 里给
// cited_text，DeepSeek 这个端点不给，所以 Snippet 只能空着，不编
type deepSeekHit struct {
	Type     string `json:"type"`
	Title    string `json:"title"`
	URL      string `json:"url"`
	PageAge  any    `json:"page_age"`
	Encypted string `json:"encrypted_content"`
}

func (p *DeepSeekProvider) Search(ctx context.Context, req SearchRequest) (SearchResult, error) {
	query := strings.TrimSpace(req.Query)
	if query == "" {
		return SearchResult{}, newError(ErrInvalidRequest, "搜索词是空的")
	}
	if !p.Available() {
		return SearchResult{}, newError(ErrConfiguredUnavailable,
			"DeepSeek 搜索没有配置 API key")
	}
	if ctx == nil {
		ctx = context.Background()
	}

	body := deepSeekRequest{
		Model:     p.opts.Model,
		MaxTokens: deepSeekMaxTokens,
		Messages: []deepSeekMessage{{
			Role: "user",
			Content: []deepSeekContent{{
				Type: "text",
				// 这句话是发给"另一个模型"的指令，不是给用户看的。
				// 让它顺手给一段总结：来源列表对模型来说只是路标，
				// 总结才是省下一次抓取的东西
				Text: "Perform a web search for the query: " + query +
					"\n\nAfter searching, summarize what you found in the language of the query, " +
					"with concrete details (API 名字、参数、版本号等)。不要编造搜索结果里没有的内容。",
			}},
		}},
		Tools: []deepSeekTool{{
			Type:    "web_search_20250305",
			Name:    "web_search",
			MaxUses: p.opts.MaxUses,
		}},
		Thinking: &deepSeekThinkingConf{Type: "disabled"},
	}
	payload, err := json.Marshal(body)
	if err != nil {
		return SearchResult{}, wrapError(ErrProvider, err, "序列化搜索请求失败")
	}

	endpoint := p.messagesURL()
	httpReq, err := http.NewRequestWithContext(ctx, http.MethodPost, endpoint, bytes.NewReader(payload))
	if err != nil {
		return SearchResult{}, wrapError(ErrProvider, err, "拼搜索请求失败")
	}
	httpReq.Header.Set("Content-Type", "application/json")
	httpReq.Header.Set("x-api-key", p.opts.APIKey)
	httpReq.Header.Set("anthropic-version", deepSeekAPIVersion)

	resp, err := deepSeekClient.Do(httpReq)
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return SearchResult{}, wrapError(ErrAborted, cerr, "搜索已取消")
		}
		// 报错里带上端点：搜索端点是能单独配的，用户改错了地方很难自己发现
		return SearchResult{}, wrapError(ErrProvider, err, "连不上 DeepSeek 搜索端点 %s", endpoint)
	}
	defer resp.Body.Close()

	raw, err := io.ReadAll(io.LimitReader(resp.Body, maxPageBytes))
	if err != nil {
		if cerr := ctx.Err(); cerr != nil {
			return SearchResult{}, wrapError(ErrAborted, cerr, "搜索已取消")
		}
		return SearchResult{}, wrapError(ErrProvider, err, "读取 DeepSeek 响应失败")
	}

	if resp.StatusCode != http.StatusOK {
		return SearchResult{}, newError(ErrProvider, "%s", describeDeepSeekFailure(resp.StatusCode, endpoint, raw))
	}

	var parsed deepSeekResponse
	if err := json.Unmarshal(raw, &parsed); err != nil {
		return SearchResult{}, wrapError(ErrProvider, err, "解析 DeepSeek 响应失败")
	}
	if parsed.Error != nil {
		return SearchResult{}, newError(ErrProvider,
			"DeepSeek 搜索失败（%s）：%s", parsed.Error.Type, parsed.Error.Message)
	}

	result, ok := mapDeepSeekResponse(parsed)
	if !ok {
		// 没有 web_search_tool_result 块：说明厂商那边没真搜，或者换了实现。
		// 这时候不能拿它的文字当结果糊弄过去——那就成了"模型自己编的搜索结果"
		return SearchResult{}, newError(ErrProvider,
			"DeepSeek 没有返回搜索结果块（模型可能是直接答的，不是搜的）；换一个说法再试，或者用 duckduckgo 后端")
	}
	return result, nil
}

// describeDeepSeekFailure 把失败响应整理成一句能照着做的话
func describeDeepSeekFailure(status int, endpoint string, raw []byte) string {
	detail := strings.TrimSpace(string(raw))
	if len(detail) > maxErrorBodyBytes {
		detail = detail[:maxErrorBodyBytes]
	}
	// 响应体是 JSON 时把 message 挑出来，剩下的嵌套结构对模型没意义
	var e struct {
		Error struct {
			Message string `json:"message"`
			Type    string `json:"type"`
		} `json:"error"`
	}
	if json.Unmarshal(raw, &e) == nil && e.Error.Message != "" {
		detail = e.Error.Type + ": " + e.Error.Message
	}
	switch status {
	case http.StatusUnauthorized:
		return fmt.Sprintf("DeepSeek 搜索鉴权失败（401，端点 %s）：%s。检查 WebSearch 里配的 key", endpoint, detail)
	case http.StatusTooManyRequests:
		return fmt.Sprintf("DeepSeek 搜索被限流了（429，端点 %s）：%s", endpoint, detail)
	}
	return fmt.Sprintf("DeepSeek 搜索返回 %d（端点 %s）：%s", status, endpoint, detail)
}

// mapDeepSeekResponse 把响应里的块映射成规范化结果。
//
// 块是交替出现的：[text] server_tool_use web_search_tool_result [text ...]
// 每个 web_search_tool_result 后面跟着的 text 是模型对那次搜索的说法。
// 我们把所有 text 拼成 Content（就是那段总结），所有来源去重成 Sources
func mapDeepSeekResponse(resp deepSeekResponse) (SearchResult, bool) {
	var answer strings.Builder
	var sources []Source
	sawResult := false

	for _, block := range resp.Content {
		switch block.Type {
		case "text":
			if txt := strings.TrimSpace(block.Text); txt != "" {
				if answer.Len() > 0 {
					answer.WriteString("\n\n")
				}
				answer.WriteString(txt)
			}
		case "web_search_tool_result":
			// 失败时 content 是个错误对象而不是数组，解不出来就跳过：
			// 这一次没搜到不代表整次失败，后面的块可能还有结果
			var hits []deepSeekHit
			if err := json.Unmarshal(block.Content, &hits); err != nil {
				continue
			}
			if len(hits) > 0 {
				sawResult = true
			}
			for _, hit := range hits {
				if strings.TrimSpace(hit.URL) == "" {
					continue
				}
				sources = append(sources, Source{
					URL:         hit.URL,
					Title:       strings.TrimSpace(hit.Title),
					PublishedAt: pageAge(hit.PageAge),
				})
			}
		}
	}

	if !sawResult {
		return SearchResult{}, false
	}
	return SearchResult{
		Content: truncateRunes(answer.String(), maxDeepSeekAnswerRunes),
		Sources: dedupeSources(sources),
	}, true
}

// pageAge 把 page_age 归一成字符串。这个字段实测是 null，
// 有的实现会给字符串，所以按 any 收
func pageAge(v any) string {
	switch v := v.(type) {
	case string:
		return strings.TrimSpace(v)
	case nil:
		return ""
	}
	return ""
}

// 编译期确认它满足接口
var _ SearchProvider = (*DeepSeekProvider)(nil)

// errNoProvider 是测试里造"一个后端都没有"时用的
var errNoProvider = errors.New("没有搜索后端")
