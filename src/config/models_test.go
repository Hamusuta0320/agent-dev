package config

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"gopkg.in/yaml.v3"
)

// 只有老的 Provider 段时，那一套就是唯一的一套；完全没配时一个都没有
func TestAvailableModelsFallsBackToProvider(t *testing.T) {
	var cfg GlobalConfig
	if err := cfg.Provider.Encode(ModelConfig{
		Type: "DeepSeek", ApiUrl: "https://api.deepseek.com", ApiKey: "sk-1", Model: "deepseek-chat",
	}); err != nil {
		t.Fatalf("造配置失败: %v", err)
	}
	models := cfg.AvailableModels()
	if len(models) != 1 {
		t.Fatalf("老配置应该算一套模型，实际 %+v", models)
	}
	if got := models[0].Label(); got != "deepseek-chat" {
		t.Fatalf("没填 Name 时该用模型名当标签，实际 %q", got)
	}
	cur, err := cfg.CurrentModel()
	if err != nil || cur.Model != "deepseek-chat" {
		t.Fatalf("Current 留空应该取第一套: %+v %v", cur, err)
	}

	// 什么都没有时别编一个空配置出来
	var empty GlobalConfig
	if got := empty.AvailableModels(); len(got) != 0 {
		t.Fatalf("没配模型时不该有可用模型: %+v", got)
	}
	if _, err := empty.CurrentModel(); err == nil {
		t.Fatal("没配模型时应该报错，让人知道去填配置")
	}
}

// Current 写错名字要报错，不能静默退回别的模型
func TestCurrentModelPicksByLabelOrModelName(t *testing.T) {
	models := []ModelConfig{
		{Name: "快", Model: "model-a"},
		{Name: "稳", Model: "model-b"},
	}
	cfg := GlobalConfig{Models: models, Current: "稳"}
	if cur, err := cfg.CurrentModel(); err != nil || cur.Model != "model-b" {
		t.Fatalf("按名字应该挑中稳: %+v %v", cur, err)
	}

	// 直接写模型名也认，大小写不敏感
	cfg.Current = "MODEL-A"
	if cur, err := cfg.CurrentModel(); err != nil || cur.Model != "model-a" {
		t.Fatalf("按模型名应该挑中快: %+v %v", cur, err)
	}

	cfg.Current = "不存在的名字"
	if _, err := cfg.CurrentModel(); err == nil || !strings.Contains(err.Error(), "快") {
		t.Fatalf("写错名字要报错并列出有哪些: %v", err)
	}
}

// 保存多套模型：其它顶层字段留着，Provider 段同步成当前这套，读回来还是同一份
func TestSaveModelsRoundTrip(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	origin := "Mode: read\nProvider:\n  Type: DeepSeek\n  ApiUrl: https://old\n  ApiKey: sk-old\n  Model: old-model\n"
	if err := os.WriteFile(path, []byte(origin), 0o644); err != nil {
		t.Fatal(err)
	}

	models := []ModelConfig{
		{Name: "快", Type: "DeepSeek", ApiUrl: "https://a", ApiKey: "sk-a", Model: "model-a", ContextLimit: 64000},
		{Type: "OpenAI", ApiUrl: "https://b", ApiKey: "sk-b", Model: "model-b"},
	}
	if err := SaveModels(path, models, "快"); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatal(err)
	}
	if !strings.Contains(string(data), "Mode: read") {
		t.Fatalf("不该丢掉别的顶层字段:\n%s", data)
	}

	var got GlobalConfig
	if err := yaml.Unmarshal(data, &got); err != nil {
		t.Fatalf("写出来的 yaml 解析不了: %v\n%s", err, data)
	}
	if len(got.Models) != 2 || got.Current != "快" {
		t.Fatalf("模型列表没写对: %+v\n%s", got, data)
	}
	// 没填 Name 的那套保存时用模型名补上，/model 才有得叫
	if got.Models[1].Name != "model-b" {
		t.Fatalf("没名字的模型该用模型名补上: %+v", got.Models[1])
	}
	cur, err := got.CurrentModel()
	if err != nil || cur.Model != "model-a" {
		t.Fatalf("读回来的当前模型不对: %+v %v", cur, err)
	}

	// 老的 Provider 段同步成当前这套，且不带 Name
	var legacy ModelConfig
	if err := got.Provider.Decode(&legacy); err != nil {
		t.Fatalf("Provider 段解不开: %v", err)
	}
	if legacy.Model != "model-a" || legacy.Name != "" {
		t.Fatalf("Provider 段该同步成当前这套（不带 Name）: %+v", legacy)
	}

	// 当前指向不存在的那套要报错，别把 Current 悄悄改成别的
	if err := SaveModels(path, models, "没有这个"); err == nil {
		t.Fatal("Current 认不出时应该报错")
	}
}

