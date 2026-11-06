package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hamusuta/agent-dev/src/config"
	"hamusuta/agent-dev/src/domain"
	"hamusuta/agent-dev/src/session"

	tea "charm.land/bubbletea/v2"
)

// press 把一次按键交给界面，返回处理后的 model。列表那套键都从 handleKey 进来，
// 所以这里也走同一个口子，测的是用户真正按下去的那条路
func press(t *testing.T, m model, key tea.KeyPressMsg) model {
	t.Helper()
	got, _ := m.handleKey(key)
	return got.(model)
}

func keyCode(code rune) tea.KeyPressMsg { return tea.KeyPressMsg{Code: code} }

func keyText(code rune, text string) tea.KeyPressMsg {
	return tea.KeyPressMsg{Code: code, Text: text}
}

// 光标挪到头要绕回来：列表短的时候按一下就到对头，比卡在边界上清楚
func TestPickerMoveWraps(t *testing.T) {
	p := &picker{items: []pickItem{{value: "a"}, {value: "b"}, {value: "c"}}}

	p.move(-1)
	if p.idx != 2 {
		t.Fatalf("从第一项往上该绕到最后，实际 %d", p.idx)
	}
	p.move(1)
	if p.idx != 0 {
		t.Fatalf("从最后一项往下该绕回第一项，实际 %d", p.idx)
	}

	// 空列表挪不动，也不能把下标弄成负数
	empty := &picker{}
	empty.move(3)
	if empty.idx != 0 {
		t.Fatalf("空列表的下标该还是 0，实际 %d", empty.idx)
	}
}

// ↑↓ / j k / PgUp / PgDn / Home / End 都管用，Esc 收起列表
func TestPickerKeysMoveCursor(t *testing.T) {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	m.width = 100
	m.picker = &picker{kind: pickKindMode, items: []pickItem{
		{value: "read"}, {value: "ask"}, {value: "full"},
	}}

	m = press(t, m, keyCode(tea.KeyDown))
	if got := m.picker.idx; got != 1 {
		t.Fatalf("↓ 该挪到第二项，实际 %d", got)
	}
	m = press(t, m, keyText('j', "j"))
	if got := m.picker.idx; got != 2 {
		t.Fatalf("j 也该往下挪，实际 %d", got)
	}
	m = press(t, m, keyCode(tea.KeyDown))
	if got := m.picker.idx; got != 0 {
		t.Fatalf("到底了要绕回第一项，实际 %d", got)
	}
	m = press(t, m, keyText('k', "k"))
	if got := m.picker.idx; got != 2 {
		t.Fatalf("k 该往上挪，实际 %d", got)
	}
	m = press(t, m, keyCode(tea.KeyHome))
	if got := m.picker.idx; got != 0 {
		t.Fatalf("Home 该到第一项，实际 %d", got)
	}
	m = press(t, m, keyCode(tea.KeyEnd))
	if got := m.picker.idx; got != 2 {
		t.Fatalf("End 该到最后一项，实际 %d", got)
	}
	m = press(t, m, keyCode(tea.KeyPgUp))
	if got := m.picker.idx; got != 0 {
		t.Fatalf("PgUp 该翻回第一项，实际 %d", got)
	}

	m = press(t, m, keyCode(tea.KeyEscape))
	if m.picker != nil {
		t.Fatal("Esc 该把列表收起来")
	}
}

// 列表开着时一打字就收起列表，字照常进输入框：想接着写字的人不该被吃掉第一个字
func TestPickerTypingClosesList(t *testing.T) {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	m.width = 100
	m.picker = &picker{kind: pickKindMode, items: []pickItem{{value: "read"}, {value: "ask"}}}

	m = press(t, m, keyText('你', "你"))
	if m.picker != nil {
		t.Fatal("打字该把列表收起来")
	}
	if v := m.input.Value(); v != "你" {
		t.Fatalf("这个字该进输入框，实际 %q", v)
	}
}

// 列表里挑会话和 /resume 会话 id 走的是同一条路（pickApply）：
// 挑完真的切过去，历史也照样重打一遍
func TestPickerApplySessionSwitches(t *testing.T) {
	store := session.NewStore(filepath.Join(t.TempDir(), "sessions"))
	older := domain.NewSession(".")
	older.AppendUser("更早那次")
	if err := store.Save(older); err != nil {
		t.Fatalf("准备会话失败: %v", err)
	}
	newer := domain.NewSession(".")
	newer.AppendUser("刚聊完那次")
	if err := store.Save(newer); err != nil {
		t.Fatalf("准备会话失败: %v", err)
	}

	m := newModel(config.GlobalConfig{}, store, ".", domain.NewSession("."))
	m.width = 100
	got, _ := m.resume("/resume")
	m = got.(model)

	// 选中更早那个（列表里按大小排在后面，直接按 id 对上）
	for i, item := range m.picker.items {
		if item.value == older.Id {
			m.picker.idx = i
		}
	}
	got, _ = m.pickApply()
	after := got.(model)
	if after.picker != nil {
		t.Fatal("挑完该把列表收起来")
	}
	if after.cur.Id != older.Id {
		t.Fatalf("该切到 %s，实际 %s", older.Id, after.cur.Id)
	}
	if len(after.cur.Messages) != 1 || after.cur.Messages[0].Content != "更早那次" {
		t.Fatalf("切过去的会话历史不对: %+v", after.cur.Messages)
	}
}

