package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

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
