package main

import (
	"fmt"
	"strings"

	"hamusuta/agent-dev/src/config"
	"hamusuta/agent-dev/src/domain"
	"hamusuta/agent-dev/src/session"

	tea "charm.land/bubbletea/v2"
)

// pickKind 说明这块列表在挑什么：挑完要做的事不一样，光有显示的文字不够。
// 它决定两件事——回车之后交给哪条命令去办，以及底部那句按键说明里的动词。
type pickKind int

const (
	pickKindSession pickKind = iota
	pickKindModel
	pickKindMode
	pickKindEffort
	pickKindModelDelete
)

// pickCurMark 贴在"现在用的那一项"后面。光标（▸）说的是"回车就是这个"，
// 两者不是一回事：光标一开始停在当前项上，一按 ↓ 就分开了
const pickCurMark = "（当前）"

// pickItem 是列表里的一行：text 是显示用的，value 是回车之后真正交给命令的那个值
// （会话 id、模型名、模式名、档位名）。分开是因为行里还要带时间、大小、说明这些陪衬，
// 而命令只认那一个值
type pickItem struct {
	value string
	text  string
}

// picker 是"把东西列出来让你按键挑一个"的列表，占输入框上方那块槽位。
// /resume、/model、/mode、/effort 不带参数时打开它：↑↓ 选、Enter 定下来、Esc 收起。
//
// 它不接输入框的字：一打字就当"不想挑了"，键照常交给输入框（见 handlePickKey），
// 所以输入框的焦点感没有被抢走，只是这块槽位暂时被列表占着
type picker struct {
	kind  pickKind
	title string
	items []pickItem
	// idx 是光标所在的那一项，渲染时夹进范围（items 不会变，夹一次就够）
	idx int
}

// pickVerb 是底部说明里"回车做什么"那个词
func (p *picker) verb() string {
	switch p.kind {
	case pickKindSession:
		return "恢复"
	case pickKindMode, pickKindModel:
		return "切换"
	case pickKindModelDelete:
		return "删"
	default:
		return "调整"
	}
}

// sessionPicker 是 /resume 不带参数时的那张会话列表：最近的在最前，当前会话标出来。
// id 是一串时间戳，认不出是哪一次，所以有标题就把标题跟在后面
func sessionPicker(infos []session.Info, cur *domain.Session) *picker {
	p := &picker{
		kind: pickKindSession,
		title: bannerStyle.Render("会话列表") +
			hintStyle.Render(fmt.Sprintf("  共 %d 个，最近的在最前", len(infos))),
	}
	for i, info := range infos {
		when := "—"
		if !info.ModTime.IsZero() {
			when = info.ModTime.Format("01-02 15:04")
		}
		text := fmt.Sprintf("%d. %-24s %s  %s", i+1, info.Id, when, sizeText(info.Size))
		if t := strings.TrimSpace(info.Title); t != "" {
			text += "  " + t
		}
		if cur != nil && info.Id == cur.Id {
			text += pickCurMark
			p.idx = i
		}
		p.items = append(p.items, pickItem{value: info.Id, text: text})
	}
	return p
}

// modelPicker 是 /model 不带参数时的那几套模型：当前这套标出来
func modelPicker(models []config.ModelConfig, current string) *picker {
	p := &picker{
		kind: pickKindModel,
		title: bannerStyle.Render("模型") +
			hintStyle.Render(fmt.Sprintf("  共 %d 套，当前用 %s", len(models), orDash(current))) +
			hintStyle.Render("  /model add 新增一套，/model del 名字 删掉一套"),
	}
	active, _ := pickModel(models, current)
	for _, mc := range models {
		text := fmt.Sprintf("%-20s %s", mc.Label(), modelHint(mc))
		// Current 留空时用第一套，和真正生效的那套对齐
		isCur := strings.EqualFold(mc.Label(), active.Label()) && current != ""
		if current == "" && strings.EqualFold(mc.Label(), models[0].Label()) {
			isCur = true
		}
		if isCur {
			text += pickCurMark
			p.idx = len(p.items)
		}
		p.items = append(p.items, pickItem{value: mc.Label(), text: text})
	}
	return p
}

// modelDeletePicker 是 /model del 不带名字时的那张列表：挑一套删掉
func modelDeletePicker(models []config.ModelConfig) *picker {
	p := &picker{
		kind: pickKindModelDelete,
		title: bannerStyle.Render("要删哪一套？") +
			hintStyle.Render(fmt.Sprintf("  共 %d 套", len(models))),
	}
	for _, mc := range models {
		p.items = append(p.items, pickItem{
			value: mc.Label(),
			text:  fmt.Sprintf("%-20s %s", mc.Label(), modelHint(mc)),
		})
	}
	return p
}

// modePicker 是 /mode 不带参数时的三种工作模式
func modePicker(cur domain.Mode) *picker {
	p := &picker{
		kind: pickKindMode,
		title: bannerStyle.Render("工作模式") +
			hintStyle.Render("  当前 ") + statusStyle.Render(cur.Label()),
	}
	for _, mode := range []domain.Mode{domain.ModeReadOnly, domain.ModeAsk, domain.ModeFull} {
		text := fmt.Sprintf("%s  %-5s %s", mode.Label(), mode.String(), modeHint(mode))
		if mode == cur {
			text += pickCurMark
			p.idx = len(p.items)
		}
		p.items = append(p.items, pickItem{value: mode.String(), text: text})
	}
	return p
}

