package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// 内置地址按类型给：不写类型按 DeepSeek，大小写和空白都认，认不出的类型返回空串（别瞎猜）
func TestDefaultApiUrl(t *testing.T) {
	for _, c := range []struct {
		typ  string
		want string
	}{
		{"", DefaultDeepSeekApiUrl},
		{"DeepSeek", DefaultDeepSeekApiUrl},
		{"openai", DefaultOpenAIApiUrl},
		{"OpenAI", DefaultOpenAIApiUrl},
		{"MiniMax", DefaultMiniMaxApiUrl},
		{"minimax", DefaultMiniMaxApiUrl},
		{"  MiniMax  ", DefaultMiniMaxApiUrl},
		{"自家的转发网关", ""},
	} {
		if got := DefaultApiUrl(c.typ); got != c.want {
			t.Fatalf("类型 %q 的内置地址不对: %q，想要 %q", c.typ, got, c.want)
		}
	}
}

// 没填上下文上限的旧配置要退回默认窗口，填了就用填的
func TestDeepSeekConfigLimit(t *testing.T) {
	cases := []struct {
		cfg  DeepSeekConfig
		want int
	}{
		{DeepSeekConfig{}, DefaultContextLimit},
		{DeepSeekConfig{ContextLimit: 64000}, 64000},
		{DeepSeekConfig{ContextLimit: -1}, DefaultContextLimit},
	}
	for _, c := range cases {
		if got := c.cfg.Limit(); got != c.want {
			t.Fatalf("%+v 的上限应该是 %d，实际 %d", c.cfg, c.want, got)
		}
	}
}

// 保存只该换掉 Provider 这一段，文件里其它顶层字段要留着
func TestSaveProviderKeepsOtherFields(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	origin := "Other: keep\nProvider:\n  Type: DeepSeek\n  ApiUrl: https://old\n  ApiKey: sk-old\n  Model: old-model\n"
	if err := os.WriteFile(path, []byte(origin), 0o644); err != nil {
		t.Fatal(err)
	}

	want := DeepSeekConfig{
		Type:   "DeepSeek",
		ApiUrl: "https://api.deepseek.com",
		ApiKey: "sk-new",
		Model:  "deepseek-chat",
	}
	if err := SaveProvider(path, want); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Other: keep") {
		t.Fatalf("不该丢掉其它字段:\n%s", data)
	}
	var got struct {
		Provider DeepSeekConfig `yaml:"Provider"`
	}
	if err := yaml.Unmarshal(data, &got); err != nil {
		t.Fatalf("写出来的 yaml 解析不了: %v\n%s", err, data)
	}
	if got.Provider != want {
		t.Fatalf("读回来对不上: %+v\n%s", got.Provider, data)
	}
}

// 空文件、没有 Provider 段的文件都要能写进去
func TestSaveProviderFromEmptyFile(t *testing.T) {
	dir := t.TempDir()
	want := DeepSeekConfig{Type: "DeepSeek", ApiUrl: "https://api.deepseek.com", ApiKey: "sk-1", Model: "deepseek-chat"}

	for name, origin := range map[string]string{
		"空文件":         "",
		"没有 Provider": "Other: 1\n",
	} {
		path := filepath.Join(dir, strings.ReplaceAll(name, " ", "_")+".yaml")
		if err := os.WriteFile(path, []byte(origin), 0o644); err != nil {
			t.Fatal(err)
		}
		if err := SaveProvider(path, want); err != nil {
			t.Fatalf("%s 保存失败: %v", name, err)
		}
		data, err := os.ReadFile(path)
		if err != nil {
			t.Fatal(err)
		}
		var got struct {
			Provider DeepSeekConfig `yaml:"Provider"`
		}
		if err := yaml.Unmarshal(data, &got); err != nil {
			t.Fatalf("%s 写出来的 yaml 解析不了: %v\n%s", name, err, data)
		}
		if got.Provider != want {
			t.Fatalf("%s 读回来对不上: %+v\n%s", name, got.Provider, data)
		}
	}
}

// 思考强度：留空合法（不发这个参数），档位名大小写和空白都认，表外的值必须报错
func TestParseEffort(t *testing.T) {
	for _, c := range []struct {
		in   string
		want string
	}{
		{"", ""},
		{"   ", ""},
		{"low", "low"},
		{"  Medium ", "medium"},
		{"HIGH", "high"},
		{"minimal", "minimal"},
	} {
		got, err := ParseEffort(c.in)
		if err != nil {
			t.Fatalf("%q 该认下来: %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("%q 归一成 %q，想要 %q", c.in, got, c.want)
		}
	}

	// 手打进配置文件里的怪值不能悄悄发给接口：这里就得报错，并且说清楚有哪些档位
	for _, bad := range []string{"high高", "ludicrous", "1"} {
		if _, err := ParseEffort(bad); err == nil {
			t.Fatalf("%q 不该被认下来", bad)
		} else if !strings.Contains(err.Error(), "minimal") {
			t.Fatalf("报错里该列出档位提醒: %v", err)
		}
	}
}

// 档位的显示名：空档叫"默认"，其余就是档位名
func TestEffortLabel(t *testing.T) {
	if got := EffortLabel(""); got != "默认" {
		t.Fatalf("空档该显示成默认: %q", got)
	}
	if got := EffortLabel(" Medium "); got != "medium" {
		t.Fatalf("档位名没归一: %q", got)
	}
	if got := strings.Join(EffortLabels(), "/"); got != "默认/minimal/low/medium/high" {
		t.Fatalf("档位表不对: %q", got)
	}
}

// 思考强度要能存进配置、也能从配置读回来；没配的时候不写这一行，老配置读起来不受影响
func TestSaveModelsKeepsEffort(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	models := []ModelConfig{{
		Name:            "快的",
		Type:            "DeepSeek",
		ApiUrl:          "https://api.deepseek.com",
		ApiKey:          "sk-1",
		Model:           "deepseek-chat",
		ReasoningEffort: "high",
	}}
	if err := SaveModels(path, models, "快的"); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	var cfg GlobalConfig
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		t.Fatalf("写出来的 yaml 解析不了: %v\n%s", err, data)
	}
	mc, err := cfg.CurrentModel()
	if err != nil {
		t.Fatalf("读当前模型失败: %v", err)
	}
	if mc.ReasoningEffort != "high" {
		t.Fatalf("思考强度没存下来: %+v\n%s", mc, data)
	}

	// 没配思考强度的模型，文件里不该冒出这一行
	plain := []ModelConfig{{
		Type:   "DeepSeek",
		ApiUrl: "https://api.deepseek.com",
		ApiKey: "sk-2",
		Model:  "deepseek-chat",
	}}
	plainPath := filepath.Join(t.TempDir(), "config.yaml")
	if err := SaveModels(plainPath, plain, ""); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	plainData, err := os.ReadFile(plainPath)
	if err != nil {
		t.Fatal(err)
	}
	if strings.Contains(string(plainData), "ReasoningEffort") {
		t.Fatalf("没配就不该写这一行:\n%s", plainData)
	}
}
