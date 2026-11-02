package tool

import (
	"encoding/json"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

type ReadTool struct {
}

func (t ReadTool) Name() string {
	return "Read"
}

func (t ReadTool) Description() string {
	return "用于读取文件内容"
}

func (t ReadTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "要读取的文件路径",
			},
			"lines": map[string]any{
				"type":        "integer",
				"description": "最多读取的行数，不传则读全部",
			},
		},
		"required": []string{"path"},
	}
}

type ReadArgs struct {
	Path  string `json:"path"`
	Lines int32  `json:"lines"`
}

func (t ReadTool) Execute(rawArgs json.RawMessage) (string, error) {
	if len(rawArgs) == 0 || string(rawArgs) == "null" {
		return "", fmt.Errorf("缺少参数: path")
	}

	var args ReadArgs
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return "", fmt.Errorf("参数格式错误: %w", err)
	}

	if args.Path == "" {
		return "", fmt.Errorf("path 不能为空")
	}

	f, err := os.Open(args.Path)
	if err != nil {
		return "", fmt.Errorf("读取 %s 失败: %w", args.Path, err)
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil {
		return "", fmt.Errorf("读取 %s 失败: %w", args.Path, err)
	}
	if info.IsDir() {
		return "", fmt.Errorf("%s 是目录，不是文件", args.Path)
	}

	const maxBytes = 100 * 1024
	data, err := io.ReadAll(io.LimitReader(f, maxBytes+1))
	if err != nil {
		return "", fmt.Errorf("读取 %s 失败: %w", args.Path, err)
	}

	truncated := len(data) > maxBytes
	if truncated {
		data = data[:maxBytes]
		// 按字节截断可能切到多字节字符中间，退到完整字符边界
		for len(data) > 0 && !utf8.Valid(data) {
			data = data[:len(data)-1]
		}
	}

	if !utf8.Valid(data) {
		return "", fmt.Errorf("%s 不是文本文件", args.Path)
	}

	text := string(data)

	if args.Lines > 0 {
		lines := strings.Split(text, "\n")
		if int(args.Lines) < len(lines) {
			lines = lines[:args.Lines]
			truncated = true
		}
		text = strings.Join(lines, "\n")
	}

	if truncated {
		text += fmt.Sprintf("\n...[内容已截断，原文件 %d 字节，仅显示前 %d 字节]", info.Size(), maxBytes)
	}
	return text, nil
}
