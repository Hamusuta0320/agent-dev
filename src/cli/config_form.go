package main

import (
	"context"
	"fmt"
	"path/filepath"
	"strconv"
	"strings"

	"hamusuta/agent-dev/src/config"
	"hamusuta/agent-dev/src/domain"
	"hamusuta/agent-dev/src/provider"

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
	{"类型", "←→ 切换（DeepSeek / OpenAI / MiniMax），都走 OpenAI 兼容协议"},
	{"接口地址", "例如 https://api.deepseek.com，留空就用内置地址"},
	{"密钥", "sk- 开头的那串"},
	{"模型", "例如 deepseek-chat；Ctrl+L 能从接口拉列表挑"},
	{"思考强度", "←→ 切换（默认 / minimal / low / medium / high），默认不发这个参数"},
	{"上下文上限", "模型的上下文窗口（token），可填 128000、128k、1M（1M = 1024k）"},
}

// 表单里各字段的位置。键位和取值的代码都靠它认字段，不写魔法数字
const (
	typeField = iota
	urlField
	keyField
	modelField
	effortField
	limitField
)

// maxFormModels 是模型候选列表最多显示几行，再多就在窗口里滚动
const maxFormModels = 8

// configForm 是 /config 打开的表单。值就存在 inputs 里，非编辑态按打码后的样子渲染，
// 回车聚焦到某个 textinput 才进入编辑。
type configForm struct {
	inputs  []textinput.Model
	cur     int    // 当前选中的字段
	backup  string // 进入编辑前的值，Esc 用它撤销
	editing bool
	err     string // 保存失败的提示

	// models 是从接口拉回来的模型名，pickIdx 是候选里选中的那个（-1 表示列表没展开）。
	// 两者都是"配好地址和密钥就能挑模型"用的，手填模型名照样可以
	models  []string
	pickIdx int
	// wantPick 表示这份列表拉回来之后要直接展开候选（用户按 Ctrl+L 就是来挑的）
	wantPick bool
	// fetching 表示正在拉列表，fetchErr 是上次拉失败的原因
	fetching bool
	fetchErr string
	// fetchSeq 是拉取序号：连按几次 Ctrl+L 时只认最后那次的结果
	fetchSeq int

	// name / typ 是这套模型的名字和协议类型：名字不在表单里改（它是 /model 认的那把钥匙），
	// 类型也先固定，保存时原样带回去
	name string
	typ  string
	// effort 是思考强度档位（config.EffortLevels 里的一项，空串表示不发这个参数）。
	// 和类型一样没有输入框，←→ 在档位之间切
	effort string
}

// modelsFetchedMsg 是拉模型列表的结果回来了。apiUrl / seq 记着这次拉的是哪个地址、
// 第几次拉，回来时表单里的地址已经改过或者又有一次更新的拉取在路上，就不认这份结果
type modelsFetchedMsg struct {
	models []string
	apiUrl string
	seq    int
	err    error
}

func newConfigForm(mc config.ModelConfig) configForm {
	apiUrl := strings.TrimSpace(mc.ApiUrl)
	if apiUrl == "" {
		// 没填过地址就先摆上内置的那个：这几家的地址是固定的，少打一遍
		apiUrl = config.DefaultApiUrl(mc.Type)
	}
	values := make([]string, len(configFields))
	values[urlField] = apiUrl
	values[keyField] = mc.ApiKey
	values[modelField] = mc.Model
	values[limitField] = strconv.Itoa(mc.Limit())
	inputs := make([]textinput.Model, len(configFields))
	for i, f := range configFields {
		in := textinput.New()
		in.Prompt = ""
		in.Placeholder = f.hint
		in.SetValue(values[i])
		// 光标交给终端画，理由和主输入框一样：输入法的候选框跟着终端光标走
		in.SetVirtualCursor(false)
		inputs[i] = in
	}
	return configForm{
		inputs:  inputs,
		name:    mc.Label(),
		typ:     normalizeType(mc.Type),
		effort:  strings.ToLower(strings.TrimSpace(mc.ReasoningEffort)),
		pickIdx: -1,
	}
}

// normalizeType 把类型名归一成内置的写法（大小写不敏感）；没填按 DeepSeek 记，
// 认不出的类型原样留着——有人会填自家转发网关的名字
func normalizeType(typ string) string {
	typ = strings.TrimSpace(typ)
	if typ == "" {
		return "DeepSeek"
	}
	for _, t := range config.ProviderTypes {
		if strings.EqualFold(t, typ) {
			return t
		}
	}
	return typ
}

