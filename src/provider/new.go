package provider

import (
	"fmt"
	"strings"

	"hamusuta/agent-dev/src/config"
	"hamusuta/agent-dev/src/domain"
)

// New 按配置里的 Type 造 provider。DeepSeek、OpenAI 和 MiniMax 走的是同一套 OpenAI 兼容协议
// （/chat/completions + Bearer key + stream），差别只在地址和模型名，都由配置自己填，
// 所以这里都交给同一个实现；Type 留空按 DeepSeek 处理，老配置里没写 Type 也能跑
func New(mc config.ModelConfig) (domain.Provider, error) {
	switch strings.ToLower(strings.TrimSpace(mc.Type)) {
	case "", "deepseek", "openai", "minimax":
		p, err := NewDeepSeek(mc)
		if err != nil {
			return nil, err
		}
		return p, nil
	default:
		return nil, fmt.Errorf("未支持的模型类型 %q（目前只支持 OpenAI 兼容接口：DeepSeek / OpenAI / MiniMax）", mc.Type)
	}
}
