package config

import (
	"strings"

	"gopkg.in/yaml.v3"
)

// DefaultMaxSteps 是一次提问里最多跑多少轮工具调用：模型陷入死循环时靠它刹车。
// 真需要更多轮就在配置里写 MaxSteps
const DefaultMaxSteps = 1200

type GlobalConfig struct {
	// Provider 是配置里只有一套模型时的老写法。新配置用 Models + Current；
	// 保存时两边都会写，老版本读到的仍然是当前跑的那一套
	Provider yaml.Node `yaml:"Provider"`
	// Models 是配置好的几套模型，/model 在它们之间切换。每套的 Name 是它的名字（不填就用模型名）
	Models []ModelConfig `yaml:"Models"`
	// Current 是当前用的那套在 Models 里的名字（Name 或模型名都认）；留空就用第一套
	Current string `yaml:"Current"`
	// Mode 是工作模式：read（只读）、ask（编辑需授权）、full（完全授权）。
	// 留空按默认的 ask 处理，写错了启动时直接报错，别让人以为配上了却没用
	Mode string `yaml:"Mode"`
	// MaxSteps 是一次提问里最多跑多少轮工具调用，不填（或填非正数）按 DefaultMaxSteps
	MaxSteps int `yaml:"MaxSteps"`
	// Quiet 是"精简显示"：打开后历史区只打正文，思考过程和工具输出的内容都不显示
	// （工具调用还是留一行标题，知道在跑什么）。不写这个字段时按打开处理——
	// 用指针是为了区分"没配"和"配了 false"，值用 QuietMode() 取
	Quiet *bool `yaml:"Quiet,omitempty"`
	// WebAuth 是浏览器界面的登录账号。不配就保持原来的"谁连上谁就能用"，
	// 那种情况下只该听 127.0.0.1；想让外面连就必须配上这个
	WebAuth *WebAuthConfig `yaml:"WebAuth,omitempty"`
	// WebSearch 是联网搜索的配置（用哪个后端、参数是什么）。
	// 整段不写也能用：有 DeepSeek 的 key 就走厂商服务端搜索，没有就刮网页兜底
	WebSearch *WebSearchConfig `yaml:"WebSearch,omitempty"`
}

// WebAuthConfig 是浏览器界面的登录账号，走 HTTP Basic 认证。
//
// 选 Basic 而不是自己写登录页：浏览器原生弹框，凭证由浏览器自己记住并
// 在同源请求上自动带上——包括 SSE 那条长连接，否则事件流会推不出来。
// 代价是密码明文存在配置里，所以配置文件本身不能外泄（config.yaml 已在 .gitignore）。
type WebAuthConfig struct {
	Username string `yaml:"Username"`
	Password string `yaml:"Password"`
}

// Valid 说明这组账号能不能用来登录：用户名和密码都得填。
// 只填一半是明显的配置错误，调用方按"没配"处理并提示，不要静默放行
func (a *WebAuthConfig) Valid() bool {
	if a == nil {
		return false
	}
	return strings.TrimSpace(a.Username) != "" && a.Password != ""
}

// QuietMode 返回精简显示开关，默认打开：没写过 Quiet 的配置也按精简来，
// 想看思考过程和工具输出就打 /quiet off
func (c GlobalConfig) QuietMode() bool {
	return c.Quiet == nil || *c.Quiet
}

// Steps 返回一次提问最多跑多少轮工具调用，没配或配了非正数时退回默认值
func (c GlobalConfig) Steps() int {
	if c.MaxSteps > 0 {
		return c.MaxSteps
	}
	return DefaultMaxSteps
}