// value 取某个字段去掉首尾空白后的值。类型和思考强度这两格没有输入框，直接给字段本身
func (f configForm) value(i int) string {
	if i == typeField {
		return strings.TrimSpace(f.typ)
	}
	if i == effortField {
		return strings.TrimSpace(f.effort)
	}
	if i < 0 || i >= len(f.inputs) {
		return ""
	}
	return strings.TrimSpace(f.inputs[i].Value())
}

// modelConfig 把表单里填的东西拼成一套模型配置；接口地址留空就用内置的那个，
// 上下文上限留空就用默认值，填了但不是正整数（也认 128k、1M 这种带单位的写法，
// 按 1024 进制换算）则返回错误，交给界面提示
func (f configForm) modelConfig() (config.ModelConfig, error) {
	mc := config.ModelConfig{
		Name:            f.name,
		Type:            f.typ,
		ApiUrl:          f.value(urlField),
		ApiKey:          f.value(keyField),
		Model:           f.value(modelField),
		ReasoningEffort: f.value(effortField),
	}
	// 类型先只有 OpenAI 兼容这一套，没写就按 DeepSeek 记下来，配置文件里能看出是什么
	mc.Type = normalizeType(mc.Type)
	// 地址留空就用内置的那个：给几家用的地址是固定的，没必要逼着人抄一遍
	if mc.ApiUrl == "" {
		mc.ApiUrl = config.DefaultApiUrl(mc.Type)
	}
	limit := f.value(limitField)
	if limit == "" {
		mc.ContextLimit = config.DefaultContextLimit
		return mc, nil
	}
	n, err := domain.ParseTokens(limit)
	if err != nil {
		return mc, fmt.Errorf("上下文上限填正整数或带单位（128k、1M，1M = 1024k）: %w", err)
	}
	mc.ContextLimit = n
	return mc, nil
}

// fieldLines 渲染表单主体，第一行是标题
func (m model) fieldLines() []string {
	f := &m.form
	out := []string{
		bannerStyle.Render("模型配置") + hintStyle.Render("  "+modelFormTitle(f)+" · "+m.cfgPathLabel()),
	}
	width := configLabelWidth()
	for i, fld := range configFields {
		label := fld.label + strings.Repeat(" ", width-lipgloss.Width(fld.label)+2)
		mark := "  "
		if i == f.cur {
			mark = promptStyle.Render("▸ ")
		}
		var body string
		switch {
		case i == typeField:
			// 类型没有输入框：←→ 在内置类型之间换，地址跟着换成对应的内置地址
			body = f.value(typeField) + hintStyle.Render("  ←→ 切换")
		case i == effortField:
			// 思考强度也没有输入框：←→ 在档位之间换，"默认"表示不发这个参数
			body = config.EffortLabel(f.value(effortField)) + hintStyle.Render("  ←→ 切换")
		case i == f.cur && f.editing:
			body = f.inputs[i].View()
		case f.value(i) == "":
			body = hintStyle.Render("（未填）" + fld.hint)
		case i == keyField:
			body = maskSecret(f.value(i))
		default:
			body = f.value(i)
		}
		out = append(out, mark+hintStyle.Render(label)+body)
	}
	// 模型候选那一块：拉取中的进度、拉失败的提示、或者能挑的列表
	out = append(out, f.listLines()...)
	if f.err != "" {
		out = append(out, errStyle.Render("  "+f.err))
	}
	return out
}

// picking 表示候选列表正展开着：这会儿 ↑↓ 是在列表里挪，而不是切字段
func (f configForm) picking() bool {
	return f.pickIdx >= 0 && f.pickIdx < len(f.models)
}

// listLines 是模型候选那一块，没有可说的就一行不占
func (f configForm) listLines() []string {
	switch {
	case f.fetching:
		return []string{hintStyle.Render("  正在从接口拉取模型列表…")}
	case f.fetchErr != "":
		return []string{errStyle.Render("  拉取模型列表失败：" + f.fetchErr) +
			hintStyle.Render("（可以重试，也可以直接在模型那格手填）")}
	case f.picking():
		return modelPickLines(&f)
	case len(f.models) > 0:
		return []string{hintStyle.Render(fmt.Sprintf(
			"  接口上有 %d 个模型：Ctrl+L 打开列表挑，或者直接在模型那格手填", len(f.models)))}
	}
	return nil
}

