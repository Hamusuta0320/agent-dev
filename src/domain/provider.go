package domain

// Provider 是 AI 服务方的最小抽象。
// 接口定义在 domain 层，具体实现（如 provider.Deepseek）反向依赖 domain，
// 避免 domain -> provider -> domain 的循环导入。
type Provider interface {
	Chat(ctx Ctx, messages []AIChatMessage) (string, error)
}
