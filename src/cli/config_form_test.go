package main

import (
	"errors"
	"path/filepath"
	"strings"
	"testing"

	"hamusuta/agent-dev/src/config"
	"hamusuta/agent-dev/src/domain"

	tea "charm.land/bubbletea/v2"
)

// 地址留空时表单里预填内置的那个；填过的地址不覆盖
func TestConfigFormFillsBuiltinUrl(t *testing.T) {
	for _, c := range []struct {
		name string
		mc   config.ModelConfig
		want string
	}{
		{"空配置", config.ModelConfig{}, config.DefaultDeepSeekApiUrl},
		{"DeepSeek 类型", config.ModelConfig{Type: "DeepSeek"}, config.DefaultDeepSeekApiUrl},
		{"OpenAI 类型", config.ModelConfig{Type: "OpenAI"}, config.DefaultOpenAIApiUrl},
		{"自己填过的", config.ModelConfig{ApiUrl: "https://my.gateway/v1"}, "https://my.gateway/v1"},
	} {
		t.Run(c.name, func(t *testing.T) {
			if got := newConfigForm(c.mc, c.mc.Label()).value(urlField); got != c.want {
				t.Fatalf("表单里的地址不对: %q，想要 %q", got, c.want)
			}
		})
	}

	// 保存时地址那一格被清空也不该报错：内置地址顶上
	f := newConfigForm(config.ModelConfig{Type: "DeepSeek"}, "")
	f.inputs[urlField].SetValue("  ")
	mc, err := f.modelConfig()
	if err != nil {
		t.Fatalf("地址留空不该报错: %v", err)
	}
	if mc.ApiUrl != config.DefaultDeepSeekApiUrl {
		t.Fatalf("地址留空该用内置的: %q", mc.ApiUrl)
	}
}

// Ctrl+L 拉到列表之后展开候选：↑↓ 挑一个、Enter 填进模型那一格
func TestConfigFormPickModel(t *testing.T) {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	m.state = stateConfig
	m.form = newConfigForm(config.ModelConfig{Type: "DeepSeek", ApiKey: "sk-test"}, "")
	pulled, _ := m.pullModels(true)
	m = pulled.(model)
	if !m.form.fetching {
		t.Fatal("拉取期间该显示进度")
	}

	// 结果回来（地址对得上）：候选列表展开
	got := m.applyModelList(modelsFetchedMsg{
		models: []string{"deepseek-chat", "deepseek-reasoner"},
		apiUrl: config.DefaultDeepSeekApiUrl,
		seq:    1,
	})
	if got.form.fetching {
		t.Fatal("结果回来了还在显示进度")
	}
	if !got.form.picking() {
		t.Fatalf("按 Ctrl+L 拉的列表回来该直接展开: %+v", got.form)
	}
	if got.form.pickIdx != 0 {
		t.Fatalf("模型空着时该停在第一个，实际 %d", got.form.pickIdx)
	}

	// ↓ 挪一条，Enter 采用
	var next tea.Model
	next, _ = got.handleConfigKey(tea.KeyPressMsg{Code: tea.KeyDown})
	got = next.(model)
	if got.form.pickIdx != 1 {
		t.Fatalf("↓ 之后该选第二项，实际 %d", got.form.pickIdx)
	}
	next, _ = got.handleConfigKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	got = next.(model)
	if v := got.form.value(modelField); v != "deepseek-reasoner" {
		t.Fatalf("模型没填进表单: %q", v)
	}
	if got.form.picking() {
		t.Fatal("采用之后列表该收起来")
	}
}

