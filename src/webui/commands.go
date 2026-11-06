package webui

import (
	"fmt"
	"strconv"
	"strings"

	"hamusuta/agent-dev/src/command"
	"hamusuta/agent-dev/src/config"
	"hamusuta/agent-dev/src/core"
	"hamusuta/agent-dev/src/domain"
	"hamusuta/agent-dev/src/session"
	"hamusuta/agent-dev/src/tool"
)

// 命令执行完回给前端的东西。浏览器界面和终端不一样：终端把结果打进历史区，
// 这里回一段 JSON，前端按 Kind 决定画成一段文字、弹表单，还是切到另一条会话。
const (
	// replyText 是"打印一段文本"：帮助、各种列表都走它
	replyText = "text"
	// replyConfig 表示要打开 /config 表单：真正的配置值走 GET /api/config 取
	replyConfig = "config"
	// replySession 表示该切到另一条会话（/new 新建的、/resume 挑中的）
	replySession = "session"
	// replyConfirm 表示要用户点一下确定才继续：前端弹确认框，
	// 点了确定再发 Confirm 里说的那个请求
	replyConfirm = "confirm"
)

// commandReply 是一条本地命令的结果
type commandReply struct {
	Kind string `json:"kind"`
	// Text 是给人看的一段纯文本。前端一律按 textContent 摆进 pre，不解析标记
	Text string `json:"text,omitempty"`
	// Session 是 Kind 为 session 时要切过去的会话 id
	Session string `json:"session,omitempty"`
	// Meta 只在配置被这条命令改过时带上（/mode、/model、/effort），前端拿来刷新顶部
	Meta *meta `json:"meta,omitempty"`
	// Config 是 Kind 为 config 时的表单模式："edit" 改当前那套，"new" 新增一套
	Config string `json:"config,omitempty"`
	// Confirm 是 Kind 为 confirm 时要确认的动作，形如 "del-model:名字"。
	// 前端弹确认框，用户点确定后再照着它发请求
	Confirm string `json:"confirm,omitempty"`
}

// textReply 造一条纯文本回复
func textReply(format string, args ...any) commandReply {
	return commandReply{Kind: replyText, Text: fmt.Sprintf(format, args...)}
}

// runCommand 执行一条以 / 开头的本地命令。认不出的不是错误：
// 回一段"没有这条命令"的提示，和终端那边说的话一致
func (m *Manager) runCommand(s *Session, input string) commandReply {
	name := command.FirstField(input)
	arg := command.Args(input)

	// 每条命令先过一遍这张表：名字认不出就提示相近的，别把 /zzz 当成提问发给模型。
	// 技能名不在这张表里——那是"直接点名用某个技能"，属于提问，交给 handleSend
	if _, known := command.Find(name); !known {
		return textReply("%s", unknownCommandText(name))
	}

	switch name {
	case "/help":
		return textReply("%s", helpText())
	case "/new":
		return m.commandNew(s)
	case "/resume":
		return m.commandResume(s, arg)
	case "/session":
		return m.commandSession(s)
	case "/config":
		return commandReply{Kind: replyConfig}
	case "/mode":
		return m.commandMode(arg)
	case "/model":
		return m.commandModel(arg)
	case "/effort":
		return m.commandEffort(arg)
	case "/skills":
		return textReply("%s", skillsText())
	case "/todo":
		return textReply("%s", todoText())
	case "/quiet":
		return m.commandQuiet(arg)
	case "/exit":
		return textReply("%s", "浏览器界面直接关标签页就行；服务端在终端窗口里用 Ctrl+C 停。")
	}
	return textReply("不认识这条命令：%s", name)
}

// unknownCommandText 是打了一条不认识的 /命令 时的提示，和终端那边同一套措辞
func unknownCommandText(name string) string {
	near := command.Near(name, nil)
	lines := []string{"没有叫 " + name + " 的命令"}
	if len(near) > 0 {
		lines = append(lines, "相近的："+strings.Join(near, " · "))
	}
	lines = append(lines, "/help 看命令；不想走命令就把开头的 / 去掉")
	return strings.Join(lines, "\n")
}

