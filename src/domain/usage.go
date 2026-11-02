package domain

import (
	"fmt"
	"math"
	"strconv"
	"strings"
)

// 上下文窗口这类数字在模型侧都是 2 的整数次幂，所以单位按 1024 进换算：
// 1k = 1024、1M = 1024k = 1048576，跟硬盘厂商的 1000 进制不是一回事。
// 例外是 Anthropic 的 200k 窗口，那就是整数 200000，直接填数字，跟换算无关。
const (
	kilo = 1 << 10
	mega = 1 << 20
	giga = 1 << 30
)

// ParseTokens 把带单位的 token 数解析成具体数字，比如 "128k" → 131072、"1.5M" → 1572864。
// 不带单位（"128000"）就按原数字算；k/M/G 大小写都认，后面多写个 b（"128kb"）也认，
// 单位前带空格（"128 k"）也行。空值、非数字、0 和负数都返回错误。
func ParseTokens(s string) (int, error) {
	t := strings.ToLower(strings.TrimSpace(s))
	t = strings.TrimSuffix(t, "b")
	t = strings.TrimSpace(t)
	if t == "" {
		return 0, fmt.Errorf("token 数不能为空")
	}

	mult := 1
	switch t[len(t)-1] {
	case 'k':
		mult, t = kilo, t[:len(t)-1]
	case 'm':
		mult, t = mega, t[:len(t)-1]
	case 'g':
		mult, t = giga, t[:len(t)-1]
	}
	t = strings.TrimSpace(t)
	if t == "" {
		return 0, fmt.Errorf("%q 只有单位没有数字", s)
	}

	f, err := strconv.ParseFloat(t, 64)
	if err != nil {
		return 0, fmt.Errorf("%q 不是数字", s)
	}
	v := f * float64(mult)
	// 上下文窗口再怎么大也到不了 2^31，超过就是填错了
	if math.IsNaN(v) || v < 1 || v > math.MaxInt32 {
		return 0, fmt.Errorf("%q 换算后不是正整数", s)
	}
	return int(math.Round(v)), nil
}

// ContextUsage 是一次请求的上下文占用。
// PromptTokens / CompletionTokens 来自响应的 usage，是真值；
// System / Tool / Message 三项是把 PromptTokens 按本地估算的比例拆出来的
// （接口只报总数，不报这三部分各占多少），所以三者之和正好等于 PromptTokens。
type ContextUsage struct {
	// SystemTokens 是系统提示词那部分的占用
	SystemTokens int
	// ToolTokens 是工具定义（名字、说明和参数 schema）那部分的占用
	ToolTokens int
	// MessageTokens 是对话消息（含工具结果）那部分的占用
	MessageTokens int
	// PromptTokens 是响应里报的本次 prompt 总 token 数
	PromptTokens int
	// CompletionTokens 是这次回答（含思考）消耗的 token 数
	CompletionTokens int
	// ContextLimit 是模型的上下文窗口（token），用来算已用百分比；
	// 不知道窗口大小时为 0，此时只显示用量不显示百分比
	ContextLimit int
}

// Total 是这次请求的 token 总量：prompt + 回答
func (u ContextUsage) Total() int {
	return u.PromptTokens + u.CompletionTokens
}

// Percent 返回 n 占 prompt 的百分比，prompt 为 0 时返回 0
func (u ContextUsage) Percent(n int) int {
	if u.PromptTokens <= 0 {
		return 0
	}
	return n * 100 / u.PromptTokens
}

// LimitPercent 返回 prompt 占上下文窗口的百分比，窗口大小未知时返回 0
func (u ContextUsage) LimitPercent() int {
	if u.PromptTokens <= 0 || u.ContextLimit <= 0 {
		return 0
	}
	return u.PromptTokens * 100 / u.ContextLimit
}

// FormatTokens 把 token 数压成给人看的样子。单位跟 ParseTokens 一样按 1024 进，
// 所以屏幕上看到的写法原样填回配置就是同一个值（"128k" ↔ 131072）：
// 一万以内保留一位小数，万以上取整；到了 M 级别就换成 M，免得 1M 的窗口显示成 1024k
func FormatTokens(n int) string {
	switch {
	case n < kilo:
		return fmt.Sprint(n)
	case n < 10*kilo:
		return fmt.Sprintf("%.1fk", float64(n)/kilo)
	case n < mega:
		return fmt.Sprintf("%.0fk", float64(n)/kilo)
	case n < 10*mega:
		return fmt.Sprintf("%.1fM", float64(n)/mega)
	default:
		return fmt.Sprintf("%.0fM", float64(n)/mega)
	}
}
