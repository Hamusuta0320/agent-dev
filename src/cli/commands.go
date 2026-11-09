package main

import (
	"fmt"
	"path/filepath"
	"strings"

	cmdspec "hamusuta/agent-dev/src/command"
	"hamusuta/agent-dev/src/core"
	"hamusuta/agent-dev/src/domain"
	"hamusuta/agent-dev/src/session"
)

// command 是本地命令，不发给模型
type command int

const (
	cmdNone command = iota
	cmdHelp
	cmdNew
	cmdResume
	cmdSession
	cmdUsage
	cmdCompact
	cmdConfig
	cmdMode
	cmdModel
	cmdSkills
	cmdTodo
	cmdQuiet
	cmdEffort
	cmdExit
)

// commandSpec 是一条本地命令的说明。帮助文本和输入补全都从这张表生成，
// 加命令只改这里一处。
type commandSpec struct {
	cmd  command
	name string
	desc string
}

// cmdOf 是"命令名 → 这一层认识的枚举"。名字和说明本身来自 command 包
// （浏览器界面用的是同一份），这里只补上终端这边执行时要走哪个分支
var cmdOf = map[string]command{
	"/help":    cmdHelp,
	"/new":     cmdNew,
	"/resume":  cmdResume,
	"/session": cmdSession,
	"/usage":   cmdUsage,
	"/compact": cmdCompact,
	"/config":  cmdConfig,
	"/mode":    cmdMode,
	"/model":   cmdModel,
	"/effort":  cmdEffort,
	"/skills":  cmdSkills,
	"/todo":    cmdTodo,
	"/quiet":   cmdQuiet,
	"/exit":    cmdExit,
}

// commandSpecs 是帮助文本和补全候选的数据源，顺序跟 cmdspec.Specs 一致
var commandSpecs = buildCommandSpecs()

func buildCommandSpecs() []commandSpec {
	out := make([]commandSpec, 0, len(cmdspec.Specs))
	for _, spec := range cmdspec.Specs {
		out = append(out, commandSpec{cmd: cmdOf[spec.Name], name: spec.Name, desc: spec.Desc})
	}
	return out
}

// parseCommand 认出输入里的本地命令，认不出就当成普通提问
func parseCommand(input string) command {
	switch firstField(input) {
	case "/quit", "exit", "quit":
		// 这几个是终端里额外的别名，两个界面共用的表里不列它们
		return cmdExit
	}
	name := firstField(input)
	if name == "" {
		return cmdNone
	}
	if cmd, ok := cmdOf[name]; ok {
		return cmd
	}
	return cmdNone
}

// suggestCommands 返回能把当前输入补全的命令。
// 匹配规则在 command 包里（浏览器界面用的是同一份），这里只换成终端这边的类型
func suggestCommands(input string) []commandSpec {
	var out []commandSpec
	for _, spec := range cmdspec.Suggest(input) {
		out = append(out, commandSpec{cmd: cmdOf[spec.Name], name: spec.Name, desc: spec.Desc})
	}
	return out
}

// suggestSkills 是能补全的技能：名字前面补个 /，和内置命令摆在同一张候选表里。
// 规则跟内置命令一样，只在还没打空格时补；已经打全了就不再提示。
// 这里对大小写不敏感——技能名是用户自己起的，不该逼他记住大小写
func suggestSkills(input string, skills []core.Skill) []commandSpec {
	s := cmdspec.Prefix(input)
	if s == "" {
		return nil
	}
	lower := strings.ToLower(s)
	var out []commandSpec
	for _, skill := range skills {
		name := "/" + skill.Name
		if strings.HasPrefix(strings.ToLower(name), lower) {
			out = append(out, commandSpec{name: name, desc: skill.Desc})
		}
	}
	if len(out) == 1 && strings.EqualFold(out[0].name, s) {
		return nil
	}
	return out
}

// looksLikeCommand 判断输入的第一段是不是"想打一条命令"。判据在 command 包里，
// 两个界面共用一个标准
func looksLikeCommand(field string) bool {
	return cmdspec.LooksLike(field)
}

// firstField 是输入的第一段，空输入时返回空串
func firstField(input string) string {
	return cmdspec.FirstField(input)
}

// parseSkillCall 把 "/技能名 补充说明" 拆成技能和后面那截补充说明。
// 认不出技能时 ok 为 false——是命令打错了还是技能名不对，交给调用方去看
func parseSkillCall(input string, skills []core.Skill) (core.Skill, string, bool) {
	name := firstField(input)
	if name == "" {
		return core.Skill{}, "", false
	}
	skill, ok := core.FindSkill(skills, name)
	if !ok {
		return core.Skill{}, "", false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(input), name))
	return skill, rest, true
}

