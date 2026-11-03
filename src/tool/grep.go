package tool

import (
	"bufio"
	"bytes"
	"context"
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
	// maxGrepContextLines 上下文行最多这么多，再多输出就刷屏了
	maxGrepContextLines = 10
)

// skipDirs 遍历时直接跳过的目录：版本库、依赖、构建产物。
// Grep 和 Glob 共用这一份，两边的"会搜哪些地方"保持一致。
var skipDirs = map[string]bool{
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

// Mutating 是 false：Grep 只搜不改
func (t GrepTool) Mutating() bool { return false }

func (t GrepTool) Description() string {
	return "按正则表达式搜索文件内容，输出格式 `路径:行号:内容`。" +
		"intent 写一句这次要找什么，会显示在界面上。" +
		"默认在当前目录递归搜索，自动跳过 .git / node_modules / vendor / dist / build 等目录、二进制文件和超过 10MB 的文件。" +
		"不是 UTF-8 的文件（例如 GBK / GB18030 编码）会先按本地编码解码再匹配，pattern 请照常写 UTF-8。" +
		"path 可以指定文件或目录；glob 限定文件类型（如 *.go，多个用逗号分隔）；" +
		"case_insensitive 忽略大小写；max_results 限制返回条数（默认 200）；" +
		"context_lines 让每处匹配多显示前后各几行上下文（默认 0，最多 10），上下文行用 `-` 分隔，" +
		"需要看清命中周围在干什么时再传。" +
		"pattern 是 Go 正则（RE2），不支持后向引用，搜代码里的符号要转义 ( ) [ ] { } * + ? | ^ $ . \\。"
}

func (t GrepTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"intent": intentProp(),
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
			"context_lines": map[string]any{
				"type":        "integer",
				"description": "每处匹配前后各显示多少行上下文，默认 0（只显示命中行），最多 10",
			},
		},
		"required": []string{"intent", "pattern"},
	}
}

type GrepArgs struct {
	Intent          string `json:"intent"`
	Pattern         string `json:"pattern"`
	Path            string `json:"path"`
	Glob            string `json:"glob"`
	CaseInsensitive bool   `json:"case_insensitive"`
	MaxResults      int    `json:"max_results"`
	ContextLines    int    `json:"context_lines"`
}

// grepMatch 输出的一行：哪个文件的第几行、内容是什么。
// context 为 true 表示这是命中行周围的上下文，不是命中本身。
type grepMatch struct {
	path    string
	line    int
	text    string
	context bool
}

