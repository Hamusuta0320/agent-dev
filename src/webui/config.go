package webui

import (
	"context"
	"strings"

	"hamusuta/agent-dev/src/config"
	"hamusuta/agent-dev/src/domain"
	"hamusuta/agent-dev/src/provider"
)

// configView 是 /config 表单打开时要的那份值。
//
// 密钥不回传明文：表单里只告诉前端"配过了没有"。提交时这一格留空就是"不改"，
// 这样改地址、换模型名都不必把密钥再抄一遍（也就不会因为看漏一个字把密钥写坏）
type configView struct {
	Name string `json:"name"`
	// EditLabel 是正在改的那套的名字，保存时原样带回来（新增时是空串）
	EditLabel string `json:"edit_label"`
	// Mode 是表单模式："edit" 改当前那套，"new" 新增一套
	Mode      string `json:"mode"`
	Type      string `json:"type"`
	ApiUrl    string `json:"api_url"`
	Model     string `json:"model"`
	ApiKeySet bool   `json:"api_key_set"`
	// ContextLimit 是给人填的写法（128k、1M 都认），没配过就是默认值
	ContextLimit string `json:"context_limit"`
	Effort       string `json:"effort"`
	// Types / Efforts 是下拉框的可选项，前端不用自己写死一份
	Types   []string `json:"types"`
	Efforts []string `json:"efforts"`
	CfgPath string   `json:"cfg_path"`
	// DefaultUrls 是各类型的内置接口地址：前端切类型时能直接把地址带出来，
	// 不用用户再抄一遍（终端界面里切类型也是这个行为）
	DefaultUrls map[string]string `json:"default_urls"`
	// Models 是配置文件里已有的几套模型名，前端提示"改的是哪一套"
	Models []string `json:"models"`
}

// configForm 按当前生效的那套模型生成表单值
func (m *Manager) configForm() configView {
	cfg := m.Config()
	mc, err := cfg.CurrentModel()
	if err != nil {
		// 还没配过：摆一个空表单，类型给默认的那个，地址给内置的
		mc = config.ModelConfig{}
	}
	out := m.baseConfigView(cfg)
	out.Mode = "edit"
	out.EditLabel = mc.Label()
	out.Name = mc.Label()
	out.Type = normalizeType(mc.Type)
	out.ApiUrl = apiUrlOrDefault(mc)
	out.Model = strings.TrimSpace(mc.Model)
	out.ApiKeySet = strings.TrimSpace(mc.ApiKey) != ""
	out.ContextLimit = domain.FormatTokens(mc.Limit())
	out.Effort = config.EffortLabel(mc.ReasoningEffort)
	return out
}

// newConfigForm 生成一张空白表单：新增一套模型时用
func (m *Manager) newConfigForm() configView {
	cfg := m.Config()
	out := m.baseConfigView(cfg)
	out.Mode = "new"
	out.EditLabel = ""
	out.Name = ""
	out.Type = "DeepSeek"
	out.ApiUrl = config.DefaultApiUrl("DeepSeek")
	out.Model = ""
	out.ApiKeySet = false
	out.ContextLimit = domain.FormatTokens(config.DefaultContextLimit)
	out.Effort = config.EffortLabel("")
	return out
}

// baseConfigView 是两种表单都一样的部分：可选项、配置文件路径、已有的模型名
func (m *Manager) baseConfigView(cfg config.GlobalConfig) configView {
	out := configView{
		Types:       config.ProviderTypes,
		Efforts:     config.EffortLabels(),
		CfgPath:     m.cfgPath,
		DefaultUrls: make(map[string]string, len(config.ProviderTypes)),
	}
	for _, typ := range config.ProviderTypes {
		out.DefaultUrls[typ] = config.DefaultApiUrl(typ)
	}
	for _, model := range cfg.AvailableModels() {
		out.Models = append(out.Models, model.Label())
	}
	return out
}

// apiUrlOrDefault 是表单里该摆的接口地址：填过的用填的，没填的用该类型内置的
func apiUrlOrDefault(mc config.ModelConfig) string {
	if url := strings.TrimSpace(mc.ApiUrl); url != "" {
		return url
	}
	return config.DefaultApiUrl(mc.Type)
}

// normalizeType 把类型名归一成内置的写法（大小写不敏感）；没填按 DeepSeek 记，
// 认不出的类型原样留着——有人会填自家转发网关的名字
func normalizeType(typ string) string {
	typ = strings.TrimSpace(typ)
	if typ == "" {
		return "DeepSeek"
	}
	for _, t := range config.ProviderTypes {
		if strings.EqualFold(t, typ) {
			return t
		}
	}
	return typ
}

// configRequest 是 POST /api/config 的请求体。ApiKey 留空表示沿用配置里已有的那个
type configRequest struct {
	// Name 是这一套的名字；新增时留空就用模型名
	Name string `json:"name"`
	// EditLabel 是正在改的那套的名字（新增时是空串）
	EditLabel string `json:"edit_label"`
	// New 为真表示这是新增一套，不是改现有那套
	New          bool   `json:"new"`
	Type         string `json:"type"`
	ApiUrl       string `json:"api_url"`
	ApiKey       string `json:"api_key"`
	Model        string `json:"model"`
	ContextLimit string `json:"context_limit"`
	Effort       string `json:"effort"`
}

