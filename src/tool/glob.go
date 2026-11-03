package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"io/fs"
	"os"
	"path"
	"path/filepath"
	"sort"
	"strings"
)

const (
	defaultGlobMaxResults = 200
	// maxGlobPatternLen 限制 pattern 长度：逐段递归匹配是指数级的，
	// 离谱的长串（模型手滑粘进来的整段文本）直接拦住
	maxGlobPatternLen = 256
	// globRecursiveSeg 是"递归任意层"的那一段
	globRecursiveSeg = "**"
)

// GlobTool 按通配符列文件和目录。它是"列目录"的主力：
// pattern 只写一层名字时就等同 ls，写 ** 才往下递归。
type GlobTool struct{}

func (t GlobTool) Name() string {
	return "Glob"
}

// Mutating 是 false：Glob 只列不改
func (t GlobTool) Mutating() bool { return false }

func (t GlobTool) Description() string {
	return "按通配符列出文件或目录，输出相对起点的路径（目录以 / 结尾），按路径排序。" +
		"intent 写一句这次要找什么，会显示在界面上。" +
		"pattern 里不含 / 时只列起点目录这一层，pattern 写 * 就是列目录；" +
		"要递归就用 **，例如 **/*.go 找所有 Go 文件、**/*_test.go 找测试、src/**/*.go 限定在 src 下。" +
		"支持 * ? [abc] 这几种通配符，** 表示任意层目录。" +
		"默认跳过 .git / node_modules / vendor / dist / build 等目录的**内容**（目录自己还是会被列出来）。" +
		"列目录请用它，不要再用 Bash 的 ls / dir / find。"
}

func (t GlobTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"intent": intentProp(),
			"pattern": map[string]any{
				"type":        "string",
				"description": "通配符，默认 *（只列起点目录这一层）；** 表示递归任意层，如 **/*.go。不含 / 时只匹配名字，不做递归",
			},
			"path": map[string]any{
				"type":        "string",
				"description": "起点的目录，默认 \".\"（当前目录）",
			},
			"max_results": map[string]any{
				"type":        "integer",
				"description": "最多返回多少项，默认 200",
			},
		},
		"required": []string{"intent"},
	}
}

type GlobArgs struct {
	Intent     string `json:"intent"`
	Pattern    string `json:"pattern"`
	Path       string `json:"path"`
	MaxResults int    `json:"max_results"`
}

// globEntry 一项命中：相对起点的路径（斜杠形式，不带 ./）和它是不是目录
type globEntry struct {
	rel string
	dir bool
}

func (t GlobTool) Execute(ctx context.Context, rawArgs json.RawMessage) (string, error) {
	cctx := ctxOrBackground(ctx)
	if err := cctx.Err(); err != nil {
		return "", err
	}

	// pattern 有默认值，所以整段参数都可以不传
	var args GlobArgs
	if len(rawArgs) > 0 && string(rawArgs) != "null" {
		if err := json.Unmarshal(rawArgs, &args); err != nil {
			return "", fmt.Errorf("参数格式错误: %w", err)
		}
	}

	pattern, segs, deep, err := parseGlobPattern(args.Pattern)
	if err != nil {
		return "", err
	}

	root := strings.TrimSpace(args.Path)
	if root == "" {
		root = "."
	}
	limit := args.MaxResults
	if limit <= 0 {
		limit = defaultGlobMaxResults
	}

	info, err := os.Stat(root)
	if err != nil {
		return "", fmt.Errorf("路径不可用 %s: %w", root, err)
	}
	if !info.IsDir() {
		return "", fmt.Errorf("%s 不是目录；要看单个文件的内容请用 Read", root)
	}

	// 多收一条用来判断"是不是还有更多"，方便给出截断提示
	maxCollect := limit + 1
	var entries []globEntry
	// record 收一项，返回 false 表示已经够了，可以停下
	record := func(rel string, dir bool) bool {
		entries = append(entries, globEntry{rel: rel, dir: dir})
		return len(entries) < maxCollect
	}

	if deep {
		err = filepath.WalkDir(root, func(p string, d fs.DirEntry, walkErr error) error {
			// 用户中断了就赶紧收工：返回 ctx 的错误，WalkDir 会立刻停下并原样带出来
			if err := cctx.Err(); err != nil {
				return err
			}
			if walkErr != nil {
				return nil // 没权限之类的目录直接跳过，不影响整体
			}
			if p == root {
				return nil
			}
			rel, relErr := filepath.Rel(root, p)
			if relErr != nil {
				return nil
			}
			rel = filepath.ToSlash(rel)
			if globMatchSegments(segs, strings.Split(rel, "/")) {
				if !record(rel, d.IsDir()) {
					return fs.SkipAll
				}
			}
			// 依赖、构建产物、版本库这些目录列出来就够了，别往里钻
			if d.IsDir() && skipDirs[d.Name()] {
				return fs.SkipDir
			}
			return nil
		})
		if err != nil {
			if cerr := cctx.Err(); cerr != nil {
				return "", cerr
			}
			return "", fmt.Errorf("遍历 %s 失败: %w", root, err)
		}
	} else {
		// 不递归：就是列一层目录，ReadDir 本身按文件名排好序
		children, readErr := os.ReadDir(root)
		if readErr != nil {
			return "", fmt.Errorf("列目录 %s 失败: %w", root, readErr)
		}
		for _, child := range children {
			if err := cctx.Err(); err != nil {
				return "", err
			}
			ok, matchErr := path.Match(segs[0], child.Name())
			if matchErr != nil {
				return "", fmt.Errorf("pattern %q 不是合法的通配符: %w", pattern, matchErr)
			}
			if ok && !record(child.Name(), child.IsDir()) {
				break
			}
		}
	}

	truncated := len(entries) > limit
	if truncated {
		entries = entries[:limit]
	}
	sort.Slice(entries, func(i, j int) bool { return entries[i].rel < entries[j].rel })

	if len(entries) == 0 {
		note := ""
		if !deep {
			note = "；列目录用 pattern=*，递归用 **/*"
		}
		return fmt.Sprintf("(没有匹配：%s 下没有符合 %s 的条目%s)", displayPath(root), pattern, note), nil
	}

	files, dirs := 0, 0
	for _, e := range entries {
		if e.dir {
			dirs++
			continue
		}
		files++
	}

	var sb strings.Builder
	fmt.Fprintf(&sb, "找到 %d 项（%d 个文件，%d 个目录），路径相对 %s:\n",
		len(entries), files, dirs, displayPath(root))
	for _, e := range entries {
		if e.dir {
			sb.WriteString(e.rel + "/\n")
			continue
		}
		sb.WriteString(e.rel + "\n")
	}
	if truncated {
		fmt.Fprintf(&sb, "\n...[结果已截断：只显示前 %d 项，请用更精确的 pattern 或缩小 path]", limit)
	}
	return strings.TrimRight(sb.String(), "\n"), nil
}

