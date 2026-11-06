package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"

	"hamusuta/agent-dev/src/web"
)

const (
	// maxWebFetchURLLen 是 url 参数的长度上限，和 web 层一个口径
	maxWebFetchURLLen = 2048
)

// WebFetchTool 抓一个 URL，把网页转成纯文本给模型看。
// 搜索只给摘要，要看细节必须抓正文——这是"查这个库怎么用"的最后一公里
type WebFetchTool struct{}

func (t WebFetchTool) Name() string {
	return "WebFetch"
}

// Mutating 是 false：不碰磁盘，也不改远端（只发 GET）
func (t WebFetchTool) Mutating() bool { return false }

func (t WebFetchTool) Description() string {
	return "抓取一个网页/URL 并转成纯文本，用于读文档、README、报错页面。" +
		"intent 写一句这次要读什么，会显示在界面上。" +
		"只支持 http/https 的公网地址：内网和本机地址会被拒绝（防止被网页内容诱导去访问内网）。" +
		"非 2xx 也会返回结果（状态码是要看的信息之一，比如 404 说明链接失效）。" +
		"正文有上限，太长的页面会被截断；二进制文件（PDF/图片/压缩包）读不了。" +
		"要从搜索结果读正文就用它，别用 Bash 的 curl 拼（那样拿到的是一堆标签）。"
}

func (t WebFetchTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"intent": intentProp(),
			"url": map[string]any{
				"type":        "string",
				"description": "要抓的地址，比如 https://pkg.go.dev/context。不带协议头时按 https 补",
			},
		},
		"required": []string{"intent", "url"},
	}
}

// WebFetchArgs 是 WebFetch 的入参
type WebFetchArgs struct {
	Intent string `json:"intent"`
	URL    string `json:"url"`
}

func (t WebFetchTool) Execute(ctx context.Context, rawArgs json.RawMessage) (string, error) {
	cctx := ctxOrBackground(ctx)
	if err := cctx.Err(); err != nil {
		return "", err
	}

	var args WebFetchArgs
	if len(rawArgs) > 0 && string(rawArgs) != "null" {
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return "", fmt.Errorf("参数格式错误: %w", err)
		}
	}
	target := strings.TrimSpace(args.URL)
	if target == "" {
		return "", fmt.Errorf("url 是空的：给一个要抓的地址，比如 https://pkg.go.dev/context")
	}
	if len(target) > maxWebFetchURLLen {
		return "", fmt.Errorf("url 太长（%d 字符，上限 %d）", len(target), maxWebFetchURLLen)
	}

	result, err := web.Fetch(cctx, web.FetchRequest{URL: target})
	if err != nil {
		return "", err
	}
	return formatFetchOutput(result), nil
}

// formatFetchOutput 把抓取结果拼成给模型看的一段文本：
// 头部写清楚地址、状态码和是否截断，正文原样跟上
func formatFetchOutput(result web.FetchResult) string {
	var sb strings.Builder

	fmt.Fprintf(&sb, "抓取 %s\n", result.URL)
	if result.Provider != "" {
		fmt.Fprintf(&sb, "后端：%s，类型：%s\n", result.Provider, result.Kind)
	}
	fmt.Fprintf(&sb, "状态码：%d", result.StatusCode)
	if result.StatusCode < 200 || result.StatusCode >= 300 {
		sb.WriteString("（不是成功响应：这个地址可能已经失效，或者需要登录/换一个地址）")
	}
	sb.WriteString("\n")

	if strings.TrimSpace(result.Content) == "" {
		sb.WriteString("\n（正文是空的：页面可能靠 JavaScript 渲染，这种内容抓不到）")
		return strings.TrimRight(sb.String(), "\n")
	}

	sb.WriteString("\n---\n")
	sb.WriteString(result.Content)
	if result.Truncated {
		sb.WriteString("\n\n（正文超长，已经截断。只要其中某一部分的话，找页内锚点或换个更具体的地址）")
	}
	return strings.TrimRight(sb.String(), "\n")
}
