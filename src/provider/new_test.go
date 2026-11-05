package provider

import (
	"testing"

	"hamusuta/agent-dev/src/config"
)

// 内置的几家都走同一套 OpenAI 兼容协议，Type 写哪家都该造出 provider；
// 认不出的类型要报错，别让人以为配上了却跑不通
func TestNewProviderTypes(t *testing.T) {
	for _, typ := range []string{"", "DeepSeek", "deepseek", "OpenAI", "MiniMax", "minimax"} {
		p, err := New(config.ModelConfig{
			Type:   typ,
			ApiUrl: "https://example.com",
			ApiKey: "sk-test",
			Model:  "test-model",
		})
		if err != nil {
			t.Fatalf("类型 %q 该能造出 provider: %v", typ, err)
		}
		if p == nil {
			t.Fatalf("类型 %q 造出了空 provider", typ)
		}
	}

	if _, err := New(config.ModelConfig{
		Type:   "别家的协议",
		ApiUrl: "https://example.com",
		ApiKey: "sk-test",
		Model:  "test-model",
	}); err == nil {
		t.Fatal("认不出的类型该报错")
	}
}