// effortPicker 是 /effort 不带参数时的几档思考强度，改的是当前生效的那套模型
func effortPicker(cur config.ModelConfig) *picker {
	now := config.EffortLabel(cur.ReasoningEffort)
	p := &picker{
		kind: pickKindEffort,
		title: bannerStyle.Render("思考强度") +
			hintStyle.Render("  当前 "+now+"（"+cur.Label()+"）"),
	}
	for _, level := range config.EffortLevels {
		label := config.EffortLabel(level)
		text := fmt.Sprintf("%-8s %s", label, effortHint(level))
		if label == now {
			text += pickCurMark
			p.idx = len(p.items)
		}
		// value 用显示的名字（默认 / low / high …）：/effort 认得的就是这些
		p.items = append(p.items, pickItem{value: label, text: text})
	}
	return p
}

// curIdx 把光标夹进列表范围。items 建好之后不会变，越界只可能是手算了错
func (p *picker) curIdx() int {
	if p.idx < 0 || p.idx >= len(p.items) {
		return 0
	}
	return p.idx
}

// move 把光标挪 delta 项，两头绕回来：列表短的时候按一下就到对头，
// 比"卡在边界上再按也没反应"清楚
func (p *picker) move(delta int) {
	n := len(p.items)
	if n == 0 {
		return
	}
	p.idx = ((p.curIdx()+delta)%n + n) % n
}

// handlePickKey 处理列表打开时的按键：只管挪光标和"定下来 / 收起来"，
// 别的键一律先收起列表再按原样往下走——用户想接着打字就是不想挑了，
// 把那个字吃掉才叫难受
func (m model) handlePickKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	p := m.picker
	n := len(p.items)
	page := max(m.pickWindow(), 1)

	switch msg.Code {
	case tea.KeyUp:
		p.move(-1)
		return m, nil
	case tea.KeyDown:
		p.move(1)
		return m, nil
	case tea.KeyPgUp:
		p.move(-page)
		return m, nil
	case tea.KeyPgDown:
		p.move(page)
		return m, nil
	case tea.KeyHome:
		p.idx = 0
		return m, nil
	case tea.KeyEnd:
		p.idx = n - 1
		return m, nil
	case tea.KeyEnter:
		return m.pickApply()
	case tea.KeyEscape:
		m.picker = nil
		return m, nil
	}
	switch msg.String() {
	case "k", "ctrl+p":
		p.move(-1)
		return m, nil
	case "j", "ctrl+n":
		p.move(1)
		return m, nil
	}

	m.picker = nil
	return m.handleKey(msg)
}

// pickApply 把选中的那一项交给对应的命令去办。
// 列表里挑和手打命令走的是同一条路（/resume id、/model 名字…），
// 所以提示、写回配置、换会话之后重打历史，全都和以前一致
func (m model) pickApply() (tea.Model, tea.Cmd) {
	p := m.picker
	m.picker = nil
	if p == nil || len(p.items) == 0 {
		return m, nil
	}
	value := p.items[p.curIdx()].value
	switch p.kind {
	case pickKindSession:
		return m.resume("/resume " + value)
	case pickKindModel:
		return m.switchModel("/model " + value)
	case pickKindMode:
		return m.switchMode("/mode " + value)
	case pickKindEffort:
		return m.adjustEffort("/effort " + value)
	case pickKindModelDelete:
		return m.deleteModel(value)
	}
	return m, nil
}

// pickWindow 是列表这一帧能显示几项：槽位里除了选项还要放标题和按键说明两行
func (m model) pickWindow() int {
	rows := m.activeRows()
	if rows < 3 {
		// 终端矮到只剩几行时，标题和说明挤不掉，能看见一项算一项
		rows = 3
	}
	return rows - 2
}

// pickerLines 是列表本身，摆进输入框上方那块槽位：第一行标题，最后一行怎么按键，
// 中间是选项。选项比窗口多时只显示光标附近的一段，省得选中项被挤出屏幕
func (m model) pickerLines() []string {
	p := m.picker
	if p == nil || len(p.items) == 0 {
		return nil
	}
	cur := p.curIdx()
	window := m.pickWindow()
	start, end := 0, len(p.items)
	if len(p.items) > window {
		start = cur - window/2
		if start < 0 {
			start = 0
		}
		if start > len(p.items)-window {
			start = len(p.items) - window
		}
		end = start + window
	}

	lines := []string{clipLine(p.title, m.width-1)}
	for i := start; i < end; i++ {
		text := p.items[i].text
		if i == cur {
			lines = append(lines, clipLine(promptStyle.Render("▸ "+text), m.width-1))
			continue
		}
		lines = append(lines, clipLine(hintStyle.Render("  "+text), m.width-1))
	}
	foot := fmt.Sprintf("  ↑↓ 选 · Enter %s · Esc 收起", p.verb())
	if start > 0 || end < len(p.items) {
		foot = fmt.Sprintf("  ↑↓ 选（%d-%d / 共 %d 个）· Enter %s · Esc 收起",
			start+1, end, len(p.items), p.verb())
	}
	return append(lines, clipLine(hintStyle.Render(foot), m.width-1))
}
