package core

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 技能清单只列名字、一句话说明和文件路径：正文太长，用到时让模型自己去读
func TestSkillSectionListsSkills(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "pdf", `---
name: pdf-处理
description: |
  拆分、合并 PDF，也能把扫描件转成图片。
  需要 qpdf 已经装好。
---
# PDF

先看 `+"`qpdf --version`"+`。
`)
	// 没有 frontmatter 的也认：名字用目录名，描述用正文第一段
	writeSkill(t, root, "release", "# Release\n\n按这个流程发版。\n")
	// 没有 SKILL.md 的目录跳过
	if err := os.MkdirAll(filepath.Join(root, "draft"), 0o755); err != nil {
		t.Fatalf("建目录失败: %v", err)
	}

	got := skillSection(root)
	for _, want := range []string{
		"## 可用技能",
		"pdf-处理",
		"拆分、合并 PDF，也能把扫描件转成图片。",
		filepath.Join(root, "pdf", "SKILL.md"),
		"release",
		"按这个流程发版。",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("技能清单里缺少 %q:\n%s", want, got)
		}
	}
	if strings.Contains(got, "draft") {
		t.Fatalf("没有 SKILL.md 的目录不该出现:\n%s", got)
	}
	// 多行描述压成一行，别把提示词撑开
	if !strings.Contains(got, "转成图片。 需要 qpdf 已经装好。") {
		t.Fatalf("多行描述应该压成一行:\n%s", got)
	}
	if !strings.Contains(got, "Read") {
		t.Fatalf("应该告诉模型用到时先 Read 一遍:\n%s", got)
	}
}

// 直接摆在技能目录根上的 .md 也算一个技能
func TestLoadSkillsFlatMarkdown(t *testing.T) {
	root := t.TempDir()
	writeFile(t, filepath.Join(root, "translate.md"), "---\ndescription: 中英互译\n---\n正文\n")

	skills := loadSkills(root)
	if len(skills) != 1 {
		t.Fatalf("应该认出 1 个技能，实际 %d 个: %+v", len(skills), skills)
	}
	if skills[0].Name != "translate" || skills[0].Desc != "中英互译" {
		t.Fatalf("名字和描述不对: %+v", skills[0])
	}
}

// 名字排序固定下来，提示词每轮都一样，接口侧的缓存才不会白白失效
func TestLoadSkillsSorted(t *testing.T) {
	root := t.TempDir()
	for _, name := range []string{"zeta", "alpha", "mid"} {
		writeSkill(t, root, name, "---\ndescription: "+name+"\n---\n")
	}
	skills := loadSkills(root)
	want := []string{"alpha", "mid", "zeta"}
	if len(skills) != len(want) {
		t.Fatalf("应该 3 个技能，实际 %d 个", len(skills))
	}
	for i, name := range want {
		if skills[i].Name != name {
			t.Fatalf("第 %d 个应该是 %s，实际 %s", i, name, skills[i].Name)
		}
	}
}

// 技能装得太多时清单按字节预算截断，并说清楚还剩多少个、去哪儿找
func TestSkillSectionBudget(t *testing.T) {
	root := t.TempDir()
	// 每个技能塞一段很长的描述，几个就能把预算吃满
	long := strings.Repeat("长描述", 40)
	for i := 0; i < 200; i++ {
		writeSkill(t, root, "s"+strings.Repeat("x", i%7)+"-"+string(rune('a'+i%26))+"-"+strings.Repeat("y", i/26),
			"---\ndescription: "+long+"\n---\n")
	}
	got := skillSection(root)
	if len(got) > maxSkillSectionBytes+4096 {
		t.Fatalf("清单超出预算太多: %d 字节", len(got))
	}
	if !strings.Contains(got, "还有") || !strings.Contains(got, root) {
		t.Fatalf("截断时要说明还剩多少、去哪儿找:\n%.400s", got)
	}
	if !strings.Contains(got, "Glob") {
		t.Fatalf("应该告诉模型可以用 Glob 去目录里翻:\n%.400s", got)
	}
}

// 描述太长时按字符截断，别把中文切坏
func TestSkillDescTruncated(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "s", "---\ndescription: "+strings.Repeat("很长的说明", 40)+"\n---\n")
	skills := loadSkills(root)
	if len(skills) != 1 {
		t.Fatalf("应该 1 个技能，实际 %d 个", len(skills))
	}
	if n := len([]rune(skills[0].Desc)); n > maxSkillDescRunes+1 {
		t.Fatalf("描述应截到 %d 字符以内，实际 %d 个字符", maxSkillDescRunes, n)
	}
	if strings.ContainsRune(skills[0].Desc, '\uFFFD') {
		t.Fatalf("截断把字符切坏了: %q", skills[0].Desc)
	}
}

// 没装技能（目录都不存在）时提示词里不该多一段空清单
func TestSkillSectionMissingDir(t *testing.T) {
	if got := skillSection(filepath.Join(t.TempDir(), "not-created")); got != "" {
		t.Fatalf("目录不存在时应返回空串:\n%s", got)
	}
	if got := skillSection(""); got != "" {
		t.Fatalf("空路径应返回空串:\n%s", got)
	}
}