// 改现有那套：改名是原地换，Current 跟着更新，列表长度不变
func TestEditModelRenames(t *testing.T) {
	models := []ModelConfig{
		{Name: "快", Type: "DeepSeek", ApiUrl: "https://a", ApiKey: "sk-a", Model: "model-a"},
		{Name: "稳", Type: "OpenAI", ApiUrl: "https://b", ApiKey: "sk-b", Model: "model-b"},
	}
	next, current, err := EditModel(models, "快", "快", ModelConfig{
		Name: "飞快", Type: "DeepSeek", ApiUrl: "https://a", ApiKey: "sk-a", Model: "model-a",
	})
	if err != nil {
		t.Fatalf("改名失败: %v", err)
	}
	if len(next) != 2 {
		t.Fatalf("改名不该多出一套，实际 %d 套: %+v", len(next), next)
	}
	if next[0].Name != "飞快" {
		t.Fatalf("名字没换过来: %+v", next[0])
	}
	if current != "飞快" {
		t.Fatalf("Current 该跟着改名，实际 %q", current)
	}

	// 改的不是当前那套：Current 不动
	_, current, err = EditModel(models, "快", "稳", ModelConfig{Name: "稳", Type: "OpenAI", Model: "model-b"})
	if err != nil {
		t.Fatalf("改别的失败: %v", err)
	}
	if current != "快" {
		t.Fatalf("改别的时 Current 不该动，实际 %q", current)
	}

	// 新名字和别的撞了：报错，不覆盖
	if _, _, err := EditModel(models, "快", "快", ModelConfig{Name: "稳", Model: "model-a"}); err == nil {
		t.Fatal("撞名时应该报错")
	}
	// 找不到要改的那套：报错
	if _, _, err := EditModel(models, "快", "没有这个", ModelConfig{Name: "x", Model: "m"}); err == nil {
		t.Fatal("找不到时应该报错")
	}
}

// 新增一套：追加到末尾、Current 不动、撞名报错
func TestAddModel(t *testing.T) {
	models := []ModelConfig{{Name: "快", Model: "model-a"}}
	next, err := AddModel(models, ModelConfig{Name: "稳", Type: "OpenAI", Model: "model-b"})
	if err != nil {
		t.Fatalf("新增失败: %v", err)
	}
	if len(next) != 2 || next[1].Name != "稳" {
		t.Fatalf("新的一套该追加到末尾: %+v", next)
	}
	if next[0].Name != "快" {
		t.Fatalf("原来那套不该被动: %+v", next[0])
	}
	// 名字留空用模型名兜底
	next, err = AddModel(models, ModelConfig{Model: "model-c"})
	if err != nil {
		t.Fatalf("新增失败: %v", err)
	}
	if next[1].Name != "model-c" {
		t.Fatalf("名字留空该用模型名: %+v", next[1])
	}
	// 撞名报错，不覆盖
	if _, err := AddModel(models, ModelConfig{Name: "快", Model: "model-x"}); err == nil {
		t.Fatal("撞名时应该报错")
	}
}

// 删一套：删的是当前那套就改用剩下的第一套，只剩一套删不掉
func TestDeleteModel(t *testing.T) {
	models := []ModelConfig{
		{Name: "快", Model: "model-a"},
		{Name: "稳", Model: "model-b"},
		{Name: "慢", Model: "model-c"},
	}
	next, current, err := DeleteModel(models, "快", "快")
	if err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if len(next) != 2 {
		t.Fatalf("该少一套，实际 %d 套: %+v", len(next), next)
	}
	if current != "稳" {
		t.Fatalf("删掉当前那套该改用剩下的第一套，实际 %q", current)
	}
	// 删的不是当前那套：Current 不动
	_, current, err = DeleteModel(models, "快", "慢")
	if err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if current != "快" {
		t.Fatalf("Current 不该动，实际 %q", current)
	}
	// 只剩一套：删不掉
	if _, _, err := DeleteModel(models[:1], "快", "快"); err == nil {
		t.Fatal("只剩一套时应该报错")
	}
	// 找不到：报错
	if _, _, err := DeleteModel(models, "快", "没有这个"); err == nil {
		t.Fatal("找不到时应该报错")
	}
}