// /model 的列表挑一套回车：当前模型换了，配置文件也写回了，和手打 /model 名字 一致
func TestPickerApplyModelSwitches(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	m := newModel(config.GlobalConfig{Models: testModels(), Current: "快"}, nil, ".", domain.NewSession("."))
	m.cfgPath = path
	m.width = 120

	got, _ := m.switchModel("/model")
	m = got.(model)
	if m.picker == nil || m.picker.kind != pickKindModel {
		t.Fatal("/model 不带参数该列成能按键挑的列表")
	}

	// 光标一开始在"快"上，往下挪一次就是"稳"
	m = press(t, m, keyCode(tea.KeyDown))
	got, _ = m.pickApply()
	after := got.(model)
	if after.cfg.Current != "稳" {
		t.Fatalf("该切到稳，实际 %q", after.cfg.Current)
	}
	if after.contextLimit != 256000 {
		t.Fatalf("上下文窗口该跟着换，实际 %d", after.contextLimit)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("配置没写回: %v", err)
	}
	if !strings.Contains(string(data), "Current: 稳") {
		t.Fatalf("写回的配置里该是稳:\n%s", data)
	}
}

// /mode 和 /effort 的列表同样是"挑完就走原来那条切换路径"，会写回配置
func TestPickerApplyModeAndEffort(t *testing.T) {
	dir := t.TempDir()
	m := newModel(config.GlobalConfig{Models: testModels(), Current: "快"}, nil, ".", domain.NewSession("."))
	m.cfgPath = filepath.Join(dir, "config.yaml")
	m.width = 120

	got, _ := m.switchMode("/mode")
	m = got.(model)
	if m.picker == nil || m.picker.kind != pickKindMode {
		t.Fatal("/mode 不带参数该列成能按键挑的列表")
	}
	m = press(t, m, keyCode(tea.KeyEnd))
	got, _ = m.pickApply()
	if after := got.(model); after.mode != domain.ModeFull {
		t.Fatalf("挑到最后一项该是完全授权，实际 %v", after.mode)
	}

	got, _ = m.adjustEffort("/effort")
	m = got.(model)
	if m.picker == nil || m.picker.kind != pickKindEffort {
		t.Fatal("/effort 不带参数该列成能按键挑的列表")
	}
	// 找到 low 那一档按下去
	for i, item := range m.picker.items {
		if item.value == "low" {
			m.picker.idx = i
		}
	}
	got, _ = m.pickApply()
	after := got.(model)
	cur, err := after.cfg.CurrentModel()
	if err != nil {
		t.Fatalf("取当前模型失败: %v", err)
	}
	if cur.ReasoningEffort != "low" {
		t.Fatalf("思考强度该改成 low，实际 %q", cur.ReasoningEffort)
	}
}

// 选项比槽位多时只显示光标附近的一段，而且不能超出 activeRows()：
// 帧一高过屏幕，inline 渲染就会叠出几份界面
func TestPickerLinesWindowFitsSlot(t *testing.T) {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	m.width = 100
	m.height = 30

	infos := make([]session.Info, 0, 30)
	for i := 0; i < 30; i++ {
		infos = append(infos, session.Info{
			Id:      fmt.Sprintf("20240102-0000%02d-0001", i),
			Size:    1024,
			ModTime: time.Date(2024, 1, 2, 9, 30, 0, 0, time.UTC),
		})
	}
	m.picker = sessionPicker(infos, nil)
	m.picker.idx = 20

	lines := m.pickerLines()
	if len(lines) > m.activeRows() {
		t.Fatalf("列表占了 %d 行，超过槽位的 %d 行", len(lines), m.activeRows())
	}
	text := stripANSI(strings.Join(lines, "\n"))
	// 选中的那项和底部说明都得看得见
	if !strings.Contains(text, "▸ 21. ") {
		t.Fatalf("选中的那项该在窗口里:\n%s", text)
	}
	if !strings.Contains(text, "共 30 个") {
		t.Fatalf("底部该交代总共有多少:\n%s", text)
	}

	// 光标在第一项时窗口贴着顶上，第 1 项看得见
	m.picker.idx = 0
	if text := stripANSI(strings.Join(m.pickerLines(), "\n")); !strings.Contains(text, "▸ 1. ") {
		t.Fatalf("光标在第一项时它该在窗口里:\n%s", text)
	}
}

// 列表摆在输入框上方那块槽位里，压过待办清单：这一刻它是唯一在等人回答的东西
func TestPickerTakesOverSlot(t *testing.T) {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	m.width = 100
	m.picker = modePicker(domain.ModeAsk)

	lines := m.activeLines()
	if len(lines) == 0 || !strings.Contains(stripANSI(lines[0]), "工作模式") {
		t.Fatalf("槽位里该是列表的标题，实际 %q", lines)
	}
	if !strings.Contains(stripANSI(strings.Join(lines, "\n")), "（当前）") {
		t.Fatalf("当前那档该标出来:\n%v", lines)
	}
}
