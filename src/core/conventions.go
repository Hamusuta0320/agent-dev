package core

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"unicode/utf8"

	"hamusuta/agent-dev/src/tool"
)

// conventionFiles 是项目里认的约定文件名，按优先级从高到低：
// AGENTS.md 是各家 agent 通用的写法，CLAUDE.md 是 Claude Code 的写法。
// 同一层里只取第一个存在的那个——两个都是同一份约定抄两遍的话，
// 都读进来又占上下文又容易自相矛盾，AGENTS.md 有就够用了
var conventionFiles = []string{"AGENTS.md", "CLAUDE.md"}

// homeConventions 是用户目录下的全局约定文件（相对用户目录），同样按优先级只取一个。
// .agents/AGENTS.md 是这个工具自己的全局约定，技能也放在同一个 .agents 下；
// 没写的话才退回 Claude Code 的 .claude/CLAUDE.md，装了它的人不用为了换到这儿再抄一份。
// 跨项目都成立的习惯（"回答用中文""提交信息这么写"）写在这里，项目特有的写项目里
var homeConventions = []string{
	filepath.Join(".agents", "AGENTS.md"),
	filepath.Join(".claude", "CLAUDE.md"),
}

// maxConventionBytes 是单个约定文件塞进提示词的上限。约定文件偶尔写得很长，
// 整篇塞进每一轮请求白占上下文，超了就截断并说明还剩多少
const maxConventionBytes = 32 << 10

// convention 是一份读到的约定文件
type convention struct {
	path string
	text string
	home bool // 用户目录下的全局约定，展示时标一下，好分辨是哪一层的规矩
}

// conventionSection 把约定文件拼成系统提示词里的一段。
// 顺序是"从全局到具体"：用户目录下的全局约定在最前，然后是工作目录每一层向上的
// 约定文件（仓库根的在前，越贴近当前目录的越靠后）。每一处只取一份。
// 一个文件都没有时返回空串，提示词里就不出现这一段。
func conventionSection(workDir string) string {
	var docs []convention
	if home, ok := homeConvention(); ok {
		docs = append(docs, convention{path: home.path, text: home.text, home: true})
	}
	for _, dir := range ancestorDirs(workDir) {
		c, ok := pickConvention(dir, conventionFiles)
		if !ok {
			continue
		}
		docs = append(docs, c)
	}
	if len(docs) == 0 {
		return ""
	}

	var sb strings.Builder
	sb.WriteString("\n## 项目约定\n下面这些是约定文件，做事时照着来")
	sb.WriteString("（和用户这一轮说的冲突时，以用户为准；和你的默认习惯冲突时，以这里为准）。")
	sb.WriteString("带（全局）的是用户目录下、所有项目通用的规矩，越往后越贴近当前目录、越具体：\n")
	for _, d := range docs {
		label := d.path
		if d.home {
			label += "（全局）"
		}
		fmt.Fprintf(&sb, "\n### %s\n%s\n", label, d.text)
	}
	return sb.String()
}

// homeConvention 找用户目录下的全局约定，按 homeConventions 的顺序取第一个能用的。
// 取不到用户目录、或者一个都没写时 ok 为 false
func homeConvention() (convention, bool) {
	home, err := os.UserHomeDir()
	if err != nil || strings.TrimSpace(home) == "" {
		return convention{}, false
	}
	for _, rel := range homeConventions {
		path := filepath.Join(home, rel)
		if text, ok := readConvention(path); ok {
			return convention{path: path, text: text}, true
		}
	}
	return convention{}, false
}

// pickConvention 在某一层目录里按 names 的顺序找第一个存在的约定文件
func pickConvention(dir string, names []string) (convention, bool) {
	for _, name := range names {
		path := filepath.Join(dir, name)
		if text, ok := readConvention(path); ok {
			return convention{path: path, text: text}, true
		}
	}
	return convention{}, false
}

// readConvention 读一个约定文件，返回去掉首尾空白的正文。
// 文件不存在、读不动、不是文本、内容为空都算"没有"，不拦启动也不报错：
// 提示词里的东西读坏了不该让整个对话用不了
func readConvention(path string) (string, bool) {
	info, err := os.Stat(path)
	if err != nil || info.IsDir() {
		return "", false
	}
	text, err := tool.ReadText(path)
	if err != nil {
		return "", false
	}
	if info.Size() > maxConventionBytes {
		// 按字节截断后可能切在多字节字符中间，裁到能停的地方再补一句说明
		text = trimToBytes(text, maxConventionBytes)
		text += fmt.Sprintf("\n…（这个文件 %s，太长只读了前面 %s，后面的要用 Read 自己看）",
			humanBytes(info.Size()), humanBytes(maxConventionBytes))
	}
	text = strings.TrimSpace(text)
	if text == "" {
		return "", false
	}
	return text, true
}

// ancestorDirs 返回从根目录到 workDir 的每一层目录（含 workDir 自己），
// 这样约定文件按"从宽到具体"的顺序读进来。workDir 为空或解析不了时返回 nil
func ancestorDirs(workDir string) []string {
	workDir = strings.TrimSpace(workDir)
	if workDir == "" {
		return nil
	}
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return nil
	}
	var dirs []string
	for dir := filepath.Clean(abs); ; dir = filepath.Dir(dir) {
		dirs = append(dirs, dir)
		parent := filepath.Dir(dir)
		if parent == dir { // 到盘符或 / 了
			break
		}
	}
	// 反转成从根开始
	for i, j := 0, len(dirs)-1; i < j; i, j = i+1, j-1 {
		dirs[i], dirs[j] = dirs[j], dirs[i]
	}
	return dirs
}

// trimToBytes 按字节上限截断字符串，保证不把一个字符切两半
func trimToBytes(s string, limit int) string {
	if len(s) <= limit {
		return s
	}
	s = s[:limit]
	for len(s) > 0 {
		r, size := utf8.DecodeLastRuneInString(s)
		if r != utf8.RuneError || size > 1 {
			break
		}
		s = s[:len(s)-1]
	}
	return s
}

// humanBytes 把字节数说成人话，用在提示词里的说明和界面上
func humanBytes(n int64) string {
	switch {
	case n >= 1<<20:
		return fmt.Sprintf("%.1f MB", float64(n)/(1<<20))
	case n >= 1<<10:
		return fmt.Sprintf("%.0f KB", float64(n)/(1<<10))
	}
	return fmt.Sprintf("%d 字节", n)
}
