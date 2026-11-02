package tool

import (
	"fmt"
	"strings"
)

const (
	// editEchoContextLines 回显里替换位置前后各带几行上下文
	editEchoContextLines = 2
	// editEchoMaxLines 回显里 -/+ 每边最多显示几行
	editEchoMaxLines = 6
)

// editEcho 拼出"改完的回显"：替换位置前后几行上下文，去掉的行以 "- " 开头、加上的以 "+ " 开头。
// text 是改之前的全文，[start,end) 是被替换掉的那一段，added 是换上去的内容，
// total 是这次一共替换了几处（多于一处时说明回显的是第一处）。
func editEcho(text string, start, end int, added string, total int) string {
	if start < 0 || end < start || end > len(text) {
		return ""
	}
	spans := splitLines(text)
	first := lineIndexAt(spans, start)
	if first < 0 {
		return ""
	}
	last := first
	for last+1 < len(spans) && spans[last+1][0] < end {
		last++
	}

	// 替换只落在行内的一部分时，把该行没被换掉的前后两截补上，回显里才是完整的行
	prefix := text[spans[first][0]:start]
	suffix := strings.TrimRight(text[end:spans[last][1]], "\r\n")
	oldLines := echoBlockLines(prefix, text[start:end], suffix)
	newLines := echoBlockLines(prefix, added, suffix)
	if len(oldLines) == 0 && len(newLines) == 0 {
		return ""
	}

	var sb strings.Builder
	switch {
	case last > first:
		fmt.Fprintf(&sb, "第 %d-%d 行", first+1, last+1)
	default:
		fmt.Fprintf(&sb, "第 %d 行", first+1)
	}
	if total > 1 {
		fmt.Fprintf(&sb, "（共替换 %d 处，这里显示第一处）", total)
	}
	sb.WriteString(":\n")

	// 上文
	for i := max(0, first-editEchoContextLines); i < first; i++ {
		fmt.Fprintf(&sb, "   %s\n", echoLine(text, spans[i]))
	}
	writeEchoBlock(&sb, "-", oldLines, "（没有改到具体内容）")
	writeEchoBlock(&sb, "+", newLines, "（这一段被删掉了）")
	// 下文
	for i := last + 1; i <= last+editEchoContextLines && i < len(spans); i++ {
		fmt.Fprintf(&sb, "   %s\n", echoLine(text, spans[i]))
	}
	return strings.TrimRight(sb.String(), "\n")
}

// writeEchoBlock 输出一边的内容（- 或 +），行数太多时只留前面几行并说明还剩多少
func writeEchoBlock(sb *strings.Builder, mark string, lines []string, empty string) {
	if len(lines) == 0 {
		fmt.Fprintf(sb, "%s %s\n", mark, empty)
		return
	}
	shown := lines
	if len(shown) > editEchoMaxLines {
		shown = shown[:editEchoMaxLines]
	}
	for _, ln := range shown {
		fmt.Fprintf(sb, "%s %s\n", mark, truncateGrepLine(ln, maxGrepDisplayBytes))
	}
	if rest := len(lines) - len(shown); rest > 0 {
		fmt.Fprintf(sb, "  …（还有 %d 行没显示）\n", rest)
	}
}

// echoBlockLines 把"行内前缀 + 被替换的原文 + 行内后缀"拼成完整的几行。
// 末尾的换行去掉，免得回显里多出一个空行。
func echoBlockLines(prefix, body, suffix string) []string {
	s := prefix + body + suffix
	s = strings.TrimSuffix(s, "\n")
	s = strings.TrimSuffix(s, "\r")
	if s == "" {
		return nil
	}
	lines := strings.Split(s, "\n")
	for i, ln := range lines {
		lines[i] = strings.TrimRight(ln, "\r")
	}
	return lines
}

// lineIndexAt 返回位置 pos 落在第几行（0 起），pos 在文件末尾时算最后一行
func lineIndexAt(spans [][2]int, pos int) int {
	if len(spans) == 0 {
		return -1
	}
	for i, sp := range spans {
		if pos < sp[1] {
			return i
		}
	}
	return len(spans) - 1
}

// echoLine 取一行内容做回显，去掉行尾换行
func echoLine(text string, span [2]int) string {
	return strings.TrimRight(text[span[0]:span[1]], "\r\n")
}
