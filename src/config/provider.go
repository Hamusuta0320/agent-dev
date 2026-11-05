package config

import (
	"bytes"
	"fmt"
	"os"
	"strings"

	"gopkg.in/yaml.v3"
)

// ProbeConfig 只看配置里的 Type，用来判断该造哪种 provider
type ProbeConfig struct {
	Type string `json:"Type" yaml:"Type"`
}

// DefaultContextLimit 是上下文窗口的默认大小（token）：配置里没填时用它算已用百分比
const DefaultContextLimit = 128000

// ModelConfig 是一套模型配置。Type 说明走哪套协议（DeepSeek 和 OpenAI 都是 OpenAI 兼容接口，
// 只是地址和模型名不同），Name 是它在 Models 列表里的名字，/model 靠它切换。
type ModelConfig struct {
	Name   string `json:"Name,omitempty" yaml:"Name,omitempty"`
	Type   string `json:"Type" yaml:"Type"`
	ApiUrl string `json:"ApiUrl" yaml:"ApiUrl"`
	Model  string `json:"Model" yaml:"Model"`
	ApiKey string `json:"ApiKey" yaml:"ApiKey"`
	// ContextLimit 是模型的上下文窗口（token），只用来算"上下文已用百分比"；不填时按 DefaultContextLimit
	ContextLimit int `json:"ContextLimit" yaml:"ContextLimit"`
}

// DeepSeekConfig 是配置里只有一套模型时的老名字，现在和 ModelConfig 是同一个类型
type DeepSeekConfig = ModelConfig

// Limit 返回配置的上下文窗口大小，没填（或填了非正数）时退回默认值
func (mc ModelConfig) Limit() int {
	if mc.ContextLimit > 0 {
		return mc.ContextLimit
	}
	return DefaultContextLimit
}

// Label 是这套配置在 /model 列表里的名字：没填 Name 就用模型名，再没有就用类型。
// 名字只是给人看的标签，切换时 Name 和模型名都能认
func (mc ModelConfig) Label() string {
	if name := strings.TrimSpace(mc.Name); name != "" {
		return name
	}
	if model := strings.TrimSpace(mc.Model); model != "" {
		return model
	}
	return strings.TrimSpace(mc.Type)
}

// AvailableModels 返回配置里所有可用的模型：新配置看 Models 列表；
// 只有老的 Provider 段（还没写过 Models）时，把那一套当成唯一的一套
func (c GlobalConfig) AvailableModels() []ModelConfig {
	if len(c.Models) > 0 {
		return c.Models
	}
	var mc ModelConfig
	if err := c.Provider.Decode(&mc); err != nil {
		return nil
	}
	if strings.TrimSpace(mc.ApiUrl) == "" && strings.TrimSpace(mc.ApiKey) == "" && strings.TrimSpace(mc.Model) == "" {
		return nil
	}
	return []ModelConfig{mc}
}

// CurrentModel 返回当前生效的那套模型配置。Current 留空就用列表里第一套；
// 写了名字却找不到就报错——静默退回别的模型，会让人以为配上的那套在跑
func (c GlobalConfig) CurrentModel() (ModelConfig, error) {
	models := c.AvailableModels()
	if len(models) == 0 {
		return ModelConfig{}, fmt.Errorf("配置里还没有模型：用 /config 填一套，或者在配置文件的 Models 下加几套")
	}
	name := strings.TrimSpace(c.Current)
	if name == "" {
		return models[0], nil
	}
	for _, mc := range models {
		if matchesModel(mc, name) {
			return mc, nil
		}
	}
	return ModelConfig{}, fmt.Errorf("Current 指的是 %q，但配置里没有这套模型（现有：%s）",
		name, strings.Join(modelLabels(models), "、"))
}

// matchesModel 判断一个名字是不是指这套配置：Name 和模型名都算，忽略大小写
func matchesModel(mc ModelConfig, name string) bool {
	name = strings.TrimSpace(name)
	if name == "" {
		return false
	}
	if strings.EqualFold(strings.TrimSpace(mc.Name), name) {
		return true
	}
	return strings.EqualFold(strings.TrimSpace(mc.Model), name)
}