// saveConfig 把表单里的值写回配置文件，并同步内存里的配置——不用重启，下一轮就用新的。
// New 为真就是新增一套（追加到末尾，当前那套不动）；否则改 EditLabel 指的那套，
// 名字改了就是改名
func (m *Manager) saveConfig(req configRequest) (configView, error) {
	cfg := m.Config()
	models := cfg.AvailableModels()
	current := cfg.Current

	// 正在改的是哪一套：新增时是空串
	label := strings.TrimSpace(req.EditLabel)
	if label == "" && !req.New {
		if cur, err := cfg.CurrentModel(); err == nil {
			label = cur.Label()
		} else if len(models) > 0 {
			label = models[0].Label()
		}
	}

	mc := config.ModelConfig{
		Name:            strings.TrimSpace(req.Name),
		Type:            normalizeType(req.Type),
		ApiUrl:          strings.TrimSpace(req.ApiUrl),
		ApiKey:          strings.TrimSpace(req.ApiKey),
		Model:           strings.TrimSpace(req.Model),
		ReasoningEffort: strings.ToLower(strings.TrimSpace(req.Effort)),
	}
	// 密钥留空就是"不改"：前端拿不到明文，这是它唯一能表达"保持原样"的方式。
	// 沿用的是**被改的那套**的密钥，不是当前那套的——改的不是当前那套时，
	// 拿当前那套的密钥盖上去会把用户原来配的密钥弄丢。
	// 新增时不沿用：那里没有"原来那一套"可沿用
	if mc.ApiKey == "" && !req.New {
		if cur, ok := config.PickModel(models, label); ok {
			mc.ApiKey = strings.TrimSpace(cur.ApiKey)
		}
	}
	// 地址留空就用内置的那个：这几家的地址是固定的，没必要逼着人抄一遍
	if mc.ApiUrl == "" {
		mc.ApiUrl = config.DefaultApiUrl(mc.Type)
	}
	// 思考强度只认表里那几档，写错了直接报，别塞给接口换一个 400 回来
	effort := strings.ToLower(strings.TrimSpace(req.Effort))
	if _, err := config.ParseEffort(effort); err != nil {
		return configView{}, err
	}
	mc.ReasoningEffort = effort
	limit := strings.TrimSpace(req.ContextLimit)
	if limit == "" {
		mc.ContextLimit = config.DefaultContextLimit
	} else {
		n, err := domain.ParseTokens(limit)
		if err != nil {
			return configView{}, err
		}
		mc.ContextLimit = n
	}
	if mc.ApiUrl == "" || mc.ApiKey == "" || mc.Model == "" {
		return configView{}, errFieldsRequired
	}

	// 还没配过任何模型时没有什么可改的，这一套就是新加的
	if req.New || len(models) == 0 {
		// 新增一套：追加到末尾，当前那套不动
		next, err := config.AddModel(models, mc)
		if err != nil {
			return configView{}, errString(err.Error())
		}
		models = next
	} else {
		// 改现有那套：名字换了就是改名（原地换，列表长度不变）
		// 名字留空就是"不改名字"：编辑模式下名字是这一套的标识，
		// 没填就保住原来那个，别默默换成模型名
		if mc.Name == "" {
			mc.Name = label
		}
		next, cur, err := config.EditModel(models, current, label, mc)
		if err != nil {
			return configView{}, errString(err.Error())
		}
		models, current = next, cur
	}

	if err := config.SaveModels(m.cfgPath, models, current); err != nil {
		return configView{}, err
	}
	cfg.Models = models
	cfg.Current = current
	// 老的 Provider 段跟着同步一份：还按老格式读配置的地方看到的也是当前这套
	if cur, err := cfg.CurrentModel(); err == nil {
		cur.Name = ""
		_ = cfg.Provider.Encode(cur)
	}
	m.setConfig(cfg)
	return m.configForm(), nil
}

// deleteModel 删掉叫 name 的那套。删的是当前那套就改用剩下的第一套
func (m *Manager) deleteModel(name string) (configView, error) {
	cfg := m.Config()
	models, current, err := config.DeleteModel(cfg.AvailableModels(), cfg.Current, name)
	if err != nil {
		return configView{}, err
	}
	if err := config.SaveModels(m.cfgPath, models, current); err != nil {
		return configView{}, err
	}
	cfg.Models = models
	cfg.Current = current
	if cur, err := cfg.CurrentModel(); err == nil {
		cur.Name = ""
		_ = cfg.Provider.Encode(cur)
	}
	m.setConfig(cfg)
	return m.configForm(), nil
}

// errFieldsRequired 是表单没填全时的提示，和终端界面的措辞一致
var errFieldsRequired = errString("接口地址、密钥、模型都得填")

// errString 是最简单的一种错误：只有一句话。用它省得为每种校验造一个类型
type errString string

func (e errString) Error() string { return string(e) }

// listModels 用表单里填的地址和密钥去接口拉一遍模型列表。
// key 留空时用配置里已有的那个（前端拿不到明文，只能这么问）
func (m *Manager) listModels(apiUrl, apiKey string) ([]string, error) {
	apiUrl = strings.TrimSpace(apiUrl)
	apiKey = strings.TrimSpace(apiKey)
	if apiUrl == "" {
		return nil, errString("先填接口地址，再拉模型列表")
	}
	if apiKey == "" {
		cur, err := m.Config().CurrentModel()
		if err != nil {
			return nil, errString("先填密钥，再拉模型列表")
		}
		apiKey = strings.TrimSpace(cur.ApiKey)
	}
	return provider.ListModels(context.Background(), apiUrl, apiKey)
}
