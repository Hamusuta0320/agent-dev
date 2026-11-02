package config

import (
	"bytes"
	"fmt"
	"os"

	"gopkg.in/yaml.v3"
)

type ProbeConfig struct {
	Type string `json:"Type" yaml:"Type"`
}

// DefaultContextLimit 是上下文窗口的默认大小（token）：配置里没填时用它算已用百分比
const DefaultContextLimit = 128000

type DeepSeekConfig struct {
	Type   string `json:"Type" yaml:"Type"`
	ApiUrl string `json:"ApiUrl" yaml:"ApiUrl"`
	Model  string `json:"Model" yaml:"Model"`
	ApiKey string `json:"ApiKey" yaml:"ApiKey"`
	// ContextLimit 是模型的上下文窗口（token），只用来算"上下文已用百分比"；不填时按 DefaultContextLimit
	ContextLimit int `json:"ContextLimit" yaml:"ContextLimit"`
}

// Limit 返回配置的上下文窗口大小，没填（或填了非正数）时退回默认值
func (dc DeepSeekConfig) Limit() int {
	if dc.ContextLimit > 0 {
		return dc.ContextLimit
	}
	return DefaultContextLimit
}

// SaveProvider 把 provider 配置写回配置文件。只替换 Provider 这一段，
// 文件里其它的顶层字段和注释尽量保留：先解析成节点再改，而不是整份覆盖。
func SaveProvider(path string, dc DeepSeekConfig) error {
	var provider yaml.Node
	if err := provider.Encode(dc); err != nil {
		return fmt.Errorf("生成配置内容失败: %w", err)
	}
	return saveTopLevel(path, "Provider", &provider)
}

// saveTopLevel 把配置文件里的某个顶层字段换成 node，其它顶层字段和注释尽量保留：
// 先解析成节点再改，而不是整份覆盖。
func saveTopLevel(path, key string, node *yaml.Node) error {
	var doc yaml.Node
	if data, err := os.ReadFile(path); err == nil && len(bytes.TrimSpace(data)) > 0 {
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("解析 %s 失败: %w", path, err)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("读取 %s 失败: %w", path, err)
	}

	if len(doc.Content) == 0 {
		doc.Kind = yaml.DocumentNode
		doc.Content = []*yaml.Node{{
			Kind: yaml.MappingNode,
			Tag:  "!!map",
			Content: []*yaml.Node{
				{Kind: yaml.ScalarNode, Tag: "!!str", Value: key},
				node,
			},
		}}
	} else if root := doc.Content[0]; root.Kind == yaml.MappingNode {
		replaced := false
		for i := 0; i+1 < len(root.Content); i += 2 {
			if root.Content[i].Value == key {
				root.Content[i+1] = node
				replaced = true
				break
			}
		}
		if !replaced {
			root.Content = append(root.Content,
				&yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: key}, node)
		}
	} else {
		return fmt.Errorf("%s 的顶层不是映射，改不了 %s", path, key)
	}

	data, err := yaml.Marshal(&doc)
	if err != nil {
		return fmt.Errorf("序列化配置失败: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return fmt.Errorf("写入 %s 失败: %w", path, err)
	}
	return nil
}
