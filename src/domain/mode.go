package domain

import (
	"fmt"
	"strings"
)

// Mode 是工作模式，决定模型能拿到哪些工具、写操作要不要用户点头。
// core 用它决定要不要问、界面用它决定显示什么，所以放在 domain 里共用。
type Mode int

const (
	// ModeReadOnly 只读：只给读类工具，项目里的东西一律改不了
	ModeReadOnly Mode = iota
	// ModeAsk 编辑需授权：写类工具照给，但每次调用前都要用户确认
	ModeAsk
	// ModeFull 完全授权：写类工具直接执行，不再确认
	ModeFull
)

// DefaultMode 是没配置时的默认模式：宁可多问一句，也别悄悄改坏东西
const DefaultMode = ModeAsk

// String 是模式在配置文件和 /mode 命令里的写法
func (m Mode) String() string {
	switch m {
	case ModeReadOnly:
		return "read"
	case ModeFull:
		return "full"
	default:
		return "ask"
	}
}

// Label 是模式给人看的中文名字，界面、帮助和系统提示词都用它
func (m Mode) Label() string {
	switch m {
	case ModeReadOnly:
		return "只读"
	case ModeFull:
		return "完全授权"
	default:
		return "编辑需授权"
	}
}

// ParseMode 解析配置文件和 /mode 命令里的写法：read / ask / full，
// 也认几个中英文别名，省得记不住。空串按默认模式处理。
func ParseMode(s string) (Mode, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "":
		return DefaultMode, nil
	case "read", "readonly", "read-only", "ro", "只读":
		return ModeReadOnly, nil
	case "ask", "edit", "编辑", "编辑需授权":
		return ModeAsk, nil
	case "full", "auto", "完全授权", "完全":
		return ModeFull, nil
	}
	return DefaultMode, fmt.Errorf("认不出工作模式 %q，可选 read / ask / full", s)
}
