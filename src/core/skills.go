package core

import (
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"gopkg.in/yaml.v3"
	"hamusuta/agent-dev/src/tool"
)

// skillFileName 是每个技能目录里那份说明的名字，技能的动作步骤都写在里面
const skillFileName = "SKILL.md"

// maxSkills 是扫技能目录时最多认多少个技能，防止目录里堆了几千个东西时白读一遍
const maxSkills = 300

// maxSkillSectionBytes 是技能清单在提示词里占的字节上限。技能装得多时清单会很长，
// 每轮请求都要带上，撑爆上下文比"少列几个"更糟；超了这个预算就少列几个，
// 并说清楚还有多少、放在哪儿，模型临时要的话自己去目录里翻
const maxSkillSectionBytes = 32 << 10

// maxSkillDescRunes 是一句话描述的截断长度，描述写成长篇也该压成一行
const maxSkillDescRunes = 80

// maxSkillPromptBytes 是用户手动调用技能时，直接摆进消息里的说明正文上限。
// SKILL.md 一般只有几 KB；真碰上几百 KB 的那种，塞进去会把上下文挤掉，
// 还不如把路径留给模型，让它自己去 Read
const maxSkillPromptBytes = 128 << 10

// Skill 是一个装在技能目录里的技能：名字、一句话说明，以及说明文件的完整路径。
// 描述只用来决定"要不要用它"，真要用还得让模型去 Read 那个文件
type Skill struct {
	Name    string
	Desc    string
	Path    string
	invalid string // 读不动的原因，只在排查用，不写进提示词
}

// SkillsRoot 返回技能目录：<用户目录>/.agents/skills
func SkillsRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("找不到用户目录: %w", err)
	}
	return filepath.Join(home, ".agents", "skills"), nil
}

// skillSection 把技能目录里的技能列成系统提示词里的一段，没有技能时返回空串。
// 这里只列名字、一句话说明和文件路径：技能正文动辄几百行，
// 全塞进提示词会挤掉真正要干活的上下文，让模型用到时自己去读。
func skillSection(root string) string {
	skills := loadSkills(root)
	if len(skills) == 0 {
		return ""
	}
	var sb strings.Builder
	sb.WriteString("\n## 可用技能（Skills）\n")
	fmt.Fprintf(&sb, "用户目录下装了这些技能（%s），都是现成的做法：\n", root)
	listed := 0
	for _, s := range skills {
		line := fmt.Sprintf("- %s：%s（%s）\n", s.Name, s.Desc, s.Path)
		// 超预算就停，剩下的在末尾一句话交代，别让清单把上下文吃光
		if sb.Len()+len(line) > maxSkillSectionBytes {
			break
		}
		sb.WriteString(line)
		listed++
	}
	if rest := len(skills) - listed; rest > 0 {
		fmt.Fprintf(&sb, "…还有 %d 个技能没列在这里（装得太多了），目录是 %s："+
			"需要时用 Glob 列出那一层看看名字，再 Read 对应的 SKILL.md。\n", rest, root)
	}
	sb.WriteString("要做的事情正好对得上某个技能时，先用 Read 把它的那份 SKILL.md 完整读一遍，" +
		"再照着里面的步骤做；只看上面这一行描述动手容易漏步骤。\n")
	sb.WriteString("技能里提到的脚本、模板就在同一个目录下，路径照它写的来。\n")
	return sb.String()
}

// loadSkills 扫技能目录，返回能用的技能（按名字排序，方便每轮提示词稳定不变，
// 也就不会让接口侧的上下文缓存失效）。
// 认两种摆法：<root>/<名字>/SKILL.md（推荐），以及直接丢在根上的 <root>/<名字>.md
func loadSkills(root string) []Skill {
	root = strings.TrimSpace(root)
	if root == "" {
		return nil
	}
	entries, err := os.ReadDir(root)
	if err != nil {
		return nil // 没装技能就不该有任何噪音
	}

	var skills []Skill
	for _, entry := range entries {
		var path, fallback string
		if entry.IsDir() {
			path, fallback = filepath.Join(root, entry.Name(), skillFileName), entry.Name()
		} else {
			name := entry.Name()
			if !strings.EqualFold(filepath.Ext(name), ".md") || strings.EqualFold(name, skillFileName) {
				continue
			}
			path, fallback = filepath.Join(root, name), strings.TrimSuffix(name, filepath.Ext(name))
		}
		// 目录里没有 SKILL.md 就跳过：那可能是技能的附属目录、草稿，或者别的东西
		if info, err := os.Stat(path); err != nil || info.IsDir() {
			continue
		}
		skill := readSkill(path, fallback)
		if skill.invalid != "" {
			continue
		}
		skills = append(skills, skill)
	}

	sort.Slice(skills, func(i, j int) bool { return skills[i].Name < skills[j].Name })
	if len(skills) > maxSkills {
		skills = skills[:maxSkills]
	}
	return skills
}

// Skills 返回技能目录里装好的技能（按名字排序）。没装技能、读不动目录时返回空，
// 调用方不用处理错误：技能是锦上添花，缺了不该拦住任何流程
func Skills() []Skill {
	root, err := SkillsRoot()
	if err != nil {
		return nil
	}
	return loadSkills(root)
}