func helpText() string {
	lines := []string{bannerStyle.Render("命令")}
	for _, spec := range commandSpecs {
		lines = append(lines, fmt.Sprintf("  %-9s %s", spec.name, spec.desc))
	}
	lines = append(lines,
		"",
		bannerStyle.Render("授权"),
		"  编辑需授权模式下，写文件、改文件、执行命令前会停下来问你：",
		"  ↑↓ 或 j k 选选项，Enter 确认（默认就是允许一次）",
		"  y 允许一次 · a 本回合都允许这个工具 · n / Esc 拒绝",
		"  在确认框里直接打字，就当成\"拒绝并说明\"：写完回车，这句话会转给模型",
		"  只读模式不给写类工具；完全授权模式不再问",
		"",
		bannerStyle.Render("输入"),
		"  Enter 发送，"+newlineHint()+"（终端不认修饰键时 Shift+Enter 和 Enter 是同一个键），粘贴多行文本会当成一次输入",
		"  输入以 / 开头时会列出可补全的命令，↑↓ 选择、Tab 或 Enter 补全",
		"  Tab 被终端接走时（比如 VS Code 集成终端），改用 Ctrl+Space 或 → 补全",
		"",
		bannerStyle.Render("按键列表"),
		"  /resume、/model、/mode、/effort 不带参数时，把候选列在输入框上方让你按键挑：",
		"  ↑↓ 或 j k 选（PgUp / PgDn 翻页、Home / End 到两头），Enter 定下来，Esc 收起",
		"  一打字列表就收起来，字照常进输入框；带参数打的 /resume 1、/model 名字 依旧能直接用",
		"",
		bannerStyle.Render("排队"),
		"  一轮还在跑的时候按 Enter 不会丢：消息排进队里，输入框上方能看到排了哪些（黄字）",
		"  这一轮正常跑完就自动接上队头那条，不用等它停下来再敲一遍；中断或出错时留着不发",
		"  Alt+↑ / Alt+↓ 选中某条（输入框空着时裸 ↑↓ 也行），Alt+E 取回输入框改，Del 删掉，Esc 取消选中",
		"  Alt+方向键和 Alt+E 都留给排队，裸 ↑↓、Ctrl+E 还是输入框的：多行输入要能上下挪光标、跳到行尾",
		"  排队里写 /命令、点技能名都和在空闲时敲一样生效",
		"",
		bannerStyle.Render("图片"),
		"  截图后按 Ctrl+V（终端把 Ctrl+V 接走时用 Alt+V）贴剪贴板里的图",
		"  剪贴板里是文字时，这个键就是普通粘贴；终端自己接管 Ctrl+V 时走它自带的粘贴",
		"  也可以把图片文件直接拖进终端，或者粘贴图片的路径",
		"  附上的图会随下一条提问一起发给模型，输入框上方能看到带了哪几张",
		"  只贴图不写字也能发；Esc 清空附图；一条提问最多 "+fmt.Sprint(maxAttachments)+
			" 张，单张最大 "+fmt.Sprint(domain.MaxImageBytes>>20)+"MB",
		"",
		bannerStyle.Render("待办与模型"),
		"  多步任务里模型会用 TodoWrite 列清单，输入框上方常驻一行进度，/todo 看完整清单",
		"  /config 里类型用 ←→ 在 DeepSeek / OpenAI / MiniMax 之间换（都走 OpenAI 兼容协议），",
		"  接口地址留空就用该类型内置的（如 https://api.deepseek.com），也可以手填覆盖；",
		"  地址和密钥填好后按 Ctrl+L 能从接口拉模型列表，↑↓ 挑、Enter 采用，也可以直接手填模型名",
		"  思考强度用 ←→ 在档位之间换，会作为 reasoning_effort 发给接口；档位按类型不同——",
		"  DeepSeek 认 默认 / none / minimal / low / medium / high / xhigh / ultra / max，",
		"  另两家只认 默认 / minimal / low / medium / high（这部分没实测过，不确定的不往里加）；",
		"  留\"默认\"就不发这个参数（认不认它看各家接口）",
		"  不想开表单就直接打 /effort：不带参数列出几档（↑↓ 选、Enter 就改成那一档），/effort high 直接改",
		"  /model 不带参数列出配置好的几套（↑↓ 选、Enter 切过去），同样会写回配置文件，重启还是那套",
		"  /model add 新增一套（名字留空就用模型名），/model del 名字 删掉一套（会再问一次）",
		"  配置文件在用户目录下的 .agent-dev/config.yaml，跟工作区无关；Models 下可以写好几套，",
		"  Current 指定默认用哪套；只写老格式的 Provider 段也能跑，/config 保存一次就会补齐两条",
		"",
		bannerStyle.Render("约定与技能"),
		"  工作目录和它上层目录里的 AGENTS.md 会自动读进提示词，不用每次重复交代",
		"  没有 AGENTS.md 的地方才退回读 CLAUDE.md，同一处只读一份",
		"  跨项目通用的规矩放 ~/.agents/AGENTS.md（全局，同样优先于 ~/.claude/CLAUDE.md）",
		"  技能放在 ~/.agents/skills/<名字>/SKILL.md：文件头 frontmatter 里写 name 和 description，",
		"  正文写步骤；启动后模型能看到技能清单，用到时自己把那份文件读完再动手",
		"  想直接点名用某个技能，就打 /技能名（后面可以跟补充说明，也可以不带）——",
		"  那份 SKILL.md 会整段摆进这一条消息里，模型照着做，不用再读一遍文件",
		"  /skills 看装了哪些技能；输入 / 开头的名字时，技能和内置命令一起补全",
		"  /help 之外以 / 开头又不认识的，会提示一句，不会当成提问发给模型",
	)
	return strings.Join(lines, "\n")
}