// helpText 是 /help 的正文。终端那边按终端的键位写，这里按浏览器的
func helpText() string {
	lines := []string{"命令"}
	for _, spec := range command.Specs {
		lines = append(lines, fmt.Sprintf("  %-9s %s", spec.Name, spec.Desc))
	}
	lines = append(lines,
		"",
		"输入",
		"  Enter 发送，Shift+Enter 换行，可以直接粘贴截图",
		"  输入框里打 / 会列出命令和技能，↑↓ 选、Tab 或 Enter 补全",
		"  运行中再发就排进队里，这一轮正常结束自动接上",
		"",
		"环境",
		"  顶栏的模式下拉框直接切工作模式；模型、思考强度那两枚徽标点开就是配置表单",
		"  /config 也是同一个表单：接口地址、密钥、模型名、上下文上限、思考强度，",
		"  地址和密钥填好后能直接从接口拉模型列表，点一个就填进去",
		"  显示方式默认是精简（只显示正文），顶栏的「精简」徽标或 /quiet 可以切；",
		"  关掉之后思考过程折叠显示、工具输出点开能看",
		"  /model 不带参数列出配置好的几套；顶栏的「模型」徽标点开，已配好的几套点一下就切；",
		"  /model add 新增一套，/model del 名字 删掉一套",
		"  左侧能切工作区、加工作区；会话列表点一下就能切换",
		"  写文件、执行命令前会弹出来问一次（完全授权模式不问）",
	)
	return strings.Join(lines, "\n")
}

// skillsText 是 /skills 的输出
func skillsText() string {
	skills := core.Skills()
	root, _ := core.SkillsRoot()
	if len(skills) == 0 {
		return "还没装技能：把说明放进 " + root + "/<名字>/SKILL.md，重启服务后就能看到"
	}
	lines := []string{fmt.Sprintf("技能 %d 个，目录 %s", len(skills), root)}
	for _, skill := range skills {
		lines = append(lines, fmt.Sprintf("  /%-24s %s", skill.Name, skill.Desc))
	}
	lines = append(lines,
		"",
		"直接调用：/技能名，后面可以跟补充说明；那份 SKILL.md 会整段摆进这一条提问")
	return strings.Join(lines, "\n")
}

// todoText 是 /todo 的输出。清单是进程级的（和终端界面共用一份），
// 所以浏览器这边看到的就是当前这次任务的进度
func todoText() string {
	items := tool.CurrentTodos()
	if len(items) == 0 {
		return "待办清单是空的：多步任务里模型会自己用 TodoWrite 列出来"
	}
	done, running := tool.TodoCounts(items)
	head := fmt.Sprintf("待办 %d/%d 完成", done, len(items))
	if running > 0 {
		head += fmt.Sprintf(" · %d 项进行中", running)
	}
	return head + "\n" + tool.TodoList(items)
}

// commandNew 在会话所在的工作区开一条新会话，前端收到 session 后切过去
func (m *Manager) commandNew(s *Session) commandReply {
	if s.Running() {
		return textReply("这条会话还在跑，先中断再开新会话")
	}
	ws := m.spaceOf(s)
	if ws == nil {
		return textReply("找不到这条会话所属的工作区，开不了新会话")
	}
	next := m.createIn(ws)
	if next == nil {
		return textReply("工作区 %s 没有可用的目录，开不了新会话", ws.Id)
	}
	// 新会话也落一次盘：不然它在列表里不出现，刷新页面就找不着了
	if err := ws.Store.Save(next.Data()); err != nil {
		return textReply("保存新会话失败：%v", err)
	}
	return commandReply{Kind: replySession, Session: next.Data().Id}
}