// 技能目录在用户目录下，路径不能跟着当前目录变
func TestSkillsRoot(t *testing.T) {
	home := t.TempDir()
	t.Setenv("USERPROFILE", home) // Windows 上 os.UserHomeDir 读的是这个
	t.Setenv("HOME", home)

	root, err := SkillsRoot()
	if err != nil {
		t.Fatalf("取技能目录失败: %v", err)
	}
	want := filepath.Join(home, ".agents", "skills")
	if root != want {
		t.Fatalf("技能目录应该是 %s，实际 %s", want, root)
	}
}

// 按名字找技能：带不带 / 都认，前缀只在对得上唯一一个时才认——
// 猜错了会拿另一个技能的步骤去干活，比让用户多打几个字糟得多
func TestFindSkill(t *testing.T) {
	skills := []Skill{
		{Name: "add-disease", Path: "a"},
		{Name: "add-scheduled-task", Path: "b"},
		{Name: "Research", Path: "c"},
	}
	cases := []struct {
		name string
		want string
		ok   bool
	}{
		{"add-disease", "add-disease", true},
		{"/add-disease", "add-disease", true},
		{"  /add-disease  ", "add-disease", true},
		{"research", "Research", true}, // 大小写不敏感
		{"res", "Research", true},      // 唯一前缀
		{"add", "", false},             // 前缀对上两个，不猜
		{"zzz", "", false},
		{"", "", false},
		{"/", "", false},
	}
	for _, c := range cases {
		got, ok := FindSkill(skills, c.name)
		if ok != c.ok {
			t.Fatalf("%q 应该是 %v，实际 %v", c.name, c.ok, ok)
		}
		if ok && got.Name != c.want {
			t.Fatalf("%q 应该找到 %s，实际 %s", c.name, c.want, got.Name)
		}
	}
}

// 手动调用技能时把说明正文整段摆进消息：模型不用再读一遍文件，
// frontmatter 留着自己用，不占发给模型的正文
func TestSkillPromptInlinesBody(t *testing.T) {
	root := t.TempDir()
	writeSkill(t, root, "pdf", `---
name: pdf-处理
description: 拆分合并 PDF
---
# 步骤

1. 确认 qpdf 装好了。
`)
	skills := loadSkills(root)
	if len(skills) != 1 {
		t.Fatalf("应该 1 个技能，实际 %d 个", len(skills))
	}
	got := SkillPrompt(skills[0], "把 a.pdf 和 b.pdf 合并")
	for _, want := range []string{
		"pdf-处理",
		"/pdf-处理",
		skills[0].Path,
		"--- 技能说明开始 ---",
		"1. 确认 qpdf 装好了。",
		"--- 技能说明结束 ---",
		"把 a.pdf 和 b.pdf 合并",
	} {
		if !strings.Contains(got, want) {
			t.Fatalf("展开出来的消息里缺少 %q:\n%s", want, got)
		}
	}
	// 正文前面的 frontmatter 不该跟着塞进去
	if strings.Contains(got, "description: 拆分合并 PDF") {
		t.Fatalf("frontmatter 不该出现在正文里:\n%s", got)
	}

	// 没补充说明时，让模型先问清楚缺什么
	if bare := SkillPrompt(skills[0], "   "); !strings.Contains(bare, "没有补充说明") {
		t.Fatalf("应该交代一句没有补充说明:\n%s", bare)
	}
}

// 说明读不动（文件没了、或者大到会把上下文吃光）时，退回把路径留给模型自己读
func TestSkillPromptFallsBackToPath(t *testing.T) {
	missing := Skill{Name: "gone", Path: filepath.Join(t.TempDir(), "nope", skillFileName)}
	got := SkillPrompt(missing, "")
	if !strings.Contains(got, missing.Path) || !strings.Contains(got, "Read") {
		t.Fatalf("读不动时应该让模型自己去读:\n%s", got)
	}
	if strings.Contains(got, "--- 技能说明开始 ---") {
		t.Fatalf("没读到正文就不该有正文分隔线:\n%s", got)
	}

	root := t.TempDir()
	writeSkill(t, root, "huge", strings.Repeat("很长的一行说明\n", maxSkillPromptBytes/8))
	huge := loadSkills(root)
	if len(huge) != 1 {
		t.Fatalf("应该 1 个技能，实际 %d 个", len(huge))
	}
	big := SkillPrompt(huge[0], "")
	if strings.Contains(big, "--- 技能说明开始 ---") {
		t.Fatalf("超过上限的说明不该整段摆进去（%d 字节）", len(big))
	}
	if !strings.Contains(big, huge[0].Path) {
		t.Fatalf("大文件也得把路径留给模型:\n%.200s", big)
	}
}

func writeSkill(t *testing.T, root, name, content string) {
	t.Helper()
	dir := filepath.Join(root, name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建技能目录失败: %v", err)
	}
	writeFile(t, filepath.Join(dir, skillFileName), content)
}
