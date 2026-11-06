package config

import "strings"

// WebSearchConfig 是联网搜索的配置段。整段不写也能用：默认按优先级
// 挑后端，DeepSeek 有 key 就用它，没有就用 DuckDuckGo 兜底
type WebSearchConfig struct {
	// Provider 固定用哪个后端（auto / deepseek / duckduckgo）。
	// 留空或写 auto 就按优先级挑第一个可用的
	Provider string `yaml:"Provider,omitempty"`
	// DeepSeek 是模型厂商服务端搜索的参数。ApiUrl / Model / ApiKey
	// 留空时从当前生效的那套模型借（前提是那套确实指向 DeepSeek）
	DeepSeek DeepSeekSearchConfig `yaml:"DeepSeek,omitempty"`
	// DuckDuckGo 是刮网页那个兜底后端的参数
	DuckDuckGo DuckDuckGoSearchConfig `yaml:"DuckDuckGo,omitempty"`
}

// DeepSeekSearchConfig 是 DeepSeek 服务端搜索的参数
type DeepSeekSearchConfig struct {
	// ApiUrl 是 Anthropic 兼容端点的基址（/messages 由程序追加）
	ApiUrl string `yaml:"ApiUrl,omitempty"`
	// Model 是走 Anthropic 协议的那个模型名，和会话用的模型名不是一回事
	Model string `yaml:"Model,omitempty"`
	// ApiKey 留空时用当前生效模型那套的 key
	ApiKey string `yaml:"ApiKey,omitempty"`
	// MaxUses 是一次搜索请求里允许厂商服务端搜几次，非正数按默认值
	MaxUses int `yaml:"MaxUses,omitempty"`
}

// DuckDuckGoSearchConfig 是兜底后端的参数
type DuckDuckGoSearchConfig struct {
	// Enabled 是显式开关。没写（nil）等于开启：兜底就该默认在。
	// 用指针是为了区分"没配"和"配了 false"
	Enabled *bool `yaml:"Enabled,omitempty"`
	// Endpoint 是结果页地址，留空用内置的
	Endpoint string `yaml:"Endpoint,omitempty"`
}

// SearchOptions 是"搜索该用哪个后端、参数是什么"归一化之后的结果，
// 装配代码只看这个结构，不用再去翻 YAML 的形状
type SearchOptions struct {
	Provider string
	DeepSeek DeepSeekSearchConfig
	// DuckDuckGoEndpoint 是结果页地址，空表示用内置的
	DuckDuckGoEndpoint string
	// DisableDuckDuckGo 表示连兜底都不要（配了 Enabled: false）
	DisableDuckDuckGo bool
}

// SearchOptions 归一化搜索配置：没配过的字段填默认值。
// 这段配错了不报错——搜索是附加能力，配坏了不该让整个程序起不来；
// 真到用不了的时候，工具会带着 provider 的 id 和原因报给模型
func (c GlobalConfig) SearchOptions() SearchOptions {
	out := SearchOptions{Provider: "auto"}
	if c.WebSearch == nil {
		return out
	}
	ws := c.WebSearch
	if p := strings.TrimSpace(ws.Provider); p != "" {
		out.Provider = p
	}
	out.DeepSeek = ws.DeepSeek
	out.DuckDuckGoEndpoint = strings.TrimSpace(ws.DuckDuckGo.Endpoint)
	// 只有显式写 Enabled: false 才关掉兜底
	out.DisableDuckDuckGo = ws.DuckDuckGo.Enabled != nil && !*ws.DuckDuckGo.Enabled
	return out
}
