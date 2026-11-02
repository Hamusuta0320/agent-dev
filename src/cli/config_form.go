package main

import (
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"hamusuta/agent-dev/src/config"
	"hamusuta/agent-dev/src/domain"

	"charm.land/bubbles/v2/textarea"
	"charm.land/bubbles/v2/textinput"
	tea "charm.land/bubbletea/v2"
	"charm.land/lipgloss/v2"
)

// configField 是 /config 表单里的一行
type configField struct {
	label string
	hint  string
}

// configFields 的顺序要和 configForm 里那几个输入框、以及 saveConfigForm 里取的顺序一致
var configFields = []configField{
	{"接口地址", "例如 https://api.deepseek.com"},
	{"密钥", "sk- 开头的那串"},
	{"模型", "例如 deepseek-chat"},
	{"上下文上限", "模型的上下文窗口（token），可填 128000、128k、1M（1M = 1024k）"},
}

// configForm 是 /config 打开的表单。值就存在 inputs 里，非编辑态按打码后的样子渲染，
// 回车聚焦到某个 textinput 才进入编辑。
type configForm struct {
	inputs  []textinput.Model
	cur     int    // 当前选中的字段
	backup  string // 进入编辑前的值，Esc 用它撤销
	editing bool
	err     string // 保存失败的提示
}

func newConfigForm(dc config.DeepSeekConfig) configForm {
	values := []string{dc.ApiUrl, dc.ApiKey, dc.Model, strconv.Itoa(dc.Limit())}
	inputs := make([]textinput.Model, len(configFields))
	for i, f := range configFields {
		in := textinput.New()
		in.Prompt = ""
		in.Placeholder = f.hint
		in.SetValue(values[i])
		inputs[i] = in
	}
	return configForm{inputs: inputs}
}

// value 取某个字段去掉首尾空白后的值
func (f configForm) value(i int) string {
	if i < 0 || i >= len(f.inputs) {
		return ""
	}
	return strings.TrimSpace(f.inputs[i].Value())
}

// deepSeekConfig 把表单里填的东西拼成配置；上下文上限留空就用默认值，
// 填了但不是正整数（也认 128k、1M 这种带单位的写法，按 1024 进制换算）则返回错误，交给界面提示
func (f configForm) deepSeekConfig() (config.DeepSeekConfig, error) {
	dc := config.DeepSeekConfig{
		Type:   "DeepSeek",
		ApiUrl: f.value(0),
		ApiKey: f.value(1),
		Model:  f.value(2),
	}
	limit := f.value(3)
	if limit == "" {
		dc.ContextLimit = config.DefaultContextLimit
		return dc, nil
	}
	n, err := domain.ParseTokens(limit)
	if err != nil {
		return dc, fmt.Errorf("上下文上限填正整数或带单位（128k、1M，1M = 1024k）: %w", err)
	}
	dc.ContextLimit = n
	return dc, nil
}

// fieldLines 渲染表单主体，第一行是标题
func (m model) fieldLines() []string {
	f := &m.form
	out := []string{
		bannerStyle.Render("模型配置") + hintStyle.Render("  DeepSeek · "+m.cfgPathLabel()),
	}
	width := 0
	for _, fld := range configFields {
		if w := lipgloss.Width(fld.label); w > width {
			width = w
		}
	}
	for i, fld := range configFields {
		label := fld.label + strings.Repeat(" ", width-lipgloss.Width(fld.label)+2)
		mark := "  "
		if i == f.cur {
			mark = promptStyle.Render("▸ ")
		}
		var body string
		switch {
		case i == f.cur && f.editing:
			body = f.inputs[i].View()
		case f.value(i) == "":
			body = hintStyle.Render("（未填）" + fld.hint)
		case i == 1:
			body = maskSecret(f.value(i))
		default:
			body = f.value(i)
		}
		out = append(out, mark+hintStyle.Render(label)+body)
	}
	if f.err != "" {
		out = append(out, errStyle.Render("  "+f.err))
	}
	return out
}

