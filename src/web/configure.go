package web

import (
	"strings"

	"hamusuta/agent-dev/src/config"
)

// SearchProviderAuto 是配置里的默认值：按注册顺序取第一个可用的后端
const SearchProviderAuto = "auto"

// Configure 按配置装配搜索后端，启动时调一次。
//
// 顺序就是优先级：DeepSeek 在前（有 key 时它给的是带来源的总结，比裸链接有用），
// DuckDuckGo 垫底（永远可用，所以永远是最后的安全网）。
//
// 这个函数只注册、不发请求：哪个后端真能用要等第一次搜索才知道。
func Configure(cfg config.GlobalConfig) {
	opts := cfg.SearchOptions()

	preferredSearch = strings.TrimSpace(opts.Provider)
	if strings.EqualFold(preferredSearch, SearchProviderAuto) {
		preferredSearch = ""
	}

	if ds, ok := deepSeekOptionsFrom(cfg, opts); ok {
		RegisterSearchProvider(NewDeepSeekProvider(ds))
	}
	// 兜底后端默认就在：配了 Enabled: false 才不注册
	if !opts.DisableDuckDuckGo {
		RegisterSearchProvider(NewDuckDuckGoProvider(opts.DuckDuckGoEndpoint))
	}

	// 抓取只有一个后端：本机发 HTTP。它没有可配的东西（安全上限都是写死的），
	// 所以无条件注册——注册了也不代表能抓，是"地址合规才抓得到"
	RegisterFetchProvider(NewHTTPFetchProvider())
}

// deepSeekOptionsFrom 从配置里凑出 DeepSeek 搜索后端的参数。
//
// key 和模型名可以显式配，也可以从当前生效的那套模型借——用户的直觉是
// "我都配了 DeepSeek 的模型了，搜索凭什么还要再填一次 key"。
// 但只在那套模型确实指向 DeepSeek 时才借：拿 MiniMax 的 key 去打
// DeepSeek 的端点，只会换回一个 401
func deepSeekOptionsFrom(cfg config.GlobalConfig, opts config.SearchOptions) (DeepSeekOptions, bool) {
	out := DeepSeekOptions{
		BaseURL: opts.DeepSeek.ApiUrl,
		Model:   opts.DeepSeek.Model,
		APIKey:  opts.DeepSeek.ApiKey,
		MaxUses: opts.DeepSeek.MaxUses,
	}

	if cur, err := cfg.CurrentModel(); err == nil {
		isDeepSeek := strings.EqualFold(strings.TrimSpace(cur.Type), "DeepSeek") ||
			strings.Contains(strings.ToLower(cur.ApiUrl), "deepseek")
		if isDeepSeek && strings.TrimSpace(out.APIKey) == "" {
			out.APIKey = cur.ApiKey
		}
		// 显式配了 key 却没配模型/端点时，也让它跟着那套走：
		// 用户已经指明"就用 DeepSeek 搜索"，地址和模型名没理由再让手填一遍
		if isDeepSeek {
			if strings.TrimSpace(out.BaseURL) == "" {
				out.BaseURL = anthropicBaseURL(cur.ApiUrl)
			}
			if strings.TrimSpace(out.Model) == "" {
				out.Model = strings.TrimSpace(cur.Model)
			}
		}
	}

	// 一条 key 都凑不出来就不注册它：注册一个永远 Available()==false 的后端，
	// 只会让报错里多一行用户看不懂的 "deepseek 不可用"
	if strings.TrimSpace(out.APIKey) == "" {
		return DeepSeekOptions{}, false
	}
	return out, true
}

// anthropicBaseURL 把会话那套 OpenAI 兼容的地址换成 Anthropic 兼容的基址。
// 只认 deepseek 官方域名：别家的 /anthropic 兼容层长什么样各不相同，猜了只会更难排查
func anthropicBaseURL(openAIURL string) string {
	u := strings.ToLower(strings.TrimSpace(openAIURL))
	if strings.Contains(u, "deepseek.com") {
		return DeepSeekDefaultBaseURL
	}
	return ""
}
