package tool

import (
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"
)

// maxWriteFileBytes 单次写入允许的最大内容大小
const maxWriteFileBytes = 10 * 1024 * 1024

type WriteTool struct {
}

func (t WriteTool) Name() string {
	return "Write"
}

func (t WriteTool) Description() string {
	return "创建或整体覆盖一个文本文件（父目录不存在会自动创建），返回写入的字节数和行数。" +
		"内容按原样写入，不做换行转换。只想改文件里的一小段请用 Edit，不要拿 Write 覆盖整个文件。"
}

func (t WriteTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "要写入的文件路径",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "文件的完整内容，允许为空字符串（用于创建空文件）",
			},
		},
		"required": []string{"path", "content"},
	}
}

type WriteArgs struct {
	Path    string  `json:"path"`
	Content *string `json:"content"`
}

func (t WriteTool) Execute(rawArgs json.RawMessage) (string, error) {
	if len(rawArgs) == 0 || string(rawArgs) == "null" {
		return "", fmt.Errorf("缺少参数: path / content")
	}

	var args WriteArgs
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return "", fmt.Errorf("参数格式错误: %w", err)
	}
	if strings.TrimSpace(args.Path) == "" {
		return "", fmt.Errorf("path 不能为空")
	}
	// content 用指针区分"没传"和"传了空字符串"
	if args.Content == nil {
		return "", fmt.Errorf("缺少参数: content（写空文件请传空字符串）")
	}

	content := []byte(*args.Content)
	if !utf8.Valid(content) {
		return "", fmt.Errorf("content 不是合法的 UTF-8 文本")
	}
	if len(content) > maxWriteFileBytes {
		return "", fmt.Errorf("内容太大（%d 字节），超过 %d 字节上限", len(content), maxWriteFileBytes)
	}

	mode := os.FileMode(0o644)
	if info, err := os.Stat(args.Path); err == nil {
		if info.IsDir() {
			return "", fmt.Errorf("%s 是目录，不是文件", args.Path)
		}
		mode = info.Mode().Perm()
	}

	if dir := filepath.Dir(args.Path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("创建目录 %s 失败: %w", dir, err)
		}
	}
	if err := os.WriteFile(args.Path, content, mode); err != nil {
		return "", fmt.Errorf("写入 %s 失败: %w", args.Path, err)
	}

	lines := 0
	if len(content) > 0 {
		lines = strings.Count(*args.Content, "\n")
		if !strings.HasSuffix(*args.Content, "\n") {
			lines++
		}
	}
	return fmt.Sprintf("已写入 %s，%d 字节，%d 行", args.Path, len(content), lines), nil
}
