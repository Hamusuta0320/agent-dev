package tool

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"regexp"
	"strings"
	"unicode/utf8"
)

// maxEditFileBytes 单次编辑允许的最大文件大小，避免误改大文件
const maxEditFileBytes = 10 * 1024 * 1024

// lineNumberPrefix 匹配 Read 输出里的行号前缀，例如 `    12→`
var lineNumberPrefix = regexp.MustCompile(`^\s*\d+→`)

type EditTool struct {
}

func (t EditTool) Name() string {
	return "Edit"
}

func (t EditTool) Description() string {
	return "把文件里的一段原文替换成新内容，返回替换的处数。" +
		"old_string 必须与文件内容逐字符一致（缩进、空格、换行都要一样），默认还要求它在文件中唯一，不唯一时请多带几行上下文，或设置 replace_all=true。" +
		"可以直接从 Read 的结果里连着行号一起复制，行号前缀会被自动去掉；行尾空白有细微差异也能匹配上。" +
		"改之前建议先用 Read 看一眼原文。"
}

func (t EditTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "要修改的文件路径",
			},
			"old_string": map[string]any{
				"type":        "string",
				"description": "要被替换掉的原文，必须与文件内容完全一致",
			},
			"new_string": map[string]any{
				"type":        "string",
				"description": "替换后的新内容，传空字符串表示删除这段原文",
			},
			"replace_all": map[string]any{
				"type":        "boolean",
				"description": "是否替换全部匹配项，默认 false（要求 old_string 唯一）",
			},
		},
		"required": []string{"path", "old_string", "new_string"},
	}
}

type EditArgs struct {
	Path       string `json:"path"`
	OldString  string `json:"old_string"`
	NewString  string `json:"new_string"`
	ReplaceAll bool   `json:"replace_all"`
}

func (t EditTool) Execute(rawArgs json.RawMessage) (string, error) {
	if len(rawArgs) == 0 {
		return "", fmt.Errorf("缺少参数: path / old_string / new_string")
	}

	args := new(EditArgs)
	if err := json.Unmarshal(rawArgs, args); err != nil {
		return "", fmt.Errorf("参数格式错误: %w", err)
	}
	if strings.TrimSpace(args.Path) == "" {
		return "", fmt.Errorf("path 不能为空")
	}

	// 允许把 Read 输出里的行号一起复制进来
	oldStr := stripLineNumbers(args.OldString)
	newStr := stripLineNumbers(args.NewString)
	if strings.TrimSpace(oldStr) == "" {
		return "", fmt.Errorf("old_string 不能为空")
	}
	if oldStr == newStr {
		return "", fmt.Errorf("old_string 与 new_string 相同，无需修改")
	}

	info, err := os.Stat(args.Path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("文件不存在: %s（新建文件请用 Write）", args.Path)
		}
		return "", fmt.Errorf("读取 %s 失败: %w", args.Path, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s 是目录，不是文件", args.Path)
	}
	if info.Size() > maxEditFileBytes {
		return "", fmt.Errorf("%s 太大（%d 字节），超过 %d 字节上限", args.Path, info.Size(), maxEditFileBytes)
	}

	data, err := os.ReadFile(args.Path)
	if err != nil {
		return "", fmt.Errorf("读取 %s 失败: %w", args.Path, err)
	}
	if !utf8.Valid(data) {
		return "", fmt.Errorf("%s 不是文本文件，无法编辑", args.Path)
	}

	text := string(data)

	// 文件用 CRLF 而传入的是 LF 时先统一，避免"内容一模一样却匹配不上"
	if !strings.Contains(oldStr, "\r\n") && strings.Contains(text, "\r\n") {
		oldStr, newStr = toCRLF(oldStr), toCRLF(newStr)
	}

	var updated string
	replaced := 0
	linewise := false

	if n := strings.Count(text, oldStr); n > 0 {
		if !args.ReplaceAll && n != 1 {
			return "", fmt.Errorf("old_string 在 %s 中出现 %d 次，不唯一；请多带几行上下文，或设置 replace_all=true", args.Path, n)
		}
		replaced = n
		limit := -1
		if !args.ReplaceAll {
			replaced, limit = 1, 1
		}
		updated = strings.Replace(text, oldStr, newStr, limit)
	} else {
		// 逐字符没匹配上，退一步按行比较，忽略行尾空白差异
		ranges := findLinewiseMatches(text, oldStr)
		if len(ranges) == 0 {
			return "", fmt.Errorf("在 %s 中没有找到 old_string%s；请先用 Read 确认原文（缩进、空格、换行需完全一致）", args.Path, notFoundHint(text, oldStr))
		}
		if !args.ReplaceAll && len(ranges) != 1 {
			return "", fmt.Errorf("old_string 在 %s 中出现 %d 次，不唯一；请多带几行上下文，或设置 replace_all=true", args.Path, len(ranges))
		}
		if !args.ReplaceAll {
			ranges = ranges[:1]
		}
		replaced = len(ranges)
		updated = applyRanges(text, ranges, newStr)
		linewise = true
	}

	if err := os.WriteFile(args.Path, []byte(updated), info.Mode().Perm()); err != nil {
		return "", fmt.Errorf("写入 %s 失败: %w", args.Path, err)
	}

	how := ""
	if linewise {
		how = "（按行匹配，忽略了行尾空白差异）"
	}
	return fmt.Sprintf("已修改 %s，替换 %d 处，文件现在 %d 字节%s", args.Path, replaced, len(updated), how), nil
}