// cfgPathLabel 是展示给用户看的配置文件路径，尽量给绝对路径
func (m model) cfgPathLabel() string {
	if filepath.IsAbs(m.cfgPath) || m.workdir == "" {
		return m.cfgPath
	}
	return filepath.Join(m.workdir, m.cfgPath)
}

// maskSecret 只留头尾几位，密钥不该整串糊在屏幕上
func maskSecret(s string) string {
	r := []rune(s)
	if len(r) == 0 {
		return ""
	}
	if len(r) <= 8 {
		return strings.Repeat("•", len(r))
	}
	return string(r[:4]) + "…" + string(r[len(r)-4:])
}

// openConfig 打开配置表单，值从内存里的 cfg 重新读一遍，显示的就是当前生效的配置
func (m model) openConfig() (tea.Model, tea.Cmd) {
	m.form = newConfigForm(deepSeekConfig(m.cfg))
	m.resizeForm()
	m.state = stateConfig
	m.input.Blur()
	return m, nil
}

// resizeForm 让表单里的输入框跟着终端宽度走，内容长了就横向滚动
func (m *model) resizeForm() {
	w := m.width - 24
	if w < 20 {
		w = 20
	}
	for i := range m.form.inputs {
		m.form.inputs[i].SetWidth(w)
	}
}

// closeConfig 收起表单，回到正常的输入状态
func (m model) closeConfig() (tea.Model, tea.Cmd) {
	m.state = stateIdle
	m.input.Focus()
	return m, textarea.Blink
}

// handleConfigKey 处理表单上的按键：非编辑态选字段/保存/返回，编辑态交给那格的 textinput
func (m model) handleConfigKey(msg tea.KeyPressMsg) (tea.Model, tea.Cmd) {
	f := &m.form

	if f.editing {
		switch msg.String() {
		case "enter":
			f.inputs[f.cur].Blur()
			f.editing = false
			return m, nil
		case "esc":
			f.inputs[f.cur].SetValue(f.backup) // 这一格改坏了就退回原值
			f.inputs[f.cur].Blur()
			f.editing = false
			return m, nil
		}
		var cmd tea.Cmd
		f.inputs[f.cur], cmd = f.inputs[f.cur].Update(msg)
		return m, cmd
	}

	switch msg.String() {
	case "up", "shift+tab":
		f.cur = (f.cur - 1 + len(configFields)) % len(configFields)
		f.err = ""
		return m, nil
	case "down", "tab":
		f.cur = (f.cur + 1) % len(configFields)
		f.err = ""
		return m, nil
	case "enter":
		f.backup = f.inputs[f.cur].Value()
		f.inputs[f.cur].Focus()
		f.editing = true
		f.err = ""
		return m, textinput.Blink
	case "ctrl+s":
		return m.saveConfig()
	case "esc", "ctrl+c":
		return m.closeConfig()
	}
	return m, nil
}

// saveConfig 把表单里的值写回配置文件，并同步内存里的 cfg —— 不用重启，下一轮就用新配置
func (m model) saveConfig() (tea.Model, tea.Cmd) {
	dc, err := m.form.deepSeekConfig()
	if err != nil {
		m.form.err = err.Error()
		return m, nil
	}
	if dc.ApiUrl == "" || dc.ApiKey == "" || dc.Model == "" {
		m.form.err = "接口地址、密钥、模型都得填"
		return m, nil
	}
	if err := config.SaveProvider(m.cfgPath, dc); err != nil {
		m.form.err = err.Error()
		return m, nil
	}
	if err := m.cfg.Provider.Encode(dc); err != nil {
		m.form.err = "已写入文件，但内存配置没更新成功: " + err.Error()
		return m, nil
	}
	// 保存后重新生成表单，密钥回到打码显示
	m.form = newConfigForm(dc)
	m.contextLimit = dc.Limit()
	m.state = stateIdle
	m.input.Focus()
	return m, tea.Sequence(
		tea.Println(bannerStyle.Render("模型配置已保存")+hintStyle.Render("  → "+m.cfgPathLabel())),
		textarea.Blink,
	)
}