// modelPickLines 渲染候选列表：最多 maxFormModels 行，选中的那个始终留在可见范围里
func modelPickLines(f *configForm) []string {
	start, end := pickWindow(len(f.models), f.pickIdx, maxFormModels)
	out := []string{hintStyle.Render(fmt.Sprintf("  可选模型 %d 个：", len(f.models)))}
	for i := start; i < end; i++ {
		mark, style := "  ", hintStyle
		if i == f.pickIdx {
			mark, style = "▸ ", promptStyle
		}
		out = append(out, mark+style.Render(f.models[i]))
	}
	if end < len(f.models) {
		out = append(out, hintStyle.Render(fmt.Sprintf("  …还有 %d 个", len(f.models)-end)))
	}
	return out
}

// pickWindow 算出候选列表该显示哪一段：选中的那一项始终在窗口里，尽量落在中间。
// n 是候选总数，idx 是选中的下标，rows 是窗口最多几行
func pickWindow(n, idx, rows int) (int, int) {
	if rows <= 0 || n <= 0 {
		return 0, 0
	}
	if idx < 0 {
		idx = 0
	}
	if idx >= n {
		idx = n - 1
	}
	start := idx - rows/2
	if start+rows > n {
		start = n - rows
	}
	if start < 0 {
		start = 0
	}
	end := start + rows
	if end > n {
		end = n
	}
	return start, end
}

// configLabelWidth 是表单里最长的那个标签有多宽，几行的值都对齐在它后面
func configLabelWidth() int {
	width := 0
	for _, fld := range configFields {
		if w := lipgloss.Width(fld.label); w > width {
			width = w
		}
	}
	return width
}