// 顺手拉回来的列表不展开，只在表单里说一句；地址改过之后那份旧结果不认
func TestConfigFormListStale(t *testing.T) {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	m.state = stateConfig
	m.form = newConfigForm(config.ModelConfig{Type: "DeepSeek", ApiKey: "sk-test"}, "")
	pulled, _ := m.pullModels(false)
	m = pulled.(model)
	if !m.form.fetching {
		t.Fatal("拉取期间该显示进度")
	}

	got := m.applyModelList(modelsFetchedMsg{
		models: []string{"deepseek-chat"},
		apiUrl: config.DefaultDeepSeekApiUrl,
		seq:    1,
	})
	if got.form.picking() {
		t.Fatal("不是 Ctrl+L 拉的列表不该自动展开")
	}
	if len(got.form.models) != 1 {
		t.Fatalf("列表该记在手里: %+v", got.form.models)
	}
	lines := stripANSI(strings.Join(got.fieldLines(), "\n"))
	if !strings.Contains(lines, "1 个模型") {
		t.Fatalf("表单里该提一句能挑的模型:\n%s", lines)
	}

	// 表单里的地址已经换掉了，这时候回来的结果对不上，丢掉
	stale := m.applyModelList(modelsFetchedMsg{
		models: []string{"别的模型的"},
		apiUrl: "https://other.example.com",
		seq:    1,
	})
	if len(stale.form.models) != 0 {
		t.Fatalf("地址对不上的结果不该采用: %+v", stale.form.models)
	}

	// 顺手拉的这次失败不弹错误：它是后台行为，别打断正在改配置的人
	quiet := m.applyModelList(modelsFetchedMsg{
		apiUrl: config.DefaultDeepSeekApiUrl,
		seq:    1,
		err:    errors.New("模型接口返回 401: invalid api key"),
	})
	if strings.Contains(stripANSI(strings.Join(quiet.fieldLines(), "\n")), "拉取模型列表失败") {
		t.Fatal("顺手拉的失败不该在表单里报错")
	}

	// 用户按 Ctrl+L 主动拉的，失败原因要摆出来，模型那一格照样能手填
	picked, _ := m.pullModels(true)
	withPick := picked.(model)
	failed := withPick.applyModelList(modelsFetchedMsg{
		apiUrl: config.DefaultDeepSeekApiUrl,
		seq:    withPick.form.fetchSeq,
		err:    errors.New("模型接口返回 401: invalid api key"),
	})
	text := stripANSI(strings.Join(failed.fieldLines(), "\n"))
	if !strings.Contains(text, "拉取模型列表失败") || !strings.Contains(text, "401") {
		t.Fatalf("拉失败的提示不对:\n%s", text)
	}

	// 又按了一次 Ctrl+L 之后，前一次迟到的结果作废：不能把新一次的进度和结果搅乱
	older := failed.applyModelList(modelsFetchedMsg{
		models: []string{"上一次的列表"},
		apiUrl: config.DefaultDeepSeekApiUrl,
		seq:    1,
	})
	if len(older.form.models) != 0 {
		t.Fatalf("过期的结果不该采用: %+v", older.form.models)
	}
}

// 地址或密钥没填时按 Ctrl+L 只提示一句，不发请求
func TestConfigFormFetchNeedsKey(t *testing.T) {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	m.state = stateConfig
	m.form = newConfigForm(config.ModelConfig{Type: "DeepSeek"}, "")

	got, cmd := m.handleConfigKey(tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})
	if cmd != nil {
		t.Fatal("密钥没填不该去请求接口")
	}
	if !strings.Contains(got.(model).form.err, "密钥") {
		t.Fatalf("该提示先填密钥，实际 %q", got.(model).form.err)
	}
	if got.(model).form.fetching {
		t.Fatal("没发请求就别显示进度")
	}
}