// stripLineNumbers 如果整段里每一行都带 Read 的行号前缀（`    12→`），就整段去掉；
// 只要有一行没有前缀就原样返回，避免误伤正常内容。
func stripLineNumbers(s string) string {
	lines := strings.Split(s, "\n")
	stripped := make([]string, 0, len(lines))
	all := true
	nonEmpty := 0
	for _, ln := range lines {
		if strings.TrimSpace(ln) == "" {
			stripped = append(stripped, ln)
			continue
		}
		nonEmpty++
		if m := lineNumberPrefix.FindString(ln); m != "" {
			stripped = append(stripped, ln[len(m):])
		} else {
			all = false
			stripped = append(stripped, ln)
		}
	}
	if nonEmpty == 0 || !all {
		return s
	}
	return strings.Join(stripped, "\n")
}

// notFoundHint 没匹配上时给一点线索：old_string 的首行出现在哪些行
func notFoundHint(text, block string) string {
	first := block
	if i := strings.IndexByte(block, '\n'); i >= 0 {
		first = block[:i]
	}
	first = strings.TrimSpace(lineNumberPrefix.ReplaceAllString(first, ""))
	if first == "" {
		return ""
	}
	var hits []string
	for i, sp := range splitLines(text) {
		if strings.Contains(text[sp[0]:sp[1]], first) {
			hits = append(hits, fmt.Sprintf("第 %d 行", i+1))
			if len(hits) >= 3 {
				break
			}
		}
	}
	if len(hits) == 0 {
		return ""
	}
	return "（内容相近的有 " + strings.Join(hits, "、") + "，注意缩进和行尾空白）"
}

// splitLines 返回每一行的字节区间，区间包含行尾换行符
func splitLines(text string) [][2]int {
	var spans [][2]int
	start := 0
	for i := 0; i < len(text); i++ {
		if text[i] == '\n' {
			spans = append(spans, [2]int{start, i + 1})
			start = i + 1
		}
	}
	if start < len(text) {
		spans = append(spans, [2]int{start, len(text)})
	}
	return spans
}

// lineContent 取一行内容，去掉行尾换行和行尾空白，用于宽松比较
func lineContent(text string, span [2]int) string {
	return strings.TrimRight(text[span[0]:span[1]], " \t\r\n")
}

// findLinewiseMatches 按行逐行比较（忽略行尾空白差异），返回所有匹配的字节区间
func findLinewiseMatches(text, block string) [][2]int {
	textLines := splitLines(text)
	blockLines := strings.Split(block, "\n")
	for len(blockLines) > 0 && strings.TrimSpace(blockLines[len(blockLines)-1]) == "" {
		blockLines = blockLines[:len(blockLines)-1]
	}
	if len(blockLines) == 0 || len(blockLines) > len(textLines) {
		return nil
	}

	var out [][2]int
	for i := 0; i+len(blockLines) <= len(textLines); i++ {
		ok := true
		for j, bl := range blockLines {
			want := strings.TrimRight(bl, " \t\r")
			if lineContent(text, textLines[i+j]) != want {
				ok = false
				break
			}
		}
		if !ok {
			continue
		}
		out = append(out, [2]int{textLines[i][0], textLines[i+len(blockLines)-1][1]})
		i += len(blockLines) - 1 // 匹配区间不重叠
	}
	return out
}

// applyRanges 用 replacement 依次替换 text 里的若干区间
func applyRanges(text string, ranges [][2]int, replacement string) string {
	var sb strings.Builder
	last := 0
	for _, rg := range ranges {
		if rg[0] < last || rg[1] > len(text) {
			continue
		}
		sb.WriteString(text[last:rg[0]])
		sb.WriteString(fixLineEnding(replacement, text[rg[0]:rg[1]]))
		last = rg[1]
	}
	sb.WriteString(text[last:])
	return sb.String()
}

// fixLineEnding 让替换内容跟被替换区间用同一种换行，必要时补上行尾换行
func fixLineEnding(replacement, replacedSpan string) string {
	crlf := strings.Contains(replacedSpan, "\r\n")
	if crlf && !strings.Contains(replacement, "\r\n") {
		replacement = toCRLF(replacement)
	}
	if strings.HasSuffix(replacedSpan, "\n") && !strings.HasSuffix(replacement, "\n") {
		if crlf {
			return replacement + "\r\n"
		}
		return replacement + "\n"
	}
	return replacement
}

// toCRLF 把文本里的换行统一成 CRLF，兼容 Windows 风格的文件
func toCRLF(s string) string {
	lf := string([]byte{10})
	crlf := string([]byte{13, 10})
	s = strings.ReplaceAll(s, crlf, lf)
	return strings.ReplaceAll(s, lf, crlf)
}
