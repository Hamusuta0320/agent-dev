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

// 内置的接口地址。这几家都走 OpenAI 兼容协议，地址是固定的，不用每次手打；
// 手填的地址永远优先，留空时才用这里的
const (
	DefaultDeepSeekApiUrl = "https://api.deepseek.com"
	DefaultOpenAIApiUrl   = "https://api.openai.com/v1"
	// DefaultMiniMaxApiUrl 是国内站的 OpenAI 兼容地址（海外站是 https://api.minimaxi.com/v1，
	// 用海外站就在配置里手填那个地址）
	DefaultMiniMaxApiUrl = "https://api.minimax.chat/v1"
)

// ProviderTypes 是内置支持的类型，/config 表单按这个顺序切换。
// 它们都走 OpenAI 兼容协议，差别只在地址和模型名
var ProviderTypes = []string{"DeepSeek", "OpenAI", "MiniMax"}

// EffortLevels 是"思考强度"的档位，从省到费排。/config 里用 ←→ 在这个表里循环，
// 空串表示压根不发这个参数（模型用自家默认值）——那是最保险的一档，
// 因为认不认 reasoning_effort 全看各家接口
//
// 各家认的档位不一样，所以按类型分开（见 EffortLevelsFor）。deepseek 那份是**实测**
// 出来的，不是照文档抄的：故意发一个非法值，接口会把自己的期望列表原样回过来——
//
//	422 unknown variant `ludicrous`, expected one of
//	    `none`, `minimal`, `low`, `medium`, `high`, `xhigh`, `ultra`, `max`
//
// openai / minimax 这两家没实测过（手上没有可信的验证手段），保持原来那几档：
// 不确定的档位不往里加，加进去只会让人选到一个接口不认的值，再换回一个 400
var (
	effortLevelsDeepSeek = []string{"", "none", "minimal", "low", "medium", "high", "xhigh", "ultra", "max"}
	// effortLevelsCommon 是各家几乎都认的那几档，认不出类型时用它
	effortLevelsCommon = []string{"", "minimal", "low", "medium", "high"}
	effortLevelsByType = map[string][]string{
		"deepseek": effortLevelsDeepSeek,
		"openai":   effortLevelsCommon,
		"minimax":  effortLevelsCommon,
	}
)

// EffortLevelsFor 返回某个类型能用的档位。
// 类型留空按 DeepSeek 算——和 DefaultApiUrl、provider.New 是同一个口径
// （老的配置文件里没有 Type 这一项，那会儿就是 DeepSeek）。
// 认不出的类型给公共的那几档：瞎给一整套 DeepSeek 的档位，等于让人配上一个
// 自家网关根本不认的值
func EffortLevelsFor(typ string) []string {
	key := strings.ToLower(strings.TrimSpace(typ))
	if key == "" {
		key = "deepseek"
	}
	if levels, ok := effortLevelsByType[key]; ok {
		return levels
	}
	return effortLevelsCommon
}

// ParseEffort 把配置里的思考强度归一成小写档位名。留空是合法的（不发这个参数），
// 写了这个类型不认的值就报错：悄悄塞给接口换回一个 400，比在这里说清楚糟得多
func ParseEffort(effort, typ string) (string, error) {
	want := strings.ToLower(strings.TrimSpace(effort))
	for _, level := range EffortLevelsFor(typ) {
		if want == level {
			return level, nil
		}
	}
	return "", fmt.Errorf("思考强度只认 %s（留空表示按模型默认）", strings.Join(EffortLabels(typ), " / "))
}

// EffortLabels 是档位的显示名：空档叫"默认"，其余就是档位名本身
func EffortLabels(typ string) []string {
	levels := EffortLevelsFor(typ)
	out := make([]string, 0, len(levels))
	for _, level := range levels {
		out = append(out, EffortLabel(level))
	}
	return out
}

// EffortLabel 是一个档位给人看的名字
func EffortLabel(effort string) string {
	level := strings.ToLower(strings.TrimSpace(effort))
	if level == "" {
		return "默认"
	}
	return level
}