// 类型那一格用 ←→ 换成内置类型：地址还空着或还是旧类型的内置地址时跟着换，
// 自己手填过的地址不动；保存时类型写进配置
func TestConfigFormSwitchType(t *testing.T) {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	m.cfgPath = filepath.Join(t.TempDir(), "config.yaml")
	m.state = stateConfig
	m.form = newConfigForm(config.ModelConfig{Type: "DeepSeek"}, "")
	// 光标得停在类型那一格上：←→ 只在类型和思考强度这两格上有用
	m.form.cur = typeField
	if got := m.form.value(typeField); got != "DeepSeek" {
		t.Fatalf("类型没带出来: %q", got)
	}
	if got := m.form.value(urlField); got != config.DefaultDeepSeekApiUrl {
		t.Fatalf("地址该预填 DeepSeek 的内置地址: %q", got)
	}

	// → 走一格到 OpenAI，地址跟着换
	next, _ := m.handleConfigKey(tea.KeyPressMsg{Code: tea.KeyRight})
	got := next.(model)
	if v := got.form.value(typeField); v != "OpenAI" {
		t.Fatalf("→ 之后该是 OpenAI，实际 %q", v)
	}
	if v := got.form.value(urlField); v != config.DefaultOpenAIApiUrl {
		t.Fatalf("地址没跟着换成 OpenAI 的: %q", v)
	}

	// 再走一格到 MiniMax
	next, _ = got.handleConfigKey(tea.KeyPressMsg{Code: tea.KeyRight})
	got = next.(model)
	if v := got.form.value(typeField); v != "MiniMax" {
		t.Fatalf("再按一次 → 该是 MiniMax，实际 %q", v)
	}
	if v := got.form.value(urlField); v != config.DefaultMiniMaxApiUrl {
		t.Fatalf("地址没跟着换成 MiniMax 的: %q", v)
	}

	// 填上密钥和模型存下来，配置里就是这套 MiniMax
	got.form.inputs[keyField].SetValue("sk-minimax")
	got.form.inputs[modelField].SetValue("MiniMax-M2")
	saved, _ := got.saveConfig()
	mc, err := saved.(model).cfg.CurrentModel()
	if err != nil {
		t.Fatalf("保存后读配置失败: %v", err)
	}
	if mc.Type != "MiniMax" || mc.ApiUrl != config.DefaultMiniMaxApiUrl || mc.Model != "MiniMax-M2" {
		t.Fatalf("存下来的配置不对: %+v", mc)
	}

	// 自己填过的地址不跟着换：换个类型它还是自己的
	custom := newConfigForm(config.ModelConfig{Type: "DeepSeek", ApiUrl: "https://my.gateway/v1"}, "")
	custom.cur = typeField
	m.form = custom
	next, _ = m.handleConfigKey(tea.KeyPressMsg{Code: tea.KeyRight})
	if v := next.(model).form.value(urlField); v != "https://my.gateway/v1" {
		t.Fatalf("手填的地址不该被换掉: %q", v)
	}

	// ← 从第一个往前绕到最后一个
	first := newConfigForm(config.ModelConfig{Type: "DeepSeek"}, "")
	first.cur = typeField
	m.form = first
	next, _ = m.handleConfigKey(tea.KeyPressMsg{Code: tea.KeyLeft})
	if v := next.(model).form.value(typeField); v != "MiniMax" {
		t.Fatalf("从 DeepSeek 往前该绕到 MiniMax，实际 %q", v)
	}
}

// 思考强度那一格用 ←→ 换档位，空档（默认）不发这个参数；选中的档位存进配置
func TestConfigFormSwitchEffort(t *testing.T) {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	m.cfgPath = filepath.Join(t.TempDir(), "config.yaml")
	m.state = stateConfig
	m.form = newConfigForm(config.ModelConfig{Type: "DeepSeek"}, "")
	m.form.cur = effortField
	if got := m.form.value(effortField); got != "" {
		t.Fatalf("没配过的思考强度该是默认档: %q", got)
	}
	if text := stripANSI(strings.Join(m.fieldLines(), "\n")); !strings.Contains(text, "思考强度") ||
		!strings.Contains(text, "默认") {
		t.Fatalf("表单里该有思考强度这一行，默认档显示成\"默认\":\n%s", text)
	}

	// 没有输入框可编辑：Enter 只提示一句怎么换，不进编辑态
	pressed, _ := m.handleConfigKey(tea.KeyPressMsg{Code: tea.KeyEnter})
	got := pressed.(model)
	if got.form.editing {
		t.Fatal("思考强度没有输入框，不该进编辑态")
	}
	if !strings.Contains(got.form.err, "←→") {
		t.Fatalf("该提示用 ←→ 切: %q", got.form.err)
	}

	// → 一档一档往上加。DeepSeek 认得全：none / minimal / low / medium / high / xhigh / ultra / max
	next, _ := got.handleConfigKey(tea.KeyPressMsg{Code: tea.KeyRight})
	got = next.(model)
	if v := got.form.value(effortField); v != "none" {
		t.Fatalf("→ 之后该是 none，实际 %q", v)
	}
	for _, want := range []string{"minimal", "low", "medium", "high", "xhigh", "ultra", "max"} {
		next, _ = got.handleConfigKey(tea.KeyPressMsg{Code: tea.KeyRight})
		got = next.(model)
		if v := got.form.value(effortField); v != want {
			t.Fatalf("继续按 → 该是 %q，实际 %q", want, v)
		}
	}
	// 到头再往前绕回默认档
	next, _ = got.handleConfigKey(tea.KeyPressMsg{Code: tea.KeyRight})
	got = next.(model)
	if v := got.form.value(effortField); v != "" {
		t.Fatalf("max 之后再按 → 该绕回默认，实际 %q", v)
	}
	// ← 从默认绕到最后一档
	next, _ = got.handleConfigKey(tea.KeyPressMsg{Code: tea.KeyLeft})
	got = next.(model)
	if v := got.form.value(effortField); v != "max" {
		t.Fatalf("从默认往前该绕到 max，实际 %q", v)
	}

	// 存下来：配置里带着思考强度，重建的表单也还是这一档
	got.form.inputs[keyField].SetValue("sk-effort")
	got.form.inputs[modelField].SetValue("deepseek-reasoner")
	saved, _ := got.saveConfig()
	mc, err := saved.(model).cfg.CurrentModel()
	if err != nil {
		t.Fatalf("保存后读配置失败: %v", err)
	}
	if mc.ReasoningEffort != "max" {
		t.Fatalf("思考强度没存进配置: %+v", mc)
	}
	if v := saved.(model).form.value(effortField); v != "max" {
		t.Fatalf("保存后表单里该还是 max，实际 %q", v)
	}

	// /model 列表里能看出这套模型配了什么思考强度
	if hint := modelHint(mc); !strings.Contains(hint, "思考 max") {
		t.Fatalf("/model 的说明里该带上思考强度: %q", hint)
	}

	// 配置里手写了个表外的档位：不崩，照样能往下切
	odd := newConfigForm(config.ModelConfig{Type: "DeepSeek", ReasoningEffort: "weird"}, "")
	odd.cur = effortField
	m.form = odd
	next, _ = m.handleConfigKey(tea.KeyPressMsg{Code: tea.KeyRight})
	if v := next.(model).form.value(effortField); v != "none" {
		t.Fatalf("表外的档位该从第一档接着走，实际 %q", v)
	}
}

