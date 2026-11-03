package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 约定文件从工作目录一路向上读：父目录（仓库根）的写在前面，越近的越靠后
func TestConventionSectionReadsAncestors(t *testing.T) {
	isolateHome(t)
	root := t.TempDir()
	sub := filepath.Join(root, "sub", "pkg")
	if err := os.MkdirAll(sub, 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}
	writeFile(t, filepath.Join(root, "AGENTS.md"), "根目录的约定：提交前跑测试\n")
	writeFile(t, filepath.Join(sub, "CLAUDE.md"), "子目录的约定：这个包别引外部依赖\n")

	got := conventionSection(sub)
	for _, want := range []string{"## 项目约定", "根目录的约定", "子目录的约定", "AGENTS.md", "CLAUDE.md"} {
		if !strings.Contains(got, want) {
			t.Fatalf("提示词段落里缺少 %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "根目录的约定") > strings.Index(got, "子目录的约定") {
		t.Fatalf("上层的约定应该排在前面:\n%s", got)
	}
	// 中层的 sub 目录没有约定文件，不该冒出空的小节
	if strings.Contains(got, filepath.Join(root, "sub")+string(filepath.Separator)+"AGENTS.md") {
		t.Fatalf("没有的约定文件不该被列出来:\n%s", got)
	}
}

// 用户目录下的全局约定也要读，而且排在项目约定前面，并标出（全局）
func TestConventionSectionReadsHome(t *testing.T) {
	home := isolateHome(t)
	writeFile(t, filepath.Join(home, ".agents", "AGENTS.md"), "全局：回答用中文\n")

	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "AGENTS.md"), "项目：这个仓库用 tab 缩进\n")

	got := conventionSection(dir)
	for _, want := range []string{"全局：回答用中文", "项目：这个仓库用 tab 缩进", "（全局）"} {
		if !strings.Contains(got, want) {
			t.Fatalf("缺少 %q:\n%s", want, got)
		}
	}
	if strings.Index(got, "全局：回答用中文") > strings.Index(got, "项目：这个仓库用 tab 缩进") {
		t.Fatalf("全局约定应该排在项目约定前面:\n%s", got)
	}
}

// 全局那份也是 AGENTS.md 优先：~/.agents/AGENTS.md 在时就不再读 ~/.claude/CLAUDE.md，
// 两份都塞进提示词太占地方，内容还容易打架
func TestConventionSectionHomePrefersAgents(t *testing.T) {
	home := isolateHome(t)
	writeFile(t, filepath.Join(home, ".agents", "AGENTS.md"), "全局：用 AGENTS 这份\n")
	writeFile(t, filepath.Join(home, ".claude", "CLAUDE.md"), "全局：CLAUDE 那份不该读\n")

	got := conventionSection(t.TempDir())
	if !strings.Contains(got, "全局：用 AGENTS 这份") {
		t.Fatalf("~/.agents/AGENTS.md 应该被读到:\n%s", got)
	}
	if strings.Contains(got, "CLAUDE 那份不该读") {
		t.Fatalf("有 AGENTS.md 时不该再读 CLAUDE.md:\n%s", got)
	}
}

// 只有 Claude Code 那份全局约定时，退回去读它，装了它的人不用再抄一遍
func TestConventionSectionHomeFallsBackToClaude(t *testing.T) {
	home := isolateHome(t)
	writeFile(t, filepath.Join(home, ".claude", "CLAUDE.md"), "全局：只有 claude 这份\n")

	got := conventionSection(t.TempDir())
	if !strings.Contains(got, "全局：只有 claude 这份") {
		t.Fatalf("没有 AGENTS.md 时应该退回读 CLAUDE.md:\n%s", got)
	}
}

// 只有全局约定、项目里什么都没有时，这一段照样要出现
func TestConventionSectionHomeOnly(t *testing.T) {
	home := isolateHome(t)
	writeFile(t, filepath.Join(home, ".agents", "AGENTS.md"), "全局：先给结论再给细节\n")

	got := conventionSection(t.TempDir())
	if !strings.Contains(got, "全局：先给结论再给细节") {
		t.Fatalf("只有全局约定时也该出现:\n%s", got)
	}
}