// DefaultApiUrl 按协议类型给出内置的接口地址。认不出的类型返回空串：
// 瞎猜一个地址比让用户自己填更糟
func DefaultApiUrl(typ string) string {
	switch strings.ToLower(strings.TrimSpace(typ)) {
	case "", "deepseek":
		return DefaultDeepSeekApiUrl
	case "openai":
		return DefaultOpenAIApiUrl
	case "minimax":
		return DefaultMiniMaxApiUrl
	}
	return ""
}

// ModelConfig 是一套模型配置。Type 说明走哪套协议（DeepSeek、OpenAI 和 MiniMax 都是 OpenAI
// 兼容接口，只是地址和模型名不同），Name 是它在 Models 列表里的名字，/model 靠它切换。
type ModelConfig struct {
	Name   string `json:"Name,omitempty" yaml:"Name,omitempty"`
	Type   string `json:"Type" yaml:"Type"`
	ApiUrl string `json:"ApiUrl" yaml:"ApiUrl"`
	Model  string `json:"Model" yaml:"Model"`
	ApiKey string `json:"ApiKey" yaml:"ApiKey"`
	// ContextLimit 是模型的上下文窗口（token），只用来算"上下文已用百分比"；不填时按 DefaultContextLimit
	ContextLimit int `json:"ContextLimit" yaml:"ContextLimit"`
	// ReasoningEffort 是思考强度（OpenAI 兼容协议里的 reasoning_effort），档位见 EffortLevels。
	// 留空就不发这个参数，让模型用它自己的默认值
	ReasoningEffort string `json:"ReasoningEffort,omitempty" yaml:"ReasoningEffort,omitempty"`
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

// PickModel 把一个名字认成一套配置：名字和模型名都能认，显示名（Label）
// 的前几个字对得上也算，但必须只对上一套——两套以上拿不准时让用户说全。
// 终端和浏览器界面的 /model 都用它
func PickModel(models []ModelConfig, arg string) (ModelConfig, bool) {
	arg = strings.TrimSpace(arg)
	if arg == "" {
		return ModelConfig{}, false
	}
	for _, mc := range models {
		if matchesModel(mc, arg) {
			return mc, true
		}
	}
	var matched []ModelConfig
	for _, mc := range models {
		if strings.HasPrefix(strings.ToLower(mc.Label()), strings.ToLower(arg)) {
			matched = append(matched, mc)
		}
	}
	if len(matched) == 1 {
		return matched[0], true
	}
	return ModelConfig{}, false
}

// EditModel 改 models 里叫 label 的那套：换成 mc。mc.Name 是新名字，
// 和 label 不同就是改名（原地换，不会多出一套）。
// 返回新的模型列表和新的 Current
func EditModel(models []ModelConfig, current, label string, mc ModelConfig) ([]ModelConfig, string, error) {
	idx := indexOfModel(models, label)
	if idx < 0 {
		return nil, "", fmt.Errorf("没有叫 %q 的模型", label)
	}
	mc = withName(mc)
	if err := checkNameFree(models, idx, mc); err != nil {
		return nil, "", err
	}
	out := append([]ModelConfig(nil), models...)
	out[idx] = mc
	next := current
	// 改的正是当前那套时，Current 跟着换成新名字
	if isCurrentIndex(models, current, idx) {
		next = mc.Label()
	}
	return out, next, nil
}

// AddModel 追加一套新的。mc.Name 空就用模型名当名字。
// 名字和现有的撞了报错——不默默覆盖人家配好的那套
func AddModel(models []ModelConfig, mc ModelConfig) ([]ModelConfig, error) {
	mc = withName(mc)
	// 新的一套排在最后：名字撞了谁都认不出它（/model 会先认出别人那一套）
	if err := checkNameFree(models, len(models), mc); err != nil {
		return nil, err
	}
	return append(append([]ModelConfig(nil), models...), mc), nil
}

// checkNameFree 查新的一套会不会把谁挡在后面。
// at 是这一套改完之后排在第几个（新增时排在最后）。
// 挡人的两种情形：
//   - 名字（Name）和别的套的 Name 或 Model 撞上了：/model 认名字时两个都认，
//     撞上谁就认不出谁；
//   - 模型名和排在后面某一套的 Name 撞上了：自己排在前面，那套用 Name 也认不出来。
func checkNameFree(models []ModelConfig, at int, mc ModelConfig) error {
	name := strings.TrimSpace(mc.Name)
	if name == "" {
		return nil
	}
	model := strings.TrimSpace(mc.Model)
	for i, other := range models {
		if i == at {
			continue
		}
		if matchesModel(other, name) {
			return fmt.Errorf("已经有叫 %q 的模型了", name)
		}
		if i > at && model != "" && strings.EqualFold(strings.TrimSpace(other.Name), model) {
			return fmt.Errorf("模型名 %q 和另一套的名字重了，那套就用 /model 认不出来了", model)
		}
	}
	return nil
}

// DeleteModel 删掉 models 里叫 label 的那套。删的是当前那套就改用剩下的第一套。
// 只剩一套时删不了：配置里一套都没有的话启动就报错
func DeleteModel(models []ModelConfig, current, label string) ([]ModelConfig, string, error) {
	idx := indexOfModel(models, label)
	if idx < 0 {
		return nil, "", fmt.Errorf("没有叫 %q 的模型", label)
	}
	if len(models) <= 1 {
		return nil, "", fmt.Errorf("只剩一套模型了，删不掉")
	}
	out := make([]ModelConfig, 0, len(models)-1)
	out = append(out, models[:idx]...)
	out = append(out, models[idx+1:]...)
	next := current
	// 删掉的正是当前那套时，改用剩下的第一套
	if isCurrentIndex(models, current, idx) {
		next = out[0].Label()
	}
	return out, next, nil
}

// withName 把名字补齐：没填就用模型名当名字（和 SaveModels 里一个口径）
func withName(mc ModelConfig) ModelConfig {
	if strings.TrimSpace(mc.Name) == "" {
		mc.Name = mc.Label()
	}
	return mc
}

// indexOfModel 找出叫 label 的那套在列表里的位置。名字和模型名都认，
// 但只做精确匹配，不做 PickModel 那种前缀匹配——增删改都得指准是哪一套。
// 好几套都认得时取第一个（和 PickModel 一个口径）；界面传进来的名字来自列表，
// 不会碰上重名。认不出返回 -1
func indexOfModel(models []ModelConfig, label string) int {
	label = strings.TrimSpace(label)
	if label == "" {
		return -1
	}
	for i, mc := range models {
		if matchesModel(mc, label) {
			return i
		}
	}
	return -1
}

// isCurrentIndex 判断第 idx 套是不是当前生效的那套。
// Current 留空时按第一套算，和 CurrentModel() 一个口径
func isCurrentIndex(models []ModelConfig, current string, idx int) bool {
	if idx < 0 || idx >= len(models) {
		return false
	}
	if strings.TrimSpace(current) == "" {
		return idx == 0
	}
	return matchesModel(models[idx], current)
}

// WithEffort 返回一份"当前这套模型的思考强度改成 level"的配置，以及改的那套模型。
// 只动内存里这份，写回文件由调用方用 SaveModels 做：两个界面都是先让新值生效、
// 写回失败只提示一句，不能因为磁盘写不进去就把这次运行也作废
func WithEffort(cfg GlobalConfig, level string) (GlobalConfig, ModelConfig, error) {
	cur, err := cfg.CurrentModel()
	if err != nil {
		return cfg, ModelConfig{}, err
	}
	// 名字先补齐，和 SaveModels 写回时补的一致，两边才认得出是同一套。
	// 拷贝一份再改：AvailableModels 返回的就是调用方那个数组，不能就地改
	models := append([]ModelConfig(nil), cfg.AvailableModels()...)
	for i := range models {
		if strings.TrimSpace(models[i].Name) == "" {
			models[i].Name = models[i].Label()
		}
	}
	// 当前这套就是要改的那套：按名字找，找不到（配置刚被改坏）就退回第一套
	target := 0
	for i := range models {
		if strings.EqualFold(models[i].Label(), cur.Label()) {
			target = i
			break
		}
	}
	models[target].ReasoningEffort = level
	cfg.Models = models
	cfg.Current = models[target].Label()
	return cfg, models[target], nil
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
