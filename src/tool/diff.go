package tool

import (
	"strings"

	"hamusuta/agent-dev/src/domain"
)

// approvalDiffLines 是改动预览里"删掉的"和"加上的"各自最多显示几行。
// 确认框是用来扫一眼的，铺满整屏反而看不清重点。
const approvalDiffLines = 8

// newDiffPreview 把"去掉什么"和"加上什么"整理成确认框里的预览。
// 两边都是整段文本，按行切开后各留开头几行，超出的部分用 Truncated 标出来。
func newDiffPreview(removed, added string) *domain.DiffPreview {
	d := &domain.DiffPreview{
		Removed:   headTextLines(removed, approvalDiffLines),
		Added:     headTextLines(added, approvalDiffLines),
		Truncated: countTextLines(removed) > approvalDiffLines || countTextLines(added) > approvalDiffLines,
	}
	if d.Empty() {
		return nil
	}
	return d
}

// headTextLines 取前 max 行，末尾的空行不算内容。
// 行首的行号前缀（从 Read 结果里连行号一起复制过来的）先去掉，预览里看着才干净。
func headTextLines(s string, max int) []string {
	s = strings.TrimRight(stripLineNumbers(s), "\r\n")
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	if len(lines) > max {
		lines = lines[:max]
	}
	for i, line := range lines {
		lines[i] = strings.TrimRight(line, "\r")
	}
	return lines
}

// countTextLines 数一段文本有多少行，末尾的空行不算
func countTextLines(s string) int {
	s = strings.TrimRight(strings.TrimRight(s, "\r\n"), "\r\n")
	if s == "" {
		return 0
	}
	return strings.Count(s, "\n") + 1
}
