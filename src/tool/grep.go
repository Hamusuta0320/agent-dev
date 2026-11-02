package tool

import (
	"bufio"
	"bytes"
	"encoding/json"
	"fmt"
	"io"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"regexp"
	"strings"
	"unicode/utf8"
)

const (
	defaultGrepMaxResults = 200
	// maxGrepFileBytes 超过这个大小的文件直接跳过，真有需要请用 Bash 里的专用工具
	maxGrepFileBytes = 10 * 1024 * 1024
	// maxGrepLineBytes 单行上限，防止压缩产物那种超长行吃光内存
	maxGrepLineBytes = 1024 * 1024
	// maxGrepDisplayBytes 命中的行最多展示这么多字节，免得一行几百 KB 把结果刷爆
	maxGrepDisplayBytes = 400
	// grepBinarySniff 读文件开头这么多字节判断是不是二进制
	grepBinarySniff = 8000
)

// grepSkipDirs 递归搜索时直接跳过的目录：版本库、依赖、构建产物
var grepSkipDirs = map[string]bool{
	".git": true, ".hg": true, ".svn": true,
	"node_modules": true, "bower_components": true, "vendor": true,
	".idea": true, ".vscode": true, ".vs": true,
	"__pycache__": true, ".venv": true, "venv": true, ".tox": true,
	"dist": true, "build": true, "target": true, "bin": true, "obj": true,
	".next": true, ".nuxt": true, ".cache": true, "coverage": true,
}

type GrepTool struct {
}

func (t GrepTool) Name() string {
	return "Grep"
}

func (t GrepTool) Description() string {
	return "按正则表达式搜索文件内容，输出格式 `路径:行号:内容`。" +
		"默认在当前目录递归搜索，自动跳过 .git / node_modules / vendor / dist / build 等目录、二进制文件和超过 10MB 的文件。" +
		"path 可以指定文件或目录；glob 限定文件类型（如 *.go，多个用逗号分隔）；" +
		"case_insensitive 忽略大小写；max_results 限制返回条数（默认 200）。" +
		"pattern 是 Go 正则（RE2），不支持后向引用，搜代码里的符号要转义 ( ) [ ] { } * + ? | ^ $ . \\。"
}

func (t GrepTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"pattern": map[string]any{
				"type":        "string",
				"description": "要搜索的正则表达式（Go / RE2 语法）",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "搜索的文件或目录，默认 \".\"（当前目录）",
			},
			"glob": map[string]any{
				"type":        "string",
				"description": "只搜索匹配这些通配符的文件，如 *.go 或 *.go,*.yaml；不传则搜所有文本文件",
			},
			"case_insensitive": map[string]any{
				"type":        "boolean",
				"description": "是否忽略大小写，默认 false",
			},
			"max_results": map[string]any{
				"type":        "integer",
				"description": "最多返回多少处匹配，默认 200",
			},
		},
		"required": []string{"pattern"},
	}
}

type GrepArgs struct {
	Pattern         string `json:"pattern"`
	Path            string `json:"path"`
	Glob            string `json:"glob"`
	CaseInsensitive bool   `json:"case_insensitive"`
	MaxResults      int    `json:"max_results"`
}

// grepMatch 一处命中：哪个文件的第几行、内容是什么
type grepMatch struct {
	path string
	line int
	text string
}

