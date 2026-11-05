package main

import (
	"fmt"
	"strings"

	tea "charm.land/bubbletea/v2"
)

// queuedPrompt 是运行中排上队的下一条提问：原话和随它一起附的图。
//
// 出队时把它们原样放回输入框、再走一遍 submit，所以排队里写 /命令、点技能名
// 都和在空闲时敲一样生效，不用在这儿再认一遍命令。
type queuedPrompt struct {
	text   string
	images []imageAttachment
}

// maxQueueRows 是排队列表最多列几条，再多就折成一句"还有 N 条"。
// 这块内容挤在输入框上方那几行槽位里，铺太长会把预览和状态顶掉
const maxQueueRows = 3

// takesQueueArrows 判断裸 ↑↓（没按 Alt）该不该归排队用。
//
// 真正的主入口是 Alt+↑ / Alt+↓，那两个键不占输入框。这里管的是没按 Alt 的情况：
// 只有排队里已经有选中项（说明用户正按着上下键一条条挑），或者输入框空着
// （空着时 ↑↓ 在输入框里本来就没东西可挪）才接过来。
// 输入框里只要有字就让给输入框：多行输入要靠 ↑↓ 挪光标，
// 为了排队把这两个键抢走，等于让人写着写着就挪不动了。
//
// 留着这条路是因为有的终端会把 Alt+方向键整个接走，那时候功能还能用。
func (m model) takesQueueArrows() bool {
	if m.state != stateRunning || len(m.queue) == 0 {
		return false
	}
	if m.queueIdx >= 0 {
		return true
	}
	// 空白也算空：只敲了几个空格或者空行，输入框里本来就没东西可挪光标
	return strings.TrimSpace(m.input.Value()) == ""
}

// moveQueueSel 上下挪一格选中项，delta 为 -1（上）或 +1（下）。
//
// 往上：没选中时从最后一条进，否则往上挪一格，到头绕回最后一条。
// 往下：没选中时不动（不进选择，免得 ↓ 在输入框里随手一按就跳走）；
// 选中最后一条时越过它——那就是"不选了"，键盘还给输入框。
func (m *model) moveQueueSel(delta int) {
	if len(m.queue) == 0 {
		return
	}
	if delta < 0 {
		if m.queueIdx < 0 {
			m.queueIdx = len(m.queue) - 1
		} else {
			m.queueIdx = (m.queueIdx - 1 + len(m.queue)) % len(m.queue)
		}
		return
	}
	if m.queueIdx < 0 {
		return
	}
	m.queueIdx++
	if m.queueIdx >= len(m.queue) {
		m.queueIdx = -1
	}
}

// enqueue 把输入框里这一条排到队尾，返回是否真的排上了。
// 一个字没写、也没附图时返回 false，让调用方知道没东西可排
func (m *model) enqueue() bool {
	text := strings.TrimSpace(m.input.Value())
	if text == "" && len(m.attachments) == 0 {
		return false
	}
	m.queue = append(m.queue, queuedPrompt{text: text, images: m.attachments})
	// 图片跟着这一条走了，别再挂在下一条上；输入框也要腾空，好接着排下一条
	m.attachments = nil
	m.queueIdx = -1
	m.input.Reset()
	m.suggestIdx = 0
	return true
}

// dequeue 把队头那一条放回输入框和附图，交给 submit 走正常流程。
// 队列空着时什么也不做（返回原样的 model，不会凭空发一条空提问）
func (m model) dequeue() (tea.Model, tea.Cmd) {
	if len(m.queue) == 0 {
		return m, nil
	}
	next := m.queue[0]
	m.queue = m.queue[1:]
	m.queueIdx = -1
	m.input.SetValue(next.text)
	m.attachments = next.images
	return m.submit()
}

// queueTarget 是这次编辑/删除要动的那一条：选中了就是它，没选中就是最后一条。
// 返回 -1 表示队列空着
func (m model) queueTarget() int {
	if len(m.queue) == 0 {
		return -1
	}
	if m.queueIdx >= 0 && m.queueIdx < len(m.queue) {
		return m.queueIdx
	}
	return len(m.queue) - 1
}