// commandResume 处理 /resume：不带参数列会话，带了就切过去。
// 浏览器界面的会话列表本来就在左侧，所以这里的列表是给人看序号用的
func (m *Manager) commandResume(s *Session, arg string) commandReply {
	ws := m.spaceOf(s)
	if ws == nil {
		return textReply("找不到这条会话所属的工作区")
	}
	infos, err := ws.Store.ListInfo()
	if err != nil {
		return textReply("读会话列表失败：%v", err)
	}
	if arg == "" {
		return textReply("%s", resumeListText(infos, s.Data().Id))
	}
	id, err := pickSession(infos, arg)
	if err != nil {
		return textReply("%v", err)
	}
	if s.Running() {
		return textReply("这条会话还在跑，先中断再切走")
	}
	return commandReply{Kind: replySession, Session: id}
}

// resumeListText 是 /resume 不带参数时的会话列表：最近的在最前，
// 当前这条标出来。id 是一串时间戳，认不出是哪一次，所以有标题就把标题跟在后面
func resumeListText(infos []session.Info, curId string) string {
	if len(infos) == 0 {
		return "还没有存过的会话：跑完一轮就会自动存一份"
	}
	lines := []string{fmt.Sprintf("会话列表  共 %d 个，最近的在最前", len(infos))}
	for i, info := range infos {
		mark := "  "
		if info.Id == curId {
			mark = "▸ "
		}
		when := "—"
		if !info.ModTime.IsZero() {
			when = info.ModTime.Format("01-02 15:04")
		}
		line := fmt.Sprintf("%s%d. %-24s %s", mark, i+1, info.Id, when)
		if t := strings.TrimSpace(info.Title); t != "" {
			line += "  " + t
		}
		lines = append(lines, line)
	}
	lines = append(lines, "", "切换：/resume 序号 或 /resume 会话 id；左侧列表也能直接点")
	return strings.Join(lines, "\n")
}

// pickSession 把 /resume 的参数认成一个会话 id：纯数字按列表序号算，其余按 id 精确匹配
func pickSession(infos []session.Info, arg string) (string, error) {
	arg = strings.TrimSpace(arg)
	if n, err := strconv.Atoi(arg); err == nil {
		if n < 1 || n > len(infos) {
			return "", fmt.Errorf("序号 %d 超出范围（1-%d），先 /resume 看列表", n, len(infos))
		}
		return infos[n-1].Id, nil
	}
	for _, info := range infos {
		if info.Id == arg {
			return info.Id, nil
		}
	}
	return "", fmt.Errorf("没找到会话 %q，先 /resume 看列表", arg)
}

// commandSession 是 /session 的输出
func (m *Manager) commandSession(s *Session) commandReply {
	data := s.Data()
	out := fmt.Sprintf("会话 %s，共 %d 条消息", data.Id, len(data.Messages))
	if title := strings.TrimSpace(data.Title); title != "" {
		out += "\n标题 " + title
	}
	if data.WorkDir != "" {
		out += "\n工作目录 " + data.WorkDir
	}
	if ws := m.spaceOf(s); ws != nil {
		out += "\n存到 " + ws.Store.Dir
	}
	return textReply("%s", out)
}

