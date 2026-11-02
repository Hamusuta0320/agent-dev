package config

import "gopkg.in/yaml.v3"

// SaveMode 把工作模式写回配置文件，只动 Mode 这一段，其它字段和注释原样保留
func SaveMode(path, mode string) error {
	var node yaml.Node
	node.SetString(mode)
	return saveTopLevel(path, "Mode", &node)
}
