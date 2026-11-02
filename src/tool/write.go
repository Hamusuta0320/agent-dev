package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"time"
	"unicode/utf8"

	"hamusuta/agent-dev/src/domain"
)

// maxWriteFileBytes 单次写入允许的最大内容大小
const maxWriteFileBytes = 10 * 1024 * 1024

// maxWriteBackupBytes 原文件超过这个大小就不备份了，免得为了一个超大文件白复制一遍
const maxWriteBackupBytes = 10 * 1024 * 1024

type WriteTool struct {
}

func (t WriteTool) Name() string {
	return "Write"
}

// Mutating 是 true：会新建或覆盖文件
func (t WriteTool) Mutating() bool { return true }

func (t WriteTool) Description() string {
	return "创建或整体覆盖一个文本文件（父目录不存在会自动创建），返回写入的字节数和行数。" +
		"intent 写一句这次要写什么，会显示在界面上。" +
		"覆盖已有文件前会先把原内容备份到 <工作目录>/.agent/backups/ 下，结果里会说明备份位置。" +
		"内容按原样写入，不做换行转换。只想改文件里的一小段请用 Edit，不要拿 Write 覆盖整个文件。"
}

func (t WriteTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"intent": intentProp(),
			"path": map[string]any{
				"type":        "string",
				"description": "要写入的文件路径",
			},
			"content": map[string]any{
				"type":        "string",
				"description": "文件的完整内容，允许为空字符串（用于创建空文件）",
			},
		},
		"required": []string{"intent", "path", "content"},
	}
}

type WriteArgs struct {
	Intent  string  `json:"intent"`
	Path    string  `json:"path"`
	Content *string `json:"content"`
}

func (t WriteTool) Execute(ctx context.Context, rawArgs json.RawMessage) (string, error) {
	if err := ctxOrBackground(ctx).Err(); err != nil {
		return "", err
	}
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
	overwrote := int64(0) // 覆盖已有文件时，原来有多少字节（0 表示没有原内容可备份）
	if info, err := os.Stat(args.Path); err == nil {
		if info.IsDir() {
			return "", fmt.Errorf("%s 是目录，不是文件", args.Path)
		}
		mode = info.Mode().Perm()
		if info.Size() > 0 {
			overwrote = info.Size()
		}
	}

	// 覆盖之前先留一份原内容：Write 是整体覆盖，手滑了就靠这个找回来
	backupNote := ""
	if overwrote > 0 {
		backup, err := backupFile(args.Path)
		if err != nil {
			backupNote = fmt.Sprintf("（原内容没能备份: %s）", err.Error())
		} else {
			backupNote = fmt.Sprintf("（原内容已备份到 %s）", displayBackupPath(backup))
		}
	}

	if dir := filepath.Dir(args.Path); dir != "" && dir != "." {
		if err := os.MkdirAll(dir, 0o755); err != nil {
			return "", fmt.Errorf("创建目录 %s 失败: %w", dir, err)
		}
	}
	if err := os.WriteFile(args.Path, content, mode); err != nil {
		return "", fmt.Errorf("写入 %s 失败: %w", args.Path, err)
	}

	lines := countLines(*args.Content)
	return fmt.Sprintf("已写入 %s，%d 字节，%d 行%s", args.Path, len(content), lines, backupNote), nil
}

// backupFile 把 path 的当前内容复制到备份目录，返回备份文件的完整路径。
// 备份放在 <工作目录>/.agent/backups/ 下（.agent 已在 .gitignore 里），
// 文件名带上时间，同一个文件改几次就留几份，不会互相覆盖。
func backupFile(path string) (string, error) {
	info, err := os.Stat(path)
	if err != nil {
		return "", err
	}
	if info.Size() > maxWriteBackupBytes {
		return "", fmt.Errorf("原文件 %s 超过 %s，跳过备份", humanBytes(info.Size()), humanBytes(maxWriteBackupBytes))
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", err
	}
	dest, err := backupPath(path)
	if err != nil {
		return "", err
	}
	if err := os.MkdirAll(filepath.Dir(dest), 0o755); err != nil {
		return "", err
	}
	if err := os.WriteFile(dest, data, 0o644); err != nil {
		return "", err
	}
	return dest, nil
}

// backupPath 算出备份该放哪儿：<工作目录>/.agent/backups/<相对路径><时间戳>.bak。
// 目标不在工作目录下时只留文件名，避免拼出 ../ 这种跑出去的路径。
func backupPath(path string) (string, error) {
	wd, err := os.Getwd()
	if err != nil {
		return "", err
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", err
	}
	rel, err := filepath.Rel(wd, abs)
	if err != nil || rel == ".." || strings.HasPrefix(rel, ".."+string(filepath.Separator)) {
		rel = filepath.Base(abs)
	}
	stamp := time.Now().Format("20060102-150405.000")
	return filepath.Join(wd, ".agent", "backups", rel+stamp+".bak"), nil
}

// displayBackupPath 把备份路径砍成相对工作目录的样子，结果里读起来短一些
func displayBackupPath(p string) string {
	wd, err := os.Getwd()
	if err != nil {
		return displayPath(p)
	}
	if rel, err := filepath.Rel(wd, p); err == nil {
		return displayPath(rel)
	}
	return displayPath(p)
}

// Preview 是"编辑需授权"模式下给用户看的一句话摘要：改哪个文件、新建还是覆盖、多少行
func (t WriteTool) Preview(rawArgs json.RawMessage) domain.ApprovalRequest {
	req := domain.ApprovalRequest{Kind: domain.ApprovalWriteFile, Target: "（参数不完整）"}
	var args WriteArgs
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return req
	}
	if path := strings.TrimSpace(args.Path); path != "" {
		req.Target = path
	}
	lines, size := 0, 0
	if args.Content != nil {
		lines, size = countLines(*args.Content), len(*args.Content)
	}
	if info, err := os.Stat(req.Target); err == nil && info.Mode().IsRegular() {
		req.Detail = withIntent(args.Intent,
			fmt.Sprintf("覆盖已有文件（原来 %s），写入 %d 行 / %d 字节", humanBytes(info.Size()), lines, size))
	} else {
		req.Detail = withIntent(args.Intent, fmt.Sprintf("新建文件，写入 %d 行 / %d 字节", lines, size))
	}
	if args.Content != nil {
		req.Diff = newDiffPreview("", *args.Content)
	}
	return req
}

// countLines 数一段文本有多少行，末尾没有换行也算一行
func countLines(s string) int {
	if s == "" {
		return 0
	}
	n := strings.Count(s, "\n")
	if !strings.HasSuffix(s, "\n") {
		n++
	}
	return n
}

// humanBytes 把字节数压成给人看的样子，只用在授权摘要里
func humanBytes(n int64) string {
	switch {
	case n < 1024:
		return fmt.Sprintf("%d 字节", n)
	case n < 1024*1024:
		return fmt.Sprintf("%.1fKB", float64(n)/1024)
	default:
		return fmt.Sprintf("%.1fMB", float64(n)/(1024*1024))
	}
}
