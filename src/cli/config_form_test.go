package main

import (
	"errors"
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
			if got := newConfigForm(c.mc).value(urlField); got != c.want {
				t.Fatalf("表单里的地址不对: %q，想要 %q", got, c.want)
			}
		})
	}

	// 保存时地址那一格被清空也不该报错：内置地址顶上
	f := newConfigForm(config.ModelConfig{Type: "DeepSeek"})
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
	m.form = newConfigForm(config.ModelConfig{Type: "DeepSeek", ApiKey: "sk-test"})
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
	m.form = newConfigForm(config.ModelConfig{Type: "DeepSeek", ApiKey: "sk-test"})
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
	m.form = newConfigForm(config.ModelConfig{Type: "DeepSeek"})

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