// commandMode 处理 /mode：不带参数列三种模式，带了就切过去并写回配置
func (m *Manager) commandMode(arg string) commandReply {
	if arg == "" {
		cur := parseMode(m.Config().Mode)
		lines := []string{"工作模式  当前 " + cur.Label()}
		for _, mode := range []domain.Mode{domain.ModeReadOnly, domain.ModeAsk, domain.ModeFull} {
			mark := "  "
			if mode == cur {
				mark = "▸ "
			}
			// 不做列对齐：中文名字宽度不等，用 %-5s 补空格反而会错位
			lines = append(lines, fmt.Sprintf("%s%s（%s）— %s",
				mark, mode.Label(), mode.String(), modeHint(mode)))
		}
		lines = append(lines, "", "切换：/mode read · /mode ask · /mode full，顶栏的下拉框也能选")
		return textReply("%s", strings.Join(lines, "\n"))
	}
	mode, err := domain.ParseMode(arg)
	if err != nil {
		return textReply("切换模式失败：%v", err)
	}
	if err := m.setMode(mode); err != nil {
		return textReply("切换模式失败：写回配置时出错：%v", err)
	}
	out := commandReply{Kind: replyText, Text: "工作模式 " + mode.Label() + "  " + modeHint(mode)}
	meta := m.meta()
	out.Meta = &meta
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

// commandQuiet 处理 /quiet：切换"精简显示"并写回配置文件。
//
// 和终端那边是同一个配置项（config.GlobalConfig.Quiet），语义也一样：
// 开着只显示正文，思考过程和工具输出的内容都不显示（工具调用留一行标题）；
// 关掉之后思考用折叠块给出来、工具输出点一下能展开。
// 没配过的情况下默认是开着的，所以浏览器界面默认就是精简的。
func (m *Manager) commandQuiet(arg string) commandReply {
	cfg := m.Config()
	want := !cfg.QuietMode()
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "on", "1", "true", "开":
		want = true
	case "off", "0", "false", "关":
		want = false
	}

	next := cfg
	next.Quiet = &want
	m.setConfig(next)

	out := commandReply{Kind: replyText, Text: "精简显示 已开  " + quietHint(want)}
	if !want {
		out.Text = "精简显示 已关  " + quietHint(want)
	}
	// 写回配置失败不影响本次运行，说一句就行
	if err := config.SaveQuiet(m.cfgPath, want); err != nil {
		out.Text += "\n（没能写回配置：" + err.Error() + "，本次运行仍然生效）"
	}
	meta := m.meta()
	out.Meta = &meta
	return out
}

// quietHint 是一句话说明精简显示开着/关着时界面上有什么
func quietHint(quiet bool) string {
	if quiet {
		return "只显示正文：思考过程和工具输出的内容都不显示，工具调用只留一行标题"
	}
	return "思考过程折叠显示、工具输出点开能看；/quiet 可以切回来"
}

// commandModel 处理 /model：不带参数列出几套，带了就切过去并写回配置。
// add 开新增表单，del 删掉一套
func (m *Manager) commandModel(arg string) commandReply {
	// add / del 是保留字：优先当命令解释，模型名不会叫这两个
	verb, rest := splitVerb(arg)
	switch verb {
	case "add":
		return commandReply{Kind: replyConfig, Config: "new"}
	case "del", "delete":
		return m.commandDeleteModel(rest)
	}

	cfg := m.Config()
	// 拷贝一份再改：AvailableModels 返回的就是配置里那个数组，不能就地改
	models := append([]config.ModelConfig(nil), cfg.AvailableModels()...)
	if len(models) == 0 {
		return textReply("%s", "还没配模型：/config 填一套；/model add 也能新增一套")
	}
	if arg == "" {
		lines := []string{fmt.Sprintf("模型  共 %d 套，当前用 %s", len(models), orDash(cfg.Current))}
		active, _ := config.PickModel(models, cfg.Current)
		for _, mc := range models {
			mark := "  "
			if strings.EqualFold(mc.Label(), active.Label()) && cfg.Current != "" {
				mark = "▸ "
			} else if cfg.Current == "" && strings.EqualFold(mc.Label(), models[0].Label()) {
				mark = "▸ "
			}
			lines = append(lines, fmt.Sprintf("%s%s — %s", mark, mc.Label(), modelHint(mc)))
		}
		lines = append(lines, "", "切换：/model 名字，会写回配置文件；顶栏的模型徽标点开也能直接挑",
			"新增：/model add（名字留空就用模型名）；删除：/model del 名字")
		return textReply("%s", strings.Join(lines, "\n"))
	}

	target, err := m.switchModel(arg)
	if target.Label() == "" {
		return textReply("%v；/model 看看有哪些", err)
	}
	out := commandReply{Kind: replyText, Text: fmt.Sprintf("已切到模型 %s  %s", target.Label(), modelHint(target))}
	if err != nil {
		out.Text += "\n（没能写回配置：" + err.Error() + "，本次运行仍然生效）"
	}
	meta := m.meta()
	out.Meta = &meta
	return out
}

