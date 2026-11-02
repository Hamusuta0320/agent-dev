package tool

import (
	"encoding/json"
	"errors"
	"fmt"
	"io/fs"
	"os"
	"strings"
	"unicode/utf8"
)

// maxEditFileBytes 单次编辑允许的最大文件大小，避免误改大文件
const maxEditFileBytes = 10 * 1024 * 1024

type EditTool struct {
}

func (t EditTool) Name() string {
	return "Edit"
}

func (t EditTool) Description() string {
	return "把文件里的一段原文替换成新内容，返回替换的处数。old_string 必须与文件内容逐字符一致（缩进、空格、换行都要一样），默认还要求它在文件中唯一；不唯一时请多带几行上下文，或设置 replace_all=true。改之前建议先用 Read 看一眼原文。"
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
	if args.OldString == "" {
		return "", fmt.Errorf("old_string 不能为空")
	}
	if args.OldString == args.NewString {
		return "", fmt.Errorf("old_string 与 new_string 相同，无需修改")
	}

	info, err := os.Stat(args.Path)
	if err != nil {
		if errors.Is(err, fs.ErrNotExist) {
			return "", fmt.Errorf("文件不存在: %s", args.Path)
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
	oldStr, newStr := args.OldString, args.NewString

	count := strings.Count(text, oldStr)
	if count == 0 {
		// 文件用 CRLF 而传入的是 LF 时匹配不上，这里按 CRLF 再试一次
		crlf := string([]byte{13, 10})
		if !strings.Contains(oldStr, crlf) {
			if strings.Contains(text, crlf) {
				if n := strings.Count(text, toCRLF(oldStr)); n != 0 {
					oldStr, newStr = toCRLF(oldStr), toCRLF(newStr)
					count = n
				}
			}
		}
	}

	if count == 0 {
		return "", fmt.Errorf("在 %s 中没有找到 old_string，请先用 Read 确认原文（缩进、空格、换行需完全一致）", args.Path)
	}
	if !args.ReplaceAll {
		if count != 1 {
			return "", fmt.Errorf("old_string 在 %s 中出现 %d 次，不唯一；请多带几行上下文，或设置 replace_all=true", args.Path, count)
		}
	}

	limit := 1
	if args.ReplaceAll {
		limit = -1
	}
	replaced := 1
	if args.ReplaceAll {
		replaced = count
	}

	updated := strings.Replace(text, oldStr, newStr, limit)
	if err := os.WriteFile(args.Path, []byte(updated), info.Mode().Perm()); err != nil {
		return "", fmt.Errorf("写入 %s 失败: %w", args.Path, err)
	}
	return fmt.Sprintf("已修改 %s，替换 %d 处，文件现在 %d 字节", args.Path, replaced, len(updated)), nil
}

// toCRLF 把文本里的换行统一成 CRLF，兼容 Windows 风格的文件
func toCRLF(s string) string {
	lf := string([]byte{10})
	crlf := string([]byte{13, 10})
	s = strings.ReplaceAll(s, crlf, lf)
	return strings.ReplaceAll(s, lf, crlf)
}
