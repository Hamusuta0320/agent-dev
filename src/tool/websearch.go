package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"

	"hamusuta/agent-dev/src/web"
)

const (
	// defaultWebSearchResults 是一次搜索默认带多少条来源回来。
	// 来源要占上下文，给多了模型也读不过来
	defaultWebSearchResults = 8
	// maxWebSearchResults 是上限：再多就分几次查，别一次把整页结果灌进来
	maxWebSearchResults = 20
	// maxWebSearchQueries 是一次调用最多接受几个查询。让模型把几种说法
	// 一起发过来，比它串行试四轮省三个来回
	maxWebSearchQueries = 4
	// maxWebSearchQueryRunes 限制单个搜索词长度：模型手滑把整段代码粘进来时，
	// 搜索引擎也帮不上忙，直接拦住让它写清楚要查什么
	maxWebSearchQueryRunes = 400
)

// WebSearchTool 查项目以外的资料：库的用法、报错含义、在线文档。
// 真正的实现在 src/web 里——它只负责"面向模型的约定"：参数、上限、输出格式。
// 用哪个搜索后端由 src/web 按配置和可用性挑，这里不认厂商
type WebSearchTool struct{}

func (t WebSearchTool) Name() string {
	return "WebSearch"
}

// Mutating 是 false：不发文件、不跑命令，只读网上的东西
func (t WebSearchTool) Mutating() bool { return false }

func (t WebSearchTool) Description() string {
	return "用搜索引擎查项目以外的资料，返回若干条来源（标题、链接、摘要），有时还带一段后端生成的总结。" +
		"intent 写一句这次要查什么，会显示在界面上。" +
		"queries 是 1~4 个查询，几种说法一起发出去（同一条消息里并发查），比一个一个试快。" +
		"总结和摘要都只是引子，需要细节就用 WebFetch 或 Bash 抓那个链接，别把它们当结论。" +
		"本机项目里的东西别用它查，用 Grep / Glob / Read。"
}

func (t WebSearchTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"intent": intentProp(),
			"queries": map[string]any{
				"type": "array",
				"items": map[string]any{
					"type": "string",
				},
				"description": "1~4 个查询，比如 [\"golang context.WithTimeout 用法\", \"go context timeout cancel\"]。" +
					"完全相同的查询只查一次",
			},
			"max_results": map[string]any{
				"type":        "integer",
				"description": "最多返回多少条来源，默认 8，上限 20",
			},
		},
		"required": []string{"intent", "queries"},
	}
}

// WebSearchArgs 是 WebSearch 的入参
type WebSearchArgs struct {
	Intent     string   `json:"intent"`
	Queries    []string `json:"queries"`
	MaxResults int      `json:"max_results"`
}

func (t WebSearchTool) Execute(ctx context.Context, rawArgs json.RawMessage) (string, error) {
	cctx := ctxOrBackground(ctx)
	if err := cctx.Err(); err != nil {
		return "", err
	}

	var args WebSearchArgs
	if len(rawArgs) > 0 && string(rawArgs) != "null" {
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return "", fmt.Errorf("参数格式错误: %w", err)
		}
	}
	queries, err := parseSearchQueries(args.Queries)
	if err != nil {
		return "", err
	}

	limit := args.MaxResults
	if limit <= 0 {
		limit = defaultWebSearchResults
	}
	if limit > maxWebSearchResults {
		limit = maxWebSearchResults
	}

	results, err := runSearches(cctx, queries)
	if err != nil {
		return "", err
	}
	return formatSearchOutput(queries, results, limit), nil
}

// parseSearchQueries 校验并规整查询列表：去掉空白项、折叠完全重复的，
// 保留首次出现的顺序。条数为 0 或超过上限都直接报错，说清楚该给几个
func parseSearchQueries(raw []string) ([]string, error) {
	seen := make(map[string]bool, len(raw))
	out := make([]string, 0, len(raw))
	for _, q := range raw {
		q = strings.TrimSpace(q)
		if q == "" {
			continue
		}
		if n := len([]rune(q)); n > maxWebSearchQueryRunes {
			return nil, fmt.Errorf("查询太长（%d 字）：只留关键信息，比如 \"golang context timeout\"", n)
		}
		if seen[q] {
			continue
		}
		seen[q] = true
		out = append(out, q)
	}
	if len(out) == 0 {
		return nil, fmt.Errorf("queries 是空的：至少给一个查询，比如 [\"golang context.WithTimeout 用法\"]")
	}
	if len(out) > maxWebSearchQueries {
		return nil, fmt.Errorf("一次最多 %d 个查询，给了 %d 个：砍掉几个，或者分几次查", maxWebSearchQueries, len(out))
	}
	return out, nil
}

// searchBatch 是一次调用里所有查询的结果（顺序和 queries 对齐）
type searchBatch struct {
	// results 每条对应一个查询
	results []web.SearchResult
	// provider 是这次实际用的后端 id，输出里告诉模型"结果是谁给的"
	provider string
}