func (t GrepTool) Execute(ctx context.Context, rawArgs json.RawMessage) (string, error) {
	cctx := ctxOrBackground(ctx)
	if err := cctx.Err(); err != nil {
		return "", err
	}
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

	// 上下文行太多会把结果刷爆，超出范围就夹紧，不报错
	ctxLines := args.ContextLines
	if ctxLines < 0 {
		ctxLines = 0
	}
	if ctxLines > maxGrepContextLines {
		ctxLines = maxGrepContextLines
	}

	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("路径不可用 %s: %w", root, err)
	}

	var hits []grepMatch
	matched := 0 // 命中行数，上下文行不算
	scanned := 0 // 真正按文本读过的文件数
	skipped := 0 // 因为是二进制/超大/读不了而跳过的文件数
	decoded := 0 // 其中不是 UTF-8、按本地编码（GBK）解码过的文件数

	countHits := func(got []grepMatch) {
		for _, m := range got {
			if !m.context {
				matched++
			}
		}
		hits = append(hits, got...)
	}

	if info.IsDir() {
		err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
			// 用户中断了就赶紧收工：返回 ctx 的错误，WalkDir 会立刻停下并原样带出来
			if err := cctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				return nil // 没权限之类的目录直接跳过，不影响整体搜索
			}
			if d.IsDir() {
				if p != root && skipDirs[d.Name()] {
					return fs.SkipDir
				}
				return nil
			}
			if !d.Type().IsRegular() || !matchGrepGlobs(globs, root, p) {
				return nil
			}
			// 多留一条用来判断"是不是还有更多"，方便给出截断提示
			got, reason, wasDecoded := grepInFile(p, re, limit-matched+1, ctxLines)
			countHits(got)
			if reason == grepFileOK {
				scanned++
				if wasDecoded {
					decoded++
				}
			} else {
				skipped++
			}
			if matched > limit {
				return fs.SkipAll
			}
			return nil
		})
		if err != nil {
			return "", fmt.Errorf("搜索 %s 失败: %w", root, err)
		}
	} else {
		got, reason, wasDecoded := grepInFile(root, re, limit+1, ctxLines)
		if reason != grepFileOK {
			return fmt.Sprintf("(没有搜索 %s：%s)", displayPath(root), reason), nil
		}
		scanned = 1
		if wasDecoded {
			decoded = 1
		}
		countHits(got)
	}

	// 最多只留前 limit 处命中：最后一处命中后面跟着的上下文行留着，再往后的（含下一处命中）都不要
	truncated := matched > limit
	shown := hits
	if truncated {
		n, cut := 0, len(hits)
		for i, m := range hits {
			if m.context {
				continue
			}
			n++
			if n == limit {
				cut = i + 1
				for cut < len(hits) && hits[cut].context {
					cut++
				}
				break
			}
		}
		shown = hits[:cut]
	}

	// 跳过的文件在结果里说明一下，免得"没匹配"看起来像是搜索范围不够
	skipNote := ""
	if skipped > 0 {
		skipNote = fmt.Sprintf("，跳过 %d 个二进制/超大/不可读的文件", skipped)
	}
	if decoded > 0 {
		skipNote += fmt.Sprintf("，%d 个文件不是 UTF-8（已按 GB18030 解码）", decoded)
	}

	if len(hits) == 0 {
		if scanned == 0 {
			return fmt.Sprintf("(没有匹配：目录下没有可搜索的文本文件%s，检查一下 path 和 glob)", skipNote), nil
		}
		return fmt.Sprintf("(没有匹配：搜索了 %d 个文件%s)", scanned, skipNote), nil
	}

	files := make(map[string]struct{}, len(shown))
	for _, m := range shown {
		files[m.path] = struct{}{}
	}

	shownMatched := matched
	if truncated {
		shownMatched = limit
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "找到 %d 处匹配（%d 个文件，共搜索 %d 个文件%s）:\n",
		shownMatched, len(files), scanned, skipNote)
	for _, m := range shown {
		text := truncateGrepLine(strings.TrimRight(m.text, "\r"), maxGrepDisplayBytes)
		// 上下文行用 "-" 分隔，和命中行的 "路径:行号:内容" 区分开
		sep := ":"
		if m.context {
			sep = "-"
		}
		fmt.Fprintf(&sb, "%s%s%d%s%s\n", displayPath(m.path), sep, m.line, sep, text)
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

// grepInFile 在一个文件里按行匹配，最多收集 max 处命中；ctxLines > 0 时连命中前后的上下文行一起收集。
// 读取/单行过长等异常都用已有结果。
// 第三个返回值表示这个文件不是 UTF-8、按本地编码（GBK）解码过。
func grepInFile(file string, re *regexp.Regexp, max, ctxLines int) ([]grepMatch, grepFileReason, bool) {
	if max <= 0 {
		return nil, grepFileOK, false
	}
	// 先开文件再 f.Stat()：WalkDir 给的 DirEntry 在 Windows 上不缓存文件信息，
	// 之前那次 os.Stat 只是为了拿大小，白多一次系统调用；大小从已打开的文件句柄问就行
	f, err := os.Open(file)
	if err != nil {
		return nil, grepFileUnreadable, false
	}
	defer f.Close()

	info, err := f.Stat()
	if err != nil || !info.Mode().IsRegular() {
		return nil, grepFileUnreadable, false
	}
	if info.Size() > maxGrepFileBytes {
		return nil, grepFileTooLarge, false
	}

	head := make([]byte, grepBinarySniff)
	n, _ := io.ReadFull(f, head)
	if bytes.IndexByte(head[:n], 0) >= 0 {
		return nil, grepFileBinary, false // 二进制文件，跳过
	}
	if _, err := f.Seek(0, io.SeekStart); err != nil {
		return nil, grepFileUnreadable, false
	}

	scanner := bufio.NewScanner(f)
	scanner.Buffer(make([]byte, 0, 64*1024), maxGrepLineBytes)

	var out []grepMatch
	decoded := false
	lineNo := 0
	matched := 0           // 本文件已经命中的行数
	shown := 0             // 已经输出到的最大行号，命中挨得近时用来去重
	var before []grepMatch // 最近的 ctxLines 行，留给下一处命中当前文
	after := 0             // 当前命中后面还要补几行上下文
	for scanner.Scan() {
		lineNo++
		line := scanner.Text()
		if !utf8.ValidString(line) {
			// GBK 之类的本地编码：pattern 是 UTF-8，得先解码再匹配
			d, ok := decodeLocalLossless([]byte(line))
			if !ok {
				continue // 解不动的行多半是二进制残留，跳过
			}
			line, decoded = d, true
		}
		if re.MatchString(line) {
			for _, b := range before {
				if b.line > shown {
					out = append(out, b)
					shown = b.line
				}
			}
			before = before[:0]
			if lineNo > shown {
				out = append(out, grepMatch{path: file, line: lineNo, text: line})
				shown = lineNo
			}
			matched++
			after = ctxLines
			if matched >= max {
				break
			}
			continue
		}
		if after > 0 {
			after--
			if lineNo > shown {
				out = append(out, grepMatch{path: file, line: lineNo, text: line, context: true})
				shown = lineNo
			}
		}
		if ctxLines > 0 {
			before = append(before, grepMatch{path: file, line: lineNo, text: line, context: true})
			if len(before) > ctxLines {
				before = before[1:]
			}
		}
	}
	return out, grepFileOK, decoded
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

// displayPath 把路径整理成给人看的样子：斜杠形式，去掉开头的 ./
func displayPath(p string) string {
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