// 表单多一格「名字」：新增时是空的，编辑时预填正在改的那套
func TestConfigFormNameField(t *testing.T) {
	// 编辑模式：名字格预填那套的名字
	f := newConfigForm(config.ModelConfig{Name: "快", Type: "DeepSeek", Model: "model-a"}, "快")
	if got := f.value(nameField); got != "快" {
		t.Fatalf("编辑时名字该预填: %q", got)
	}
	if f.editLabel != "快" {
		t.Fatalf("编辑模式该记住正在改哪套: %q", f.editLabel)
	}

	// 新增模式：名字空着，类型给默认的，地址给内置的
	blank := newConfigForm(config.ModelConfig{}, "")
	if blank.editLabel != "" {
		t.Fatalf("新增模式不该有正在改的那套: %q", blank.editLabel)
	}
	if got := blank.value(nameField); got != "" {
		t.Fatalf("新增时名字该是空的: %q", got)
	}
	if got := blank.value(typeField); got != "DeepSeek" {
		t.Fatalf("新增时类型该默认 DeepSeek: %q", got)
	}
	if got := blank.value(urlField); got != config.DefaultDeepSeekApiUrl {
		t.Fatalf("新增时地址该给内置的: %q", got)
	}

	// 名字格能填：填了就是这一套的名字
	f.inputs[nameField].SetValue("  新名字  ")
	mc, err := f.modelConfig()
	if err != nil {
		t.Fatalf("填名字不该报错: %v", err)
	}
	if mc.Name != "新名字" {
		t.Fatalf("名字没取到: %q", mc.Name)
	}
}

