package tool

import (
	"bufio"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"os"
	"strings"
	"unicode/utf8"
)

const (
	defaultReadLines = 2000
	// maxLineBytes 单行上限，避免一行超长的文件（压缩过的 js、二进制）吃光内存
	maxLineBytes = 1024 * 1024
)

type ReadTool struct {
}

func (t ReadTool) Name() string {
	return "Read"
}

func (t ReadTool) Description() string {
	return "读取文本文件，按行返回，默认每行前面带行号（格式 `    12→代码`）。" +
		"默认从第 1 行开始、最多读 2000 行；文件更长时返回末尾会提示下一次该用的 offset。" +
		"不要用它读目录或二进制文件；搜文件内容用 Grep，新建/覆盖文件用 Write，只改一小段用 Edit。"
}

func (t ReadTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"path": map[string]any{
				"type":        "string",
				"description": "要读取的文件路径",
			},
			"offset": map[string]any{
				"type":        "integer",
				"description": "从第几行开始读（1 起），默认 1",
			},
			"lines": map[string]any{
				"type":        "integer",
				"description": "最多读取的行数，默认 2000",
			},
			"line_numbers": map[string]any{
				"type":        "boolean",
				"description": "是否显示行号，默认 true；需要原样复制内容做 Edit 时可以关掉",
			},
		},
		"required": []string{"path"},
	}
}

type ReadArgs struct {
	Path        string `json:"path"`
	Offset      int    `json:"offset"`
	Lines       int    `json:"lines"`
	LineNumbers *bool  `json:"line_numbers"`
}

func (t ReadTool) Execute(rawArgs json.RawMessage) (string, error) {
	if len(rawArgs) == 0 || string(rawArgs) == "null" {
		return "", fmt.Errorf("缺少参数: path")
	}

	var args ReadArgs
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return "", fmt.Errorf("参数格式错误: %w", err)
	}
	if strings.TrimSpace(args.Path) == "" {
		return "", fmt.Errorf("path 不能为空")
	}

	offset := args.Offset
	if offset <= 0 {
		offset = 1
	}
	limit := args.Lines
	if limit <= 0 {
		limit = defaultReadLines
	}
	withNumbers := true
	if args.LineNumbers != nil {
		withNumbers = *args.LineNumbers
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
		return "", fmt.Errorf("%s 是目录，不是文件；列目录请用 Bash（dir / ls）", args.Path)
	}

	r := bufio.NewReaderSize(f, 64*1024)
	var sb strings.Builder
	total := 0   // 已经读过的行数
	printed := 0 // 已经输出的行数
	eof := false

	for {
		line, lineTrunc, err := readLine(r, maxLineBytes)
		if err != nil && !errors.Is(err, io.EOF) {
			return "", fmt.Errorf("读取 %s 失败: %w", args.Path, err)
		}
		if errors.Is(err, io.EOF) {
			eof = true
		}
		if line == "" && eof {
			break
		}

		total++
		if total >= offset && printed < limit {
			text := strings.TrimRight(line, "\r\n")
			if !utf8.ValidString(text) {
				return "", fmt.Errorf("%s 不是文本文件（第 %d 行里有非法 UTF-8 字节）", args.Path, total)
			}
			if withNumbers {
				fmt.Fprintf(&sb, "%6d→%s\n", total, text)
			} else {
				sb.WriteString(text)
				sb.WriteString("\n")
			}
			if lineTrunc {
				sb.WriteString("      …（本行过长，后面部分已省略）\n")
			}
			printed++
		}
		if eof {
			break
		}
		if printed >= limit {
			// 已经读够了。大文件不再往下数总行数，交给调用方用 offset 继续
			break
		}
	}

	if printed == 0 {
		if total == 0 {
			return "(文件是空的)", nil
		}
		return "", fmt.Errorf("offset=%d 超出 %s 的行数（共 %d 行）", offset, args.Path, total)
	}

	result := sb.String()
	if !eof {
		result += fmt.Sprintf("\n...[已显示第 %d-%d 行，文件还没读完；继续读请用 offset=%d]", offset, total, total+1)
	}
	return result, nil
}

// readLine 读一行（包含行尾换行符）。超过 max 字节的部分只消费不保留，
// 避免单行超长时把内存撑爆。返回 io.EOF 表示已经到文件末尾。
func readLine(r *bufio.Reader, max int) (string, bool, error) {
	var sb strings.Builder
	truncated := false
	for {
		chunk, err := r.ReadSlice('\n')
		if len(chunk) > 0 {
			if remain := max - sb.Len(); remain > 0 {
				if len(chunk) > remain {
					sb.Write(chunk[:remain])
					truncated = true
				} else {
					sb.Write(chunk)
				}
			} else {
				truncated = true
			}
		}
		if errors.Is(err, bufio.ErrBufferFull) {
			continue
		}
		if err != nil {
			return sb.String(), truncated, err
		}
		return sb.String(), truncated, nil
	}
}