// 同一层里 AGENTS.md 和 CLAUDE.md 都在时只读 AGENTS.md
func TestConventionSectionSameDirPrefersAgents(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "AGENTS.md"), "agents 的话\n")
	writeFile(t, filepath.Join(dir, "CLAUDE.md"), "claude 的话\n")

	got := conventionSection(dir)
	if !strings.Contains(got, "agents 的话") {
		t.Fatalf("AGENTS.md 应该被读到:\n%s", got)
	}
	if strings.Contains(got, "claude 的话") {
		t.Fatalf("有 AGENTS.md 时不该再读 CLAUDE.md:\n%s", got)
	}
}

// 这一层只有 CLAUDE.md 时退回读它，别的层不受影响
func TestConventionSectionClaudeOnly(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "CLAUDE.md"), "claude 的话\n")

	got := conventionSection(dir)
	if !strings.Contains(got, "claude 的话") {
		t.Fatalf("没有 AGENTS.md 时该读 CLAUDE.md:\n%s", got)
	}
}

// 一个约定文件都没有时，提示词里不该多出一段空的小节
func TestConventionSectionEmpty(t *testing.T) {
	// 用户目录也指到临时目录，否则本机真有全局约定时这个断言就没意义了
	isolateHome(t)
	dir := t.TempDir()
	// 临时目录的上级（盘符根）要是有约定文件，同样跳过
	dirs := ancestorDirs(dir)
	for _, d := range dirs[:len(dirs)-1] {
		for _, name := range conventionFiles {
			if _, err := os.Stat(filepath.Join(d, name)); err == nil {
				t.Skipf("上层目录 %s 里已经有 %s，跳过", d, name)
			}
		}
	}
	if got := conventionSection(dir); got != "" {
		t.Fatalf("没有约定文件时应返回空串，实际:\n%s", got)
	}
	if got := conventionSection(""); got != "" {
		t.Fatalf("工作目录为空时应返回空串，实际:\n%s", got)
	}
}

// 超长的约定文件只读前面一段，并说清楚后面的要自己看
func TestConventionSectionTruncatesHugeFile(t *testing.T) {
	isolateHome(t) // 本机若有全局约定，长度断言就跟着机器变了
	dir := t.TempDir()
	huge := strings.Repeat("这行很长的约定内容\n", maxConventionBytes/10)
	writeFile(t, filepath.Join(dir, "AGENTS.md"), huge)

	got := conventionSection(dir)
	if !strings.Contains(got, "太长") {
		t.Fatalf("超长文件应该说明被截断了:\n%.200s", got)
	}
	// 原文近 90KB，截断后不该再有原来那么大
	if len(got) >= len(huge) {
		t.Fatalf("截断后还是原文那么大: %d 字节", len(got))
	}
	if len(got) > maxConventionBytes+2<<10 {
		t.Fatalf("截断后超出上限太多: %d 字节", len(got))
	}
}

// 内容只有空白的约定文件当没有，省得提示词里多一行空标题
func TestConventionSectionSkipsBlankFile(t *testing.T) {
	isolateHome(t)
	dir := t.TempDir()
	writeFile(t, filepath.Join(dir, "AGENTS.md"), "  \n\t\n")
	if got := conventionSection(dir); got != "" {
		t.Fatalf("空白文件不该产生段落:\n%s", got)
	}
}

func TestAncestorDirs(t *testing.T) {
	dir := t.TempDir()
	dirs := ancestorDirs(dir)
	if len(dirs) < 2 {
		t.Fatalf("应该至少有根和叶子两层: %v", dirs)
	}
	if dirs[len(dirs)-1] != filepath.Clean(dir) {
		t.Fatalf("最后一层应该是自己: %v", dirs)
	}
	if filepath.Dir(dirs[0]) != dirs[0] {
		t.Fatalf("第一层应该是根: %v", dirs)
	}
	if got := ancestorDirs("  "); got != nil {
		t.Fatalf("空目录应该返回 nil: %v", got)
	}
}

func writeFile(t *testing.T, path, content string) {
	t.Helper()
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		t.Fatalf("建目录 %s 失败: %v", filepath.Dir(path), err)
	}
	if err := os.WriteFile(path, []byte(content), 0o644); err != nil {
		t.Fatalf("写 %s 失败: %v", path, err)
	}
}

// isolateHome 把用户目录指到临时目录，返回它。
// 全局约定是从用户目录读的，不隔离的话本机真装了什么全局约定，测试结果就跟着机器变
func isolateHome(t *testing.T) string {
	t.Helper()
	home := t.TempDir()
	t.Setenv("USERPROFILE", home) // Windows 上 os.UserHomeDir 读的是这个
	t.Setenv("HOME", home)
	return home
}