func (t GrepTool) Execute(rawArgs json.RawMessage) (string, error) {
	if len(rawArgs) == 0 || string(rawArgs) == "null" {
		return "", fmt.Errorf("缺少参数: pattern")
	}

	var args GrepArgs
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return "", fmt.Errorf("参数格式错误: %w", err)
	}
	if strings.TrimSpace(args.Pattern) == "" {
		return "", fmt.Errorf("pattern 不能为空")
	}

	pattern := args.Pattern
	if args.CaseInsensitive {
		pattern = "(?i)" + pattern
	}
	re, err := regexp.Compile(pattern)
	if err != nil {
		return "", fmt.Errorf("pattern 不是合法的正则表达式: %w（搜索代码里的符号请转义，例如 fmt\\.Println）", err)
	}

	root := strings.TrimSpace(args.Path)
	if root == "" {
		root = "."
	}
	limit := args.MaxResults
	if limit <= 0 {
		limit = defaultGrepMaxResults
	}
	globs := splitGrepGlobs(args.Glob)

	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("路径不可用 %s: %w", root, err)
	}

	var matches []grepMatch
	scanned := 0 // 真正按文本读过的文件数
	skipped := 0 // 因为是二进制/超大/读不了而跳过的文件数

	if info.IsDir() {
		err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
			if walkErr != nil {
				return nil // 没权限之类的目录直接跳过，不影响整体搜索
			}
			if d.IsDir() {
				if p != root && grepSkipDirs[d.Name()] {
					return fs.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() || !matchGrepGlobs(globs, root, p) {
				return nil
			}
			// 多留一条用来判断"是不是还有更多"，方便给出截断提示
			got, reason := grepInFile(p, re, limit-len(matches)+1)
			matches = append(matches, got...)
			if reason == grepFileOK {
				scanned++
			} else {
				skipped++
			}
			if len(matches) > limit {
				return fs.SkipAll
			}
			return nil
		})
		if err != nil {
			return "", fmt.Errorf("搜索 %s 失败: %w", root, err)
		}
	} else {
		got, reason := grepInFile(root, re, limit+1)
		if reason != grepFileOK {
			return fmt.Sprintf("(没有搜索 %s：%s)", displayGrepPath(root), reason), nil
		}
		scanned = 1
		matches = append(matches, got...)
	}

	truncated := len(matches) > limit
	if truncated {
		matches = matches[:limit]
	}

	// 跳过的文件在结果里说明一下，免得"没匹配"看起来像是搜索范围不够
	skipNote := ""
	if skipped > 0 {
		skipNote = fmt.Sprintf("，跳过 %d 个二进制/超大/不可读的文件", skipped)
	}

	if len(matches) == 0 {
		if scanned == 0 {
			return fmt.Sprintf("(没有匹配：目录下没有可搜索的文本文件%s，检查一下 path 和 glob)", skipNote), nil
		}
		return fmt.Sprintf("(没有匹配：搜索了 %d 个文件%s)", scanned, skipNote), nil
	}

	files := make(map[string]struct{}, len(matches))
	for _, m := range matches {
		files[m.path] = struct{}{}
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "找到 %d 处匹配（%d 个文件，共搜索 %d 个文件%s）:\n",
		len(matches), len(files), scanned, skipNote)
	for _, m := range matches {
		text := truncateGrepLine(strings.TrimRight(m.text, "\r"), maxGrepDisplayBytes)
		fmt.Fprintf(&sb, "%s:%d:%s\n", displayGrepPath(m.path), m.line, text)
	}
	if truncated {
		fmt.Fprintf(&sb, "\n...[结果已截断：只显示前 %d 处匹配，请用更精确的 pattern、path 或 glob 缩小范围]", limit)
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

// grepFileReason 说明一个文件为什么没被按文本搜索，直接拼进提示里给模型看
type grepFileReason string

const (
	grepFileOK         grepFileReason = ""
	grepFileBinary     grepFileReason = "看起来是二进制文件，需要时请用 Bash 里的专用工具"
	grepFileTooLarge   grepFileReason = "文件太大（超过 10MB），请用 Bash 里的专用工具"
	grepFileUnreadable grepFileReason = "读取失败，可能没有权限"
)

// grepInFile 在一个文件里按行匹配，最多收集 max 条，读取/单行过长等异常都用已有结果
func grepInFile(file string, re *regexp.Regexp, max int) ([]grepMatch, grepFileReason) {
	if max <= 0 {
		return nil, grepFileOK
	}
	info, err := os.Stat(file)
	if err != nil || !info.Mode().IsRegular() {
		return nil, grepFileUnreadable
	}
	if info.Size() > maxGrepFileBytes {
		return nil, grepFileTooLarge
	}
	f, err := os.Open(file)
	if err != nil {
		return nil, grepFileUnreadable
	}
	defer f.Close()

	head := make([]byte, grepBinarySniff)
	n, _ := io.ReadFull(f, head)
	if bytes.IndexByte(head[:n], 0) >= 0 {
		return nil, grepFileBinary // 二进制文件，跳过
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, grepFileUnreadable
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxGrepLineBytes)

	var out []grepMatch
	lineNo := 0
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if !re.MatchString(line) || !utf8.ValidString(line) {
			continue
		}
		out = append(out, grepMatch{path: file, line: lineNo, text: line})
		if len(out) >= max {
			break
		}
	}
	return out, grepFileOK
}

// splitGrepGlobs 把 "*.go, *.yaml" 拆成多个通配符
func splitGrepGlobs(s string) []string {
	var out []string
	for _, p := range strings.FieldsFunc(s, func(r rune) bool {
		return r == ',' || r == ';' || r == ' ' || r == '\t'
	}) {
		if p = strings.TrimSpace(p); p != "" {
			out = append(out, p)
		}
	}
	return out
}

// matchGrepGlobs 无通配符时全都要；带路径分隔符的通配符比对相对路径，否则只比对文件名。
// 一律先把通配符和路径转成斜杠形式再用 path.Match：Windows 上 filepath.Match 的分隔符是 "\"，
// 直接用它匹配 "sub/*.go" 会把 sub/deep/d.go 也算命中（"/" 在那儿只是普通字符）。
func matchGrepGlobs(globs []string, root, file string) bool {
	if len(globs) == 0 {
		return true
	}
	base := filepath.Base(file)
	rel := ""
	if r, err := filepath.Rel(root, file); err == nil {
		rel = filepath.ToSlash(r)
	}
	for _, g := range globs {
		g = filepath.ToSlash(g)
		if ok, err := path.Match(g, base); err == nil && ok {
			return true
		}
		if rel != "" && strings.Contains(g, "/") {
			if ok, err := path.Match(g, rel); err == nil && ok {
				return true
			}
		}
	}
	return false
}

func displayGrepPath(p string) string {
	return strings.TrimPrefix(filepath.ToSlash(p), "./")
}

// truncateGrepLine 长行截断，并保证不切在多字节字符中间
func truncateGrepLine(s string, max int) string {
	if len(s) <= max {
		return s
	}
	cut := max
	for cut > 0 && !utf8.RuneStart(s[cut]) {
		cut--
	}
	return s[:cut] + " …(本行过长，已截断)"
}
