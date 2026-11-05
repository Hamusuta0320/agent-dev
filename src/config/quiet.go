package config

import "gopkg.in/yaml.v3"

// SaveQuiet 把"精简显示"开关写回配置文件，只动 Quiet 这一段，其它字段和注释原样保留
func SaveQuiet(path string, quiet bool) error {
	node := &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!bool", Value: "false"}
	if quiet {
		node.Value = "true"
	}
	return saveTopLevel(path, "Quiet", node)
}