// FindSkill 在清单里按名字找一个技能，名字前面的 "/" 会自动去掉。
// 先认完整名字，再认只对得上的前缀；前缀能对上好几个就当没找到——
// 猜错了会拿另一个技能的步骤去干活，比让用户多打几个字糟得多
func FindSkill(skills []Skill, name string) (Skill, bool) {
	name = strings.TrimSpace(name)
	name = strings.TrimSpace(strings.TrimPrefix(name, "/"))
	if name == "" {
		return Skill{}, false
	}
	for _, s := range skills {
		if strings.EqualFold(s.Name, name) {
			return s, true
		}
	}
	lower := strings.ToLower(name)
	var matched []Skill
	for _, s := range skills {
		if strings.HasPrefix(strings.ToLower(s.Name), lower) {
			matched = append(matched, s)
		}
	}
	if len(matched) == 1 {
		return matched[0], true
	}
	return Skill{}, false
}

// SkillPrompt 把一份技能展开成一条用户消息：说明全文摆在前头，再接用户补的话。
// 用户既然点名要这个技能，就省掉"模型先 Read 一遍再说"那一轮，
// 也保证它看到的是完整的那份说明，而不是记忆里的一行描述
func SkillPrompt(s Skill, rest string) string {
	var sb strings.Builder
	fmt.Fprintf(&sb, "用户手动调用了技能「%s」（/%s），这次就照它的步骤做。\n", s.Name, s.Name)
	fmt.Fprintf(&sb, "技能文件：%s\n", s.Path)

	if body := skillBody(s.Path); body != "" {
		sb.WriteString("\n--- 技能说明开始 ---\n")
		sb.WriteString(strings.TrimSpace(body))
		sb.WriteString("\n--- 技能说明结束 ---\n")
	} else {
		// 读不动或者太大：路径已经给了，让模型自己去读，别把这份说明整段丢掉
		sb.WriteString("（这份说明没能一次读进来——太大或读不动，先用 Read 把上面那个文件读完再动手。）\n")
	}

	if rest = strings.TrimSpace(rest); rest != "" {
		sb.WriteString("\n用户这一条说：" + rest)
	} else {
		sb.WriteString("\n用户没有补充说明：技能里要什么输入、缺哪些信息，先问清楚再动手。")
	}
	return sb.String()
}

// skillBody 读技能说明的正文（去掉 frontmatter）；读不动或者超过上限时返回空串
func skillBody(path string) string {
	text, err := tool.ReadText(path)
	if err != nil || len(text) > maxSkillPromptBytes {
		return ""
	}
	if _, body := splitFrontMatter(text); strings.TrimSpace(body) != "" {
		return body
	}
	return text
}

// readSkill 读一份 SKILL.md：名字和描述优先取文件头的 frontmatter，
// 缺了就退回目录名和正文第一段。技能本身写得潦草不该让它消失
func readSkill(path, fallbackName string) Skill {
	skill := Skill{Name: fallbackName, Path: path}
	text, err := tool.ReadText(path)
	if err != nil {
		skill.invalid = err.Error()
		return skill
	}

	front, body := splitFrontMatter(text)
	var meta struct {
		Name        string `yaml:"name"`
		Description string `yaml:"description"`
	}
	if strings.TrimSpace(front) != "" {
		// frontmatter 写坏了不算致命：退回正文和目录名，照样能用
		_ = yaml.Unmarshal([]byte(front), &meta)
	}
	skill.Name = strings.TrimSpace(meta.Name)
	if skill.Name == "" {
		skill.Name = fallbackName
	}
	skill.Desc = firstMeaningfulLine(meta.Description)
	if skill.Desc == "" {
		skill.Desc = firstParagraph(body)
	}
	if skill.Desc == "" {
		skill.Desc = "（没有写说明，用到时看文件）"
	}
	skill.Desc = truncateRunes(skill.Desc, maxSkillDescRunes)
	return skill
}

// splitFrontMatter 把 "---" 包起来的那段 yaml 和正文分开。没有 frontmatter 时 front 为空
func splitFrontMatter(text string) (front, body string) {
	text = strings.TrimPrefix(text, "\ufeff") // 有的编辑器会加 BOM
	lines := strings.Split(text, "\n")
	if len(lines) == 0 || strings.TrimSpace(strings.TrimSuffix(lines[0], "\r")) != "---" {
		return "", text
	}
	for i := 1; i < len(lines); i++ {
		if strings.TrimSpace(strings.TrimSuffix(lines[i], "\r")) == "---" {
			return strings.Join(lines[1:i], "\n"), strings.Join(lines[i+1:], "\n")
		}
	}
	// 只有开头没有结尾：多半不是 frontmatter，整篇当正文
	return "", text
}

// firstMeaningfulLine 把多行描述压成一行，空的话返回空串
func firstMeaningfulLine(s string) string {
	return strings.TrimSpace(strings.Join(strings.Fields(s), " "))
}

// firstParagraph 取正文第一段既不是标题也不是空行的文字，兜底当描述用
func firstParagraph(body string) string {
	for _, line := range strings.Split(body, "\n") {
		line = strings.TrimSpace(strings.TrimSuffix(line, "\r"))
		if line == "" || strings.HasPrefix(line, "#") || strings.HasPrefix(line, "---") {
			continue
		}
		return firstMeaningfulLine(line)
	}
	return ""
}

// truncateRunes 按字符数（不是字节）截断，免得把中文切成乱码
func truncateRunes(s string, max int) string {
	runes := []rune(s)
	if len(runes) <= max {
		return s
	}
	return strings.TrimSpace(string(runes[:max])) + "…"
}