// formCursor 是正在编辑的那一格里的光标位置。表单每行是"两格标记 + 标签 + 值"，
// 值前面那几格要加到 textinput 给的偏移上；行号则是标题下面第 cur 行
func (m model) formCursor() *tea.Cursor {
	f := &m.form
	if !f.editing {
		return nil
	}
	c := f.inputs[f.cur].Cursor()
	if c == nil {
		return nil
	}
	// 两格标记 + 标签（补到最长那个再加两格空格）
	x := 2 + configLabelWidth() + 2 + c.X
	// 标题占一行，字段从第二行开始
	return m.placedCursor(c, x, 1+f.cur+c.Y)
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

// openConfig 打开配置表单，值从内存里的 cfg 重新读一遍，显示的就是当前生效的配置。
// 地址和密钥都填着的话顺手去接口拉一次模型列表：多半就是来换模型的，列表先摆好
func (m model) openConfig() (tea.Model, tea.Cmd) {
	m.form = newConfigForm(m.currentModel())
	m.resizeForm()
	m.state = stateConfig
	m.input.Blur()
	return m.pullModels(false)
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
			// 刚改完地址或密钥：顺手去接口拉一遍模型列表，配好了就能直接挑模型
			if (f.cur == urlField || f.cur == keyField) && f.value(f.cur) != strings.TrimSpace(f.backup) {
				f.models = nil
				f.pickIdx = -1
				return m.pullModels(false)
			}
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

	// 候选列表展开着：这会儿 ↑↓ 归列表，Enter 采用，Esc 收起
	if f.picking() {
		switch msg.String() {
		case "up", "k":
			f.pickIdx = (f.pickIdx - 1 + len(f.models)) % len(f.models)
			return m, nil
		case "down", "j":
			f.pickIdx = (f.pickIdx + 1) % len(f.models)
			return m, nil
		case "enter":
			f.chooseModel()
			return m, nil
		case "esc":
			f.pickIdx = -1
			return m, nil
		}
	}

	// 类型那一格没有输入框：←→ 在内置类型之间换，Enter 只说一句怎么换
	if f.cur == typeField {
		switch msg.String() {
		case "left", "h":
			return m.switchType(-1)
		case "right", "l":
			return m.switchType(1)
		case "enter":
			f.err = "类型用 ←→ 切换（DeepSeek / OpenAI / MiniMax）"
			return m, nil
		}
	}

	// 思考强度也一样：←→ 在档位之间换，没有输入框可编辑
	if f.cur == effortField {
		switch msg.String() {
		case "left", "h":
			return m.switchEffort(-1)
		case "right", "l":
			return m.switchEffort(1)
		case "enter":
			f.err = "思考强度用 ←→ 切换（" + strings.Join(config.EffortLabels(), " / ") + "）"
			return m, nil
		}
	}

	switch msg.String() {
	case "up", "shift+tab":
		f.cur = (f.cur - 1 + len(configFields)) % len(configFields)
		f.pickIdx = -1 // 换字段就把候选收起来，不然 ↑↓ 归谁说不清
		f.err = ""
		return m, nil
	case "down", "tab":
		f.cur = (f.cur + 1) % len(configFields)
		f.pickIdx = -1
		f.err = ""
		return m, nil
	case "enter":
		f.backup = f.inputs[f.cur].Value()
		f.inputs[f.cur].Focus()
		f.editing = true
		f.pickIdx = -1
		f.err = ""
		return m, textinput.Blink
	case "ctrl+l":
		return m.fetchModelList()
	case "ctrl+s":
		return m.saveConfig()
	case "esc", "ctrl+c":
		return m.closeConfig()
	}
	return m, nil
}

// switchType 把类型换成内置列表里的前（step = -1）或后（step = 1）一个。
// 地址那格还空着、或者还是旧类型的内置地址时，跟着换成新类型的内置地址——
// 换了类型地址几乎一定也要换；自己手填过的地址不动。
// 地址真被换掉了就去接口拉一遍模型列表，配好密钥的话直接能挑模型
func (m model) switchType(step int) (tea.Model, tea.Cmd) {
	f := &m.form
	old := f.value(typeField)
	idx := 0
	for i, t := range config.ProviderTypes {
		if strings.EqualFold(t, old) {
			idx = i
			break
		}
	}
	next := config.ProviderTypes[(idx+step+len(config.ProviderTypes))%len(config.ProviderTypes)]
	if next == old {
		return m, nil
	}
	f.typ = next
	f.err = ""
	// 类型换了，之前拉的候选列表是别家的模型名，作废
	f.models = nil
	f.pickIdx = -1

	before := f.value(urlField)
	if before == "" || before == config.DefaultApiUrl(old) {
		f.inputs[urlField].SetValue(config.DefaultApiUrl(next))
	}
	if f.value(urlField) == before {
		return m, nil
	}
	return m.pullModels(false)
}

// switchEffort 把思考强度换成档位表里的前（step = -1）或后（step = 1）一个。
// 配置里存了个表外的值（手改过配置文件）时，从第一档开始找，仍然能往下切
func (m model) switchEffort(step int) (tea.Model, tea.Cmd) {
	f := &m.form
	old := f.value(effortField)
	idx := 0
	for i, level := range config.EffortLevels {
		if strings.EqualFold(level, old) {
			idx = i
			break
		}
	}
	next := config.EffortLevels[(idx+step+len(config.EffortLevels))%len(config.EffortLevels)]
	f.effort = next
	f.err = ""
	return m, nil
}


// 地址或密钥还没填时先提示一句——这时候请求也发不出去
func (m model) fetchModelList() (tea.Model, tea.Cmd) {
	f := &m.form
	if f.value(urlField) == "" {
		f.err = "先填接口地址，再用 Ctrl+L 拉模型列表"
		return m, nil
	}
	if f.value(keyField) == "" {
		f.err = "先填密钥，再用 Ctrl+L 拉模型列表"
		return m, nil
	}
	f.err = ""
	f.models = nil
	f.pickIdx = -1
	return m.pullModels(true)
}

// pullModels 在地址和密钥都填着的时候去接口拉一遍模型列表。
// wantPick 为真就拉回来直接展开候选（按 Ctrl+L 就是来挑模型的），
// 否则只把列表记在手里，等用户要的时候再展开（打开表单、改完地址密钥时顺手拉的那次）
func (m model) pullModels(wantPick bool) (tea.Model, tea.Cmd) {
	f := &m.form
	apiUrl, apiKey := f.value(urlField), f.value(keyField)
	if apiUrl == "" || apiKey == "" {
		return m, nil
	}
	f.fetchSeq++
	f.fetching = true
	f.fetchErr = ""
	f.wantPick = wantPick
	return m, fetchModels(apiUrl, apiKey, f.fetchSeq)
}

// fetchModels 起一个 Cmd 去接口拉模型列表。网络请求放进 Cmd 里跑，
// 界面不会跟着卡一下；结果回来是 modelsFetchedMsg。seq 是这次拉取的序号，
// 连按几次只认最后那次的结果
func fetchModels(apiUrl, apiKey string, seq int) tea.Cmd {
	return func() tea.Msg {
		names, err := provider.ListModels(context.Background(), apiUrl, apiKey)
		return modelsFetchedMsg{models: names, apiUrl: apiUrl, seq: seq, err: err}
	}
}

// applyModelList 把拉回来的结果接到表单上。拉的还是改以前的地址、
// 或者这会儿用户已经把地址改掉了、又有一次更新的拉取在路上，这份结果就不认了。
// 失败时只有用户按 Ctrl+L 主动拉的那次才把原因摆出来：顺手拉的那次是后台行为，
// 拿一条错误信息打断改配置的人不值当
func (m model) applyModelList(msg modelsFetchedMsg) model {
	f := &m.form
	if msg.seq != f.fetchSeq {
		return m
	}
	f.fetching = false
	if strings.TrimSpace(msg.apiUrl) != f.value(urlField) {
		return m
	}
	wantPick := f.wantPick
	f.wantPick = false
	if msg.err != nil {
		f.models = nil
		f.pickIdx = -1
		f.fetchErr = ""
		if wantPick {
			f.fetchErr = msg.err.Error()
		}
		return m
	}
	f.fetchErr = ""
	f.models = msg.models
	f.pickIdx = -1
	if wantPick {
		// 光标停在现在填着的那个模型上，没填（或列表里没有）就停在第一个
		f.pickIdx = 0
		for i, name := range f.models {
			if strings.EqualFold(name, f.value(modelField)) {
				f.pickIdx = i
				break
			}
		}
	}
	return m
}

// chooseModel 把候选里选中的那个填进模型那一格
func (f *configForm) chooseModel() {
	if !f.picking() {
		return
	}
	f.inputs[modelField].SetValue(f.models[f.pickIdx])
	f.pickIdx = -1
	f.err = ""
}

// saveConfig 把表单里的值写回配置文件，并同步内存里的 cfg —— 不用重启，下一轮就用新配置。
// 配了好几套模型时改的是当前这套：按名字替换掉 Models 里对应的那一条
func (m model) saveConfig() (tea.Model, tea.Cmd) {
	mc, err := m.form.modelConfig()
	if err != nil {
		m.form.err = err.Error()
		return m, nil
	}
	if mc.ApiUrl == "" || mc.ApiKey == "" || mc.Model == "" {
		m.form.err = "接口地址、密钥、模型都得填"
		return m, nil
	}
	// 还没有名字的新模型就用模型名当名字，/model 里才有得叫
	if strings.TrimSpace(mc.Name) == "" {
		mc.Name = mc.Label()
	}
	models := replaceModel(m.cfg.AvailableModels(), mc)
	if err := config.SaveModels(m.cfgPath, models, mc.Name); err != nil {
		m.form.err = err.Error()
		return m, nil
	}
	m.cfg.Models = models
	m.cfg.Current = mc.Name
	// 老的 Provider 段跟着同步一份，还按老格式读配置的代码看到的也是当前这套
	legacy := mc
	legacy.Name = ""
	_ = m.cfg.Provider.Encode(legacy)

	// 保存后重新生成表单，密钥回到打码显示
	m.form = newConfigForm(mc)
	m.contextLimit = mc.Limit()
	m.state = stateIdle
	m.input.Focus()
	return m, tea.Sequence(
		m.print(bannerStyle.Render("模型配置已保存")+hintStyle.Render("  → "+m.cfgPathLabel())),
		textarea.Blink,
	)
}

// replaceModel 把 mc 放进 models：名字对得上的那条原地换掉，没有就追加。
// 名字是 /model 认的那把钥匙，所以改名等于新增一套
func replaceModel(models []config.ModelConfig, mc config.ModelConfig) []config.ModelConfig {
	out := make([]config.ModelConfig, 0, len(models)+1)
	replaced := false
	for _, old := range models {
		if !replaced && strings.EqualFold(old.Label(), mc.Name) {
			out = append(out, mc)
			replaced = true
			continue
		}
		out = append(out, old)
	}
	if !replaced {
		out = append(out, mc)
	}
	return out
}

// modelFormTitle 是表单标题里那句"哪套模型 · 什么类型"
func modelFormTitle(f *configForm) string {
	name := strings.TrimSpace(f.name)
	if name == "" {
		name = "新模型"
	}
	kind := strings.TrimSpace(f.typ)
	if kind == "" {
		kind = "DeepSeek"
	}
	return name + " · " + kind
}