// switchModel 把"当前用哪套"换成 name 指的那套：内存和配置文件一起改，
// 顺手推给开着的其它页面——切模型是全局的，另一个标签页的徽标不能还停在旧名字上。
//
// 认不出 name（或一套都没配）时返回零值配置 + 错误；写回配置文件失败时照常返回
// 切到的那套 + 错误——内存里已经生效了，调用方提示一句就行，不该当成切换失败
func (m *Manager) switchModel(name string) (config.ModelConfig, error) {
	cfg := m.Config()
	// 拷贝一份再改：AvailableModels 返回的就是配置里那个数组，不能就地改
	models := append([]config.ModelConfig(nil), cfg.AvailableModels()...)
	if len(models) == 0 {
		return config.ModelConfig{}, errString("还没配模型：/config 填一套；/model add 也能新增一套")
	}
	target, ok := config.PickModel(models, name)
	if !ok {
		return config.ModelConfig{}, errString(fmt.Sprintf("没有叫 %q 的模型", strings.TrimSpace(name)))
	}
	// 内存里也把名字补齐，和写回文件的那份保持一致
	for i := range models {
		if strings.TrimSpace(models[i].Name) == "" {
			models[i].Name = models[i].Label()
		}
	}
	m.setCurrentModel(models, target.Label())
	m.broadcastMeta()
	if err := config.SaveModels(m.cfgPath, models, target.Label()); err != nil {
		return target, err
	}
	return target, nil
}

// commandDeleteModel 处理 /model del：带名字就回一个要确认的回复，
// 不带名字就把有几套列出来，让人照着写名字
func (m *Manager) commandDeleteModel(name string) commandReply {
	cfg := m.Config()
	models := cfg.AvailableModels()
	if len(models) == 0 {
		return textReply("还没配模型，没得删")
	}
	if len(models) == 1 {
		return textReply("只剩一套模型了，删不掉")
	}
	if name == "" {
		lines := []string{fmt.Sprintf("模型  共 %d 套", len(models))}
		for _, mc := range models {
			lines = append(lines, fmt.Sprintf("  %s — %s", mc.Label(), modelHint(mc)))
		}
		lines = append(lines, "", "删除：/model del 名字")
		return textReply("%s", strings.Join(lines, "\n"))
	}
	target, ok := config.PickModel(models, name)
	if !ok {
		return textReply("没有叫 %q 的模型；/model 看看有哪些", name)
	}
	return commandReply{
		Kind:    replyConfirm,
		Text:    fmt.Sprintf("删掉 %s？", target.Label()),
		Confirm: "del-model:" + target.Label(),
	}
}

// splitVerb 把参数分成"第一个词"和"剩下的"：/model del 名字 里 verb 是 del、rest 是名字
func splitVerb(arg string) (string, string) {
	fields := strings.Fields(arg)
	if len(fields) == 0 {
		return "", ""
	}
	rest := ""
	if len(fields) > 1 {
		rest = strings.Join(fields[1:], " ")
	}
	return strings.ToLower(fields[0]), rest
}

// commandEffort 处理 /effort：不带参数列几档，带了就改成那一档并写回配置
func (m *Manager) commandEffort(arg string) commandReply {
	cfg := m.Config()
	cur, err := cfg.CurrentModel()
	if err != nil {
		return textReply("调整思考强度失败：%v", err)
	}
	if arg == "" {
		now := config.EffortLabel(cur.ReasoningEffort)
		lines := []string{fmt.Sprintf("思考强度  当前 %s（%s）", now, cur.Label())}
		levels := config.EffortLevelsFor(cur.Type)
		for _, level := range levels {
			mark := "  "
			if config.EffortLabel(level) == now {
				mark = "▸ "
			}
			lines = append(lines, fmt.Sprintf("%s%s — %s", mark, config.EffortLabel(level), effortHint(level)))
		}
		deepest := "high"
		if n := len(levels); n > 0 {
			deepest = config.EffortLabel(levels[n-1])
		}
		lines = append(lines, "", fmt.Sprintf("调整：/effort low、/effort %s、/effort 默认，会写回配置文件", deepest))
		return textReply("%s", strings.Join(lines, "\n"))
	}

	level, ok := parseEffortArg(arg, cur.Type)
	if !ok {
		return textReply("认不出思考强度 %q；%s 这套可选 %s", arg, cur.Type,
			strings.Join(config.EffortLabels(cur.Type), " / "))
	}
	next, target, err := config.WithEffort(cfg, level)
	if err != nil {
		return textReply("调整思考强度失败：%v", err)
	}
	m.setConfig(next)
	out := commandReply{
		Kind: replyText,
		Text: fmt.Sprintf("思考强度 %s  %s · %s",
			config.EffortLabel(level), effortHint(level), target.Label()),
	}
	if err := config.SaveModels(m.cfgPath, next.Models, next.Current); err != nil {
		out.Text += "\n（没能写回配置：" + err.Error() + "，本次运行仍然生效）"
	}
	meta := m.meta()
	out.Meta = &meta
	return out
}