// modelLabels 是所有配置的名字，报错和列表里用
func modelLabels(models []ModelConfig) []string {
	out := make([]string, 0, len(models))
	for _, mc := range models {
		out = append(out, mc.Label())
	}
	return out
}

// SaveModels 把几套模型和当前选中的那套写回配置文件。除了 Models 和 Current，
// 当前这套还会同步写进老的 Provider 段：文件同时喂得饱新老两个版本，
// 也不会留下一份过期没人看的 Provider
func SaveModels(path string, models []ModelConfig, current string) error {
	prepared := make([]ModelConfig, 0, len(models))
	for _, mc := range models {
		if strings.TrimSpace(mc.Name) == "" {
			mc.Name = mc.Label()
		}
		prepared = append(prepared, mc)
	}
	// 当前这套按名字认；认不出来就报错，别把 Current 静默写成别的模型
	cur, err := GlobalConfig{Models: prepared, Current: current}.CurrentModel()
	if err != nil {
		return err
	}

	var modelsNode, providerNode yaml.Node
	if err := modelsNode.Encode(prepared); err != nil {
		return fmt.Errorf("生成模型列表失败: %w", err)
	}
	// Provider 段保持老样子：不带 Name
	legacy := cur
	legacy.Name = ""
	if err := providerNode.Encode(legacy); err != nil {
		return fmt.Errorf("生成配置内容失败: %w", err)
	}
	return saveTopLevels(path,
		yamlField{"Models", &modelsNode},
		yamlField{"Current", scalarNode(cur.Label())},
		yamlField{"Provider", &providerNode},
	)
}

// SaveProvider 只把 Provider 这一段写回去（配置里只有一套模型时的写法）
func SaveProvider(path string, dc DeepSeekConfig) error {
	var provider yaml.Node
	if err := provider.Encode(dc); err != nil {
		return fmt.Errorf("生成配置内容失败: %w", err)
	}
	return saveTopLevel(path, "Provider", &provider)
}

// yamlField 是配置文件里要写的一个顶层字段
type yamlField struct {
	key  string
	node *yaml.Node
}

// saveTopLevel 把配置文件里的某个顶层字段换成 node
func saveTopLevel(path, key string, node *yaml.Node) error {
	return saveTopLevels(path, yamlField{key, node})
}

// saveTopLevels 按 fields 的顺序替换（或追加）配置文件的顶层字段，
// 文件里其它的顶层字段和注释尽量保留：先解析成节点再改，而不是整份覆盖。
func saveTopLevels(path string, fields ...yamlField) error {
	var doc yaml.Node
	if data, err := os.ReadFile(path); err == nil && len(bytes.TrimSpace(data)) > 0 {
		if err := yaml.Unmarshal(data, &doc); err != nil {
			return fmt.Errorf("解析 %s 失败: %w", path, err)
		}
	} else if err != nil && !os.IsNotExist(err) {
		return fmt.Errorf("读取 %s 失败: %w", path, err)
	}

	if len(doc.Content) == 0 {
		root := &yaml.Node{Kind: yaml.MappingNode, Tag: "!!map"}
		for _, f := range fields {
			root.Content = append(root.Content, scalarNode(f.key), f.node)
		}
		doc.Kind = yaml.DocumentNode
		doc.Content = []*yaml.Node{root}
	} else if root := doc.Content[0]; root.Kind == yaml.MappingNode {
		for _, f := range fields {
			replaced := false
			for i := 0; i+1 < len(root.Content); i += 2 {
				if root.Content[i].Value == f.key {
					root.Content[i+1] = f.node
					replaced = true
					break
				}
			}
			if !replaced {
				root.Content = append(root.Content, scalarNode(f.key), f.node)
			}
		}
	} else {
		return fmt.Errorf("%s 的顶层不是映射，改不了配置", path)
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

// scalarNode 造一个字符串标量节点，用来写 Current 这种只有一个值的顶层字段
func scalarNode(s string) *yaml.Node {
	return &yaml.Node{Kind: yaml.ScalarNode, Tag: "!!str", Value: s}
}
