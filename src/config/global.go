package config

import "gopkg.in/yaml.v3"

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
}

// Steps 返回一次提问最多跑多少轮工具调用，没配或配了非正数时退回默认值
func (c GlobalConfig) Steps() int {
	if c.MaxSteps > 0 {
		return c.MaxSteps
	}
	return DefaultMaxSteps
}