// editQueued 把排队里的那一条取回输入框改：从队里拿掉，原话和附图放回输入框。
// 输入框里已经有东西时不覆盖——那会把用户正在写的内容弄丢，先说一句让他自己处理
func (m model) editQueued() (tea.Model, tea.Cmd) {
	idx := m.queueTarget()
	if idx < 0 {
		return m, nil
	}
	if strings.TrimSpace(m.input.Value()) != "" || len(m.attachments) > 0 {
		return m, m.print(hintStyle.Render("输入框里还有内容，发出去或清空之后再取回排队的那条"))
	}
	q := m.queue[idx]
	// 三索引切片：截断时不和原底层数组共用尾巴，免得别处的副本把这条又带回来
	m.queue = append(m.queue[:idx:idx], m.queue[idx+1:]...)
	m.queueIdx = -1
	m.input.SetValue(q.text)
	m.attachments = q.images
	return m, nil
}

// removeQueued 删掉排队里的那一条，并在历史区留一行——队列行本身会消失，
// 不说一句的话看不出是删掉了还是发出去了
func (m model) removeQueued() (tea.Model, tea.Cmd) {
	idx := m.queueTarget()
	if idx < 0 {
		return m, nil
	}
	label := queueLabel(m.queue[idx])
	m.queue = append(m.queue[:idx:idx], m.queue[idx+1:]...)
	// 删完选中项往前挪一格：连着按删除能一条条清掉；删空了就复位
	if m.queueIdx >= len(m.queue) {
		m.queueIdx = len(m.queue) - 1
	}
	return m, m.print(hintStyle.Render("已删掉排队消息 " + label))
}

// queueLines 是输入框上方那几行排队提示：几条在等、怎么改怎么删。
// 一条都没有就一行不占，那块槽位让给状态和预览
func (m model) queueLines() []string {
	if len(m.queue) == 0 {
		return nil
	}
	// 选中的下标可能因为删除、切会话已经失效，渲染时夹回范围内，
	// 否则会算出一个空窗口（标题写着"1 条"，下面一条都不列）
	sel := m.queueIdx
	if sel < 0 || sel >= len(m.queue) {
		sel = -1
	}
	// 窗口跟着选中项走：选中的那条必须露头，否则按了 Alt+E 看不出动的是哪条
	start := 0
	if sel >= maxQueueRows {
		start = sel - maxQueueRows + 1
	}
	end := min(start+maxQueueRows, len(m.queue))

	limit := m.width - 1
	title := hintStyle.Render("排队 ") + statusStyle.Render(fmt.Sprint(len(m.queue))) +
		hintStyle.Render(" 条")
	// 窗口不是全部时在标题里交代落在第几条到第几条，省得以为就排了这几条
	if start > 0 || end < len(m.queue) {
		title += hintStyle.Render(fmt.Sprintf("（%d-%d）", start+1, end))
	}
	title += hintStyle.Render(" · Alt+↑↓ 选 · Alt+E 改 · Del 删")

	out := []string{clipLine(title, limit)}
	for i := start; i < end; i++ {
		mark, style := "  ", hintStyle
		if i == sel {
			mark, style = "▸ ", promptStyle
		}
		out = append(out, clipLine(style.Render(fmt.Sprintf("%s%d. %s", mark, i+1, queueLabel(m.queue[i]))), limit))
	}
	return out
}

// queueLabel 是一条排队消息在列表里怎么显示：压成一行，附了图就说一声。
// 纯图片的提问没有文字，也不能显示成空行
func queueLabel(q queuedPrompt) string {
	text := strings.TrimSpace(q.text)
	if line, _, ok := strings.Cut(text, "\n"); ok {
		text = strings.TrimSpace(line) + " …"
	}
	if text == "" {
		text = "(只有图片)"
	}
	if n := len(q.images); n > 0 {
		text += fmt.Sprintf("（+%d 图）", n)
	}
	return text
}