// runSearches 并发跑所有查询。
//
// 任何一个失败就取消其余的并报第一个失败：一半成功一半失败的结果最难解释——
// 模型不知道该信哪部分，用户看到的是"搜出来几条但少了"。
// 干脆全失败，让模型换个说法重来，或者让用户去修配置
func runSearches(ctx context.Context, queries []string) (searchBatch, error) {
	batchCtx, cancel := context.WithCancel(ctx)
	defer cancel()

	var (
		wg       sync.WaitGroup
		mu       sync.Mutex
		firstErr error
		out      = make([]web.SearchResult, len(queries))
	)

	for i, q := range queries {
		wg.Add(1)
		go func(i int, q string) {
			defer wg.Done()
			result, err := web.Search(batchCtx, web.SearchRequest{Query: q, MaxResults: 0})
			mu.Lock()
			defer mu.Unlock()
			if err != nil {
				if firstErr == nil {
					firstErr = fmt.Errorf("搜索 %q 失败: %w", q, err)
					// 取消其余的：它们的结果已经没人要了
					cancel()
				}
				return
			}
			out[i] = result
		}(i, q)
	}
	wg.Wait()

	if firstErr != nil {
		return searchBatch{}, firstErr
	}
	return searchBatch{results: out, provider: providerOf(out)}, nil
}

// providerOf 取出这次实际用的后端 id（每个结果里都带着同一个）
func providerOf(results []web.SearchResult) string {
	for _, r := range results {
		if r.Provider != "" {
			return r.Provider
		}
	}
	return ""
}

// formatSearchOutput 把结果拼成给模型看的一段文本：
// 后端生成的总结在最前，然后是来源清单，最后是截断/引用提示
func formatSearchOutput(queries []string, batch searchBatch, limit int) string {
	var sb strings.Builder
	sources := mergeSources(batch.results, limit)

	fmt.Fprintf(&sb, "搜索：%s\n", strings.Join(quotes(queries), "、"))
	fmt.Fprintf(&sb, "来源 %d 条", len(sources))
	if batch.provider != "" {
		fmt.Fprintf(&sb, "（后端 %s）", batch.provider)
	}
	sb.WriteString("\n")

	// 后端生成的那段总结：能省掉一次抓取，但要说明它不是原文
	if answer := mergeAnswers(batch.results); answer != "" {
		sb.WriteString("\n## 搜索后端给的总结（不是原文，具体细节请核对来源）\n")
		sb.WriteString(answer)
		sb.WriteString("\n")
	}

	if len(sources) == 0 {
		sb.WriteString("\n（没有来源：换个更具体的说法再查）")
		return strings.TrimRight(sb.String(), "\n")
	}

	sb.WriteString("\n## 来源\n")
	for i, s := range sources {
		title := s.Title
		if title == "" {
			title = s.URL
		}
		fmt.Fprintf(&sb, "%d. %s\n   %s\n", i+1, title, s.URL)
		if s.Snippet != "" {
			fmt.Fprintf(&sb, "   %s\n", s.Snippet)
		}
		if s.PublishedAt != "" {
			fmt.Fprintf(&sb, "   (%s)\n", s.PublishedAt)
		}
	}

	truncated := false
	for _, r := range batch.results {
		if r.Truncated {
			truncated = true
		}
	}
	if len(sources) >= limit || truncated {
		fmt.Fprintf(&sb, "\n（还有更多结果没列出来：需要更精确的来源就把查询写具体一点）\n")
	}
	sb.WriteString("\n（要用到具体细节就抓上面的链接：WebFetch 或 Bash 的 curl / Invoke-WebRequest；" +
		"引用内容时在回答里标出 URL）")
	return strings.TrimRight(sb.String(), "\n")
}

// mergeSources 把多个查询的来源合并：按轮询顺序交错，按 URL 去重，再截断。
//
// 轮询而不是简单拼接：几个查询的排序都代表相关性，交错着取能让每个查询的
// 第一条都进得来；直接串起来的话，第一个查询的 8 条会把名额占满
func mergeSources(results []web.SearchResult, limit int) []web.Source {
	merged := make([]web.Source, 0, limit)
	seen := make(map[string]bool, limit)
	for i := 0; len(merged) < limit; i++ {
		added := false
		for _, r := range results {
			if i >= len(r.Sources) {
				continue
			}
			added = true
			s := r.Sources[i]
			if s.URL == "" || seen[s.URL] {
				continue
			}
			seen[s.URL] = true
			merged = append(merged, s)
			if len(merged) >= limit {
				break
			}
		}
		if !added {
			break
		}
	}
	return merged
}

// mergeAnswers 拼各查询后端给的总结。多数情况下只有一个查询有总结，
// 多查询时按顺序接起来
func mergeAnswers(results []web.SearchResult) string {
	parts := make([]string, 0, len(results))
	for _, r := range results {
		if a := strings.TrimSpace(r.Content); a != "" {
			parts = append(parts, a)
		}
	}
	return strings.Join(parts, "\n\n")
}

// quotes 给查询加上「」，输出里好认
func quotes(items []string) []string {
	out := make([]string, 0, len(items))
	for _, s := range items {
		out = append(out, "「"+s+"」")
	}
	return out
}