// parseGlobPattern 把用户写的通配符整理成可以逐段匹配的形式：
// 统一成斜杠、去掉开头的 ./ 和结尾的 /。deep 表示要不要往下递归。
func parseGlobPattern(raw string) (pattern string, segs []string, deep bool, err error) {
	pattern = strings.TrimSpace(raw)
	if pattern == "" {
		pattern = "*"
	}
	if len(pattern) > maxGlobPatternLen {
		return "", nil, false, fmt.Errorf("pattern 太长（%d 字符），请用更精确的通配符", len(pattern))
	}
	pattern = filepath.ToSlash(pattern)
	// pattern 只描述起点以下的路径，带前缀（/tmp/*.go、D:/x/*.go）只会让匹配永远落空
	if strings.HasPrefix(pattern, "/") || (len(pattern) > 1 && pattern[1] == ':') {
		return "", nil, false, fmt.Errorf("pattern 里不要带目录前缀 %q：要限定范围请用 path 参数", raw)
	}
	pattern = strings.TrimPrefix(pattern, "./")
	pattern = strings.Trim(pattern, "/") // 写成 "src/" 也是想列 src 这一层
	if pattern == "" {
		pattern = "*"
	}

	segs = strings.Split(pattern, "/")
	for _, seg := range segs {
		if seg == "" {
			return "", nil, false, fmt.Errorf("pattern %q 里有空的路径段，检查一下多余的 /", raw)
		}
		if seg == globRecursiveSeg {
			continue
		}
		// 这里拿一个占位名字试一下语法，非法字符（比如未闭合的 [）当场报出来
		if _, matchErr := path.Match(seg, "x"); matchErr != nil {
			return "", nil, false, fmt.Errorf("pattern %q 不是合法的通配符: %w（支持 * ? [abc]，** 表示任意层目录）", raw, matchErr)
		}
	}
	// 只有 "**" 这一个也当递归：它本来就该匹配所有层级
	deep = len(segs) > 1 || segs[0] == globRecursiveSeg
	return pattern, segs, deep, nil
}

// globMatchSegments 按 / 分段匹配通配符，** 匹配零到任意多段。
// 不用 filepath.Match：它不支持 **，而且在 Windows 上把 \ 当分隔符。
func globMatchSegments(pat, name []string) bool {
	// 只有一层 ** 时朴素递归最多线性回溯；有两层以上才会指数爆炸
	//（**/**/**/x 这种），那时才换上记忆化
	if countRecursiveSegs(pat) < 2 {
		return globMatchSimple(pat, name)
	}
	return globMatchMemo(pat, name)
}

// countRecursiveSegs 数 pattern 里有几段 **
func countRecursiveSegs(pat []string) int {
	n := 0
	for _, seg := range pat {
		if seg == globRecursiveSeg {
			n++
		}
	}
	return n
}

// globMatchSimple 是一次到位的递归匹配
func globMatchSimple(pat, name []string) bool {
	if len(pat) == 0 {
		return len(name) == 0
	}
	if pat[0] == globRecursiveSeg {
		for i := 0; i <= len(name); i++ {
			if globMatchSimple(pat[1:], name[i:]) {
				return true
			}
		}
		return false
	}
	if len(name) == 0 {
		return false
	}
	ok, err := path.Match(pat[0], name[0])
	if err != nil || !ok {
		return false
	}
	return globMatchSimple(pat[1:], name[1:])
}

// globMatchMemo 是同一个匹配加上记忆化：** 的分支会重复走到同样的
// （pattern 段下标, 名字段下标）组合，算过一遍就不再算
func globMatchMemo(pat, name []string) bool {
	type state struct{ pat, name int }
	memo := make(map[state]bool, (len(pat)+1)*(len(name)+1))

	var match func(p, n int) bool
	match = func(p, n int) bool {
		k := state{pat: p, name: n}
		if v, ok := memo[k]; ok {
			return v
		}
		result := false
		switch {
		case p == len(pat):
			result = n == len(name)
		case pat[p] == globRecursiveSeg:
			for i := n; i <= len(name); i++ {
				if match(p+1, i) {
					result = true
					break
				}
			}
		case n < len(name):
			if ok, err := path.Match(pat[p], name[n]); err == nil && ok {
				result = match(p+1, n+1)
			}
		}
		memo[k] = result
		return result
	}
	return match(0, 0)
}