// 存的时候分两种：新增是追加到末尾，改现有的是改那一条（改名不额外多一套）
func TestSaveConfigAddAndRename(t *testing.T) {
	// 新增：列表多一套，当前那套不动
	m := newModel(config.GlobalConfig{Models: testModels(), Current: "快"}, nil, ".", domain.NewSession("."))
	m.cfgPath = filepath.Join(t.TempDir(), "config.yaml")
	m.state = stateConfig
	m.form = newConfigForm(config.ModelConfig{}, "")
	// ←→ 切到 MiniMax：地址得跟着换成该类型的内置地址，
	// 不然会存下一套"类型是 MiniMax、地址却是 DeepSeek"的配置
	got := m
	for i := 0; i < 2; i++ {
		next, _ := got.switchType(1)
		got = next.(model)
	}
	if got.form.value(typeField) != "MiniMax" {
		t.Fatalf("该切到 MiniMax，实际 %q", got.form.value(typeField))
	}
	if got.form.value(urlField) != config.DefaultMiniMaxApiUrl {
		t.Fatalf("地址该跟着换成 MiniMax 的: %q", got.form.value(urlField))
	}
	got.form.inputs[keyField].SetValue("sk-mm")
	got.form.inputs[modelField].SetValue("MiniMax-M3")
	saved, _ := got.saveConfig()
	after := saved.(model)
	if len(after.cfg.Models) != 3 {
		t.Fatalf("新增后该有三套，实际 %d: %+v", len(after.cfg.Models), after.cfg.Models)
	}
	if after.cfg.Models[2].Model != "MiniMax-M3" {
		t.Fatalf("新的一套没追加到末尾: %+v", after.cfg.Models)
	}
	if after.cfg.Models[2].ApiUrl != config.DefaultMiniMaxApiUrl {
		t.Fatalf("新的一套地址不对: %+v", after.cfg.Models[2])
	}
	if after.cfg.Current != "快" {
		t.Fatalf("新增不该动当前那套: %q", after.cfg.Current)
	}
	// 上下文窗口按当前那套算，不该被新增的带跑
	if after.contextLimit != 64000 {
		t.Fatalf("新增不该动上下文窗口: %d", after.contextLimit)
	}

	// 改名：列表长度不变，Current 跟着更新
	m2 := newModel(config.GlobalConfig{Models: testModels(), Current: "快"}, nil, ".", domain.NewSession("."))
	m2.cfgPath = filepath.Join(t.TempDir(), "config.yaml")
	m2.state = stateConfig
	m2.form = newConfigForm(testModels()[0], "快")
	m2.form.inputs[nameField].SetValue("飞快")
	saved2, _ := m2.saveConfig()
	after2 := saved2.(model)
	if len(after2.cfg.Models) != 2 {
		t.Fatalf("改名不该多出一套，实际 %d: %+v", len(after2.cfg.Models), after2.cfg.Models)
	}
	if after2.cfg.Models[0].Name != "飞快" {
		t.Fatalf("名字没换过来: %+v", after2.cfg.Models[0])
	}
	if after2.cfg.Current != "飞快" {
		t.Fatalf("Current 该跟着改名: %q", after2.cfg.Current)
	}

	// 编辑时把名字格清空：名字不变（不是变成模型名）
	m3 := newModel(config.GlobalConfig{Models: testModels(), Current: "快"}, nil, ".", domain.NewSession("."))
	m3.cfgPath = filepath.Join(t.TempDir(), "config.yaml")
	m3.state = stateConfig
	m3.form = newConfigForm(testModels()[0], "快")
	m3.form.inputs[nameField].SetValue("")
	saved3, _ := m3.saveConfig()
	after3 := saved3.(model)
	if after3.cfg.Models[0].Name != "快" {
		t.Fatalf("名字留空该保住原来的，实际 %q", after3.cfg.Models[0].Name)
	}
}

// 候选列表的窗口：选中的那个始终在可见范围里
func TestPickWindow(t *testing.T) {
	for _, c := range []struct {
		name      string
		n, idx    int
		rows      int
		wantStart int
		wantEnd   int
	}{
		{"装得下就全列出来", 3, 0, 8, 0, 3},
		{"开头", 20, 0, 5, 0, 5},
		{"中间", 20, 10, 5, 8, 13},
		{"末尾", 20, 19, 5, 15, 20},
		{"一行都匀不出来", 20, 3, 0, 0, 0},
	} {
		t.Run(c.name, func(t *testing.T) {
			start, end := pickWindow(c.n, c.idx, c.rows)
			if start != c.wantStart || end != c.wantEnd {
				t.Fatalf("窗口算错了: [%d,%d)，想要 [%d,%d)", start, end, c.wantStart, c.wantEnd)
			}
			if c.rows > 0 && c.n > 0 && (c.idx < start || c.idx >= end) {
				t.Fatalf("选中的第 %d 项不在窗口 [%d,%d) 里", c.idx, start, end)
			}
		})
	}
}