// parseEffortArg 把 /effort 后面那截认成档位。"默认/auto/off" 都表示不发这个参数。
//
// "none" 故意不在这里当别名：DeepSeek 真认这一档（明确让它别思考），
// 和"压根不发这个字段、用模型自己的默认"是两回事。把它归到空串，
// 用户想关掉思考反而配成了"看模型心情"
func parseEffortArg(arg, typ string) (string, bool) {
	switch strings.ToLower(strings.TrimSpace(arg)) {
	case "默认", "default", "auto", "off":
		return "", true
	}
	level, err := config.ParseEffort(arg, typ)
	if err != nil {
		return "", false
	}
	return level, true
}

// effortHint 是一句话说明某个档位是什么意思
func effortHint(level string) string {
	switch config.EffortLabel(level) {
	case "none":
		return "明确不思考，直接答（和「默认」不是一个意思：默认是看模型自己的设置）"
	case "minimal":
		return "几乎不思考，最快，只有认这档的接口支持"
	case "low":
		return "想得少一点，快"
	case "medium":
		return "均衡"
	case "high":
		return "想得最久最深，最慢也最费 token"
	case "xhigh":
		return "比 high 再深一档，更慢更费"
	case "ultra":
		return "很深的思考，慢且费 token"
	case "max":
		return "最深的一档，最慢也最费 token"
	default:
		return "不发这个参数，用模型自己的默认"
	}
}

// modelHint 是一句话说明某套模型配的是什么
func modelHint(mc config.ModelConfig) string {
	kind := strings.TrimSpace(mc.Type)
	if kind == "" {
		kind = "DeepSeek"
	}
	out := kind
	if model := strings.TrimSpace(mc.Model); model != "" {
		out += " · " + model
	}
	if url := strings.TrimSpace(mc.ApiUrl); url != "" {
		out += " · " + url
	}
	if effort := strings.TrimSpace(mc.ReasoningEffort); effort != "" {
		out += " · 思考 " + config.EffortLabel(effort)
	}
	return out + " · 上下文 " + domain.FormatTokens(mc.Limit())
}

// orDash 空串显示成破折号
func orDash(s string) string {
	if strings.TrimSpace(s) == "" {
		return "—"
	}
	return s
}

// spaceOf 找出这条会话属于哪个工作区。会话从哪个 store 读出来的就归哪个工作区，
// 按 store 指针比就行——每个工作区一个 store，不会混
func (m *Manager) spaceOf(s *Session) *workspace {
	if s == nil {
		return nil
	}
	for _, ws := range m.spacesSnapshot() {
		if ws.Store == s.store {
			return ws
		}
	}
	return nil
}

// setCurrentModel 把"当前用哪套模型"落到内存里的配置上。
// 已经打开的会话在下一轮开始时才读这份配置，所以不用挨个通知
func (m *Manager) setCurrentModel(models []config.ModelConfig, current string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg.Models = models
	m.cfg.Current = current
}

// setConfig 整份换掉内存里的配置（/effort 这类要动 Models 里某一套的命令用）
func (m *Manager) setConfig(cfg config.GlobalConfig) {
	m.mu.Lock()
	defer m.mu.Unlock()
	m.cfg = cfg
}