// skillsText 是 /skills 的输出：装了哪些技能，以及怎么直接调用
func skillsText(skills []core.Skill) string {
	root, _ := core.SkillsRoot()
	title := bannerStyle.Render("技能")
	if len(skills) == 0 {
		return title + "\n" + hintStyle.Render("  还没装：把说明放进 "+
			filepath.Join(root, "<名字>", "SKILL.md")+"，重启后 /skills 就能看到")
	}
	title += hintStyle.Render(fmt.Sprintf("  %d 个，目录 %s", len(skills), root))
	// 名字长短不一，按最长的对齐，后面的说明才不会参差
	width := 0
	for _, skill := range skills {
		if n := len([]rune(skill.Name)) + 1; n > width {
			width = n
		}
	}
	lines := []string{title}
	for _, skill := range skills {
		lines = append(lines, promptStyle.Render(fmt.Sprintf("  /%-*s", width, skill.Name))+
			hintStyle.Render(" "+skill.Desc))
	}
	lines = append(lines,
		"",
		hintStyle.Render("  直接调用：/技能名，后面可以跟补充说明；那份 SKILL.md 会整段摆进这一条消息"))
	return strings.Join(lines, "\n")
}

// unknownCommandText 是打了一条不认识的 /命令 时的提示。
// 技能名记不全很常见，把名字里含这段字都列出来，比只甩一句"没有这个命令"有用
func unknownCommandText(name string, skills []core.Skill) string {
	near := cmdspec.Near(name, skillNames(skills))
	lines := []string{errStyle.Render("没有叫 " + name + " 的命令或技能")}
	if len(near) > 0 {
		lines = append(lines, hintStyle.Render("  相近的：")+strings.Join(near, " · "))
	}
	lines = append(lines, hintStyle.Render(
		"  /help 看命令，/skills 看装了哪些技能；不想走命令就把开头的 / 去掉"))
	return strings.Join(lines, "\n")
}

// skillNames 取出技能名，够 cmdspec.Near 用
func skillNames(skills []core.Skill) []string {
	out := make([]string, 0, len(skills))
	for _, skill := range skills {
		out = append(out, skill.Name)
	}
	return out
}

func sessionText(cur *domain.Session, workdir string, store *session.Store) string {
	dir := ""
	if store != nil {
		dir = store.Dir
	}
	out := fmt.Sprintf("工作区 %s\n会话 %s，共 %d 条消息", workdir, cur.Id, len(cur.Messages))
	if title := strings.TrimSpace(cur.Title); title != "" {
		out += "\n标题 " + title
	}
	if dir != "" {
		out += "\n存到 " + dir
	}
	return out
}

// modeHint 是一句话说明某个模式放行到什么程度
func modeHint(mode domain.Mode) string {
	switch mode {
	case domain.ModeReadOnly:
		return "只看不改：不给写文件、改文件、执行命令的工具"
	case domain.ModeFull:
		return "写操作直接执行，不再确认"
	default:
		return "写操作逐个确认：改哪个文件、跑什么命令都先摊开给你看"
	}
}
