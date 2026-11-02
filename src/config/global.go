package config

import "gopkg.in/yaml.v3"

type GlobalConfig struct {
	Provider yaml.Node `yaml:"Provider"`
	// Mode 是工作模式：read（只读）、ask（编辑需授权）、full（完全授权）。
	// 留空按默认的 ask 处理，写错了启动时直接报错，别让人以为配上了却没用
	Mode string `yaml:"Mode"`
}
