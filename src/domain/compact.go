package domain

import (
	"fmt"
	"strings"
)

const (
	// compactLineRunes 是压缩工具输出时单行保留的字符数上限
	compactLineRunes = 200
	// compactKeepLines 是总行数超限时，头尾各保留的行数
	compactKeepLines = 2
)

// TruncateToolContent 把较早的工具输出压短，只用于发给模型的视图；
// 会话里还留着原文，事后翻会话文件能看到完整结果。
// 单行超过 200 字符只留前 200；总行数超过 4 行时只留前两行和后两行，中间说明省略了多少行。
func TruncateToolContent(content string) string {
	if content == "" {
		return ""
	}
	lines := strings.Split(strings.TrimRight(content, "\n"), "\n")

	omitted := 0
	if len(lines) > compactKeepLines*2 {
		omitted = len(lines) - compactKeepLines*2
		lines = append(append([]string{}, lines[:compactKeepLines]...), lines[len(lines)-compactKeepLines:]...)
	}
	for i, line := range lines {
		lines[i] = truncateLine(line)
	}
	if omitted == 0 {
		return strings.Join(lines, "\n")
	}
	return fmt.Sprintf("%s\n…（省略 %d 行）\n%s",
		strings.Join(lines[:compactKeepLines], "\n"),
		omitted,
		strings.Join(lines[compactKeepLines:], "\n"))
}

// truncateLine 把单行压到 compactLineRunes 个字符以内，并说明原来有多长
func truncateLine(line string) string {
	runes := []rune(line)
	if len(runes) <= compactLineRunes {
		return line
	}
	return string(runes[:compactLineRunes]) + fmt.Sprintf("…（本行共 %d 字符）", len(runes))
}
