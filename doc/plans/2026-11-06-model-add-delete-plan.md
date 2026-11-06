# 模型配置的新增与删除 实施计划

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** 让 `/model add` 能新增一套模型配置、`/model del` 能删掉一套，`/config` 表单多一格「名字」支持改名；终端和浏览器两边都能用。

**Architecture:** `src/config` 包里把「靠名字匹配猜」的 `ReplaceModel` 换成三个各自只管一件事的动作（`EditModel` / `AddModel` / `DeleteModel`），两个界面都只负责画表单和写文件，列表怎么变一律问 config 包。终端用 `/config` 表单改配置，新增走空白表单、删除走 picker 挑 + 再打一遍命令确认；浏览器用同一个 dialog，新增走 `?new=1` 的空白表单、删除走现有的 `askConfirm` 确认框。

**Tech Stack:** Go 1.x、`gopkg.in/yaml.v3`、bubbletea v2（终端）、原生 `<dialog>` + `fetch`（浏览器，无构建步骤）。

**设计文档：** `doc/specs/2026-11-06-model-add-delete-design.md`

**注意：** 本仓库有大量未提交的改动（`src/cli/ui.go`、`src/config/provider.go` 等）。**不要新建 worktree**——新工作区从 `origin/main` 拉出来会丢掉这些改动，功能要建在它们之上。就地改，就地提交。

---

## 文件地图

| 文件 | 这次改什么 |
|---|---|
| `src/config/provider.go` | 删掉 `ReplaceModel`，加 `EditModel` / `AddModel` / `DeleteModel` 和三个私有小助手 |
| `src/config/models_test.go` | 三个新动作的测试 |
| `src/cli/config_form.go` | `configFields` 多一格「名字」；`configForm` 多 `editLabel`；`newConfigForm` 多一个参数；`saveConfig` 按新增/编辑分派 |
| `src/cli/ui.go` | `model` 结构多 `pendingDelete`；`switchModel` 分流 add/del；新增 `deleteModel` / `removeModel` / `openConfigNew` |
| `src/cli/picker.go` | 多一种 picker「要删哪一套？」 |
| `src/cli/model_test.go` | `/model add` 和 `/model del` 的测试 |
| `src/command/command.go` | `/model` 的说明加上 add / del |
| `src/cli/commands.go` | `/help` 正文里 `/model` 那行 |
| `src/webui/config.go` | `configView` 多 `Mode` / `EditLabel`；`configRequest` 多 `Name` / `New` / `EditLabel`；`saveConfig` 分派；新增 `newConfigForm` / `deleteModel` |
| `src/webui/commands.go` | `commandReply` 多 `Kind: "confirm"` 和 `Confirm`；`commandModel` 分流 add/del |
| `src/webui/api.go` | `GET /api/config?new=1`；`DELETE /api/config/models?name=` |
| `src/webui/web.go` | 注册新路由 |
| `src/webui/static/index.html` | 表单多「名字」输入框、两个按钮 |
| `src/webui/static/app.js` | 名字格、模式切换、确认框、删除 |
| `src/webui/commands_test.go` | 浏览器那半边的测试 |

---

### Task 1: config 层——三个明确动作

**Files:**
- Modify: `src/config/provider.go`（删 `ReplaceModel`，加三个动作）
- Test: `src/config/models_test.go`

- [ ] **Step 1: 写失败的测试**

追加到 `src/config/models_test.go` 末尾：

```go
// 改现有那套：改名是原地换，Current 跟着更新，列表长度不变
func TestEditModelRenames(t *testing.T) {
	models := []ModelConfig{
		{Name: "快", Type: "DeepSeek", ApiUrl: "https://a", ApiKey: "sk-a", Model: "model-a"},
		{Name: "稳", Type: "OpenAI", ApiUrl: "https://b", ApiKey: "sk-b", Model: "model-b"},
	}
	next, current, err := EditModel(models, "快", "快", ModelConfig{
		Name: "飞快", Type: "DeepSeek", ApiUrl: "https://a", ApiKey: "sk-a", Model: "model-a",
	})
	if err != nil {
		t.Fatalf("改名失败: %v", err)
	}
	if len(next) != 2 {
		t.Fatalf("改名不该多出一套，实际 %d 套: %+v", len(next), next)
	}
	if next[0].Name != "飞快" {
		t.Fatalf("名字没换过来: %+v", next[0])
	}
	if current != "飞快" {
		t.Fatalf("Current 该跟着改名，实际 %q", current)
	}

	// 改的不是当前那套：Current 不动
	_, current, err = EditModel(models, "快", "稳", ModelConfig{Name: "稳", Type: "OpenAI", Model: "model-b"})
	if err != nil {
		t.Fatalf("改别的失败: %v", err)
	}
	if current != "快" {
		t.Fatalf("改别的时 Current 不该动，实际 %q", current)
	}

	// 新名字和别的撞了：报错，不覆盖
	if _, _, err := EditModel(models, "快", "快", ModelConfig{Name: "稳", Model: "model-a"}); err == nil {
		t.Fatal("撞名时应该报错")
	}
	// 找不到要改的那套：报错
	if _, _, err := EditModel(models, "快", "没有这个", ModelConfig{Name: "x", Model: "m"}); err == nil {
		t.Fatal("找不到时应该报错")
	}
}

// 新增一套：追加到末尾、Current 不动、撞名报错
func TestAddModel(t *testing.T) {
	models := []ModelConfig{{Name: "快", Model: "model-a"}}
	next, err := AddModel(models, ModelConfig{Name: "稳", Type: "OpenAI", Model: "model-b"})
	if err != nil {
		t.Fatalf("新增失败: %v", err)
	}
	if len(next) != 2 || next[1].Name != "稳" {
		t.Fatalf("新的一套该追加到末尾: %+v", next)
	}
	if next[0].Name != "快" {
		t.Fatalf("原来那套不该被动: %+v", next[0])
	}
	// 名字留空用模型名兜底
	next, err = AddModel(models, ModelConfig{Model: "model-c"})
	if err != nil {
		t.Fatalf("新增失败: %v", err)
	}
	if next[1].Name != "model-c" {
		t.Fatalf("名字留空该用模型名: %+v", next[1])
	}
	// 撞名报错，不覆盖
	if _, err := AddModel(models, ModelConfig{Name: "快", Model: "model-x"}); err == nil {
		t.Fatal("撞名时应该报错")
	}
}

// 删一套：删的是当前那套就改用剩下的第一套，只剩一套删不掉
func TestDeleteModel(t *testing.T) {
	models := []ModelConfig{
		{Name: "快", Model: "model-a"},
		{Name: "稳", Model: "model-b"},
		{Name: "慢", Model: "model-c"},
	}
	next, current, err := DeleteModel(models, "快", "快")
	if err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if len(next) != 2 {
		t.Fatalf("该少一套，实际 %d 套: %+v", len(next), next)
	}
	if current != "稳" {
		t.Fatalf("删掉当前那套该改用剩下的第一套，实际 %q", current)
	}
	// 删的不是当前那套：Current 不动
	_, current, err = DeleteModel(models, "快", "慢")
	if err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	if current != "快" {
		t.Fatalf("Current 不该动，实际 %q", current)
	}
	// 只剩一套：删不掉
	if _, _, err := DeleteModel(models[:1], "快", "快"); err == nil {
		t.Fatal("只剩一套时应该报错")
	}
	// 找不到：报错
	if _, _, err := DeleteModel(models, "快", "没有这个"); err == nil {
		t.Fatal("找不到时应该报错")
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./src/config/ -run 'TestEditModel|TestAddModel|TestDeleteModel'`
Expected: 编译失败，`undefined: EditModel` / `undefined: AddModel` / `undefined: DeleteModel`

- [ ] **Step 3: 实现**

在 `src/config/provider.go` 里把 `ReplaceModel` 整个函数删掉，换成下面这些（放在 `PickModel` 后面）：

```go
// EditModel 改 models 里叫 label 的那套：换成 mc。mc.Name 是新名字，
// 和 label 不同就是改名（原地换，不会多出一套）。
// 返回新的模型列表和新的 Current
func EditModel(models []ModelConfig, current, label string, mc ModelConfig) ([]ModelConfig, string, error) {
	idx := indexOfModel(models, label)
	if idx < 0 {
		return nil, "", fmt.Errorf("没有叫 %q 的模型", label)
	}
	mc = withName(mc)
	// 新名字和别的撞了就说清楚，不默默覆盖人家配好的那套
	for i, other := range models {
		if i == idx {
			continue
		}
		if strings.EqualFold(other.Label(), mc.Name) {
			return nil, "", fmt.Errorf("已经有叫 %q 的模型了", mc.Name)
		}
	}
	out := append([]ModelConfig(nil), models...)
	out[idx] = mc
	next := current
	// 改的正是当前那套时，Current 跟着换成新名字
	if isCurrentIndex(models, current, idx) {
		next = mc.Label()
	}
	return out, next, nil
}

// AddModel 追加一套新的。mc.Name 空就用模型名当名字。
// 名字和现有的撞了报错——不默默覆盖人家配好的那套
func AddModel(models []ModelConfig, mc ModelConfig) ([]ModelConfig, error) {
	mc = withName(mc)
	for _, other := range models {
		if strings.EqualFold(other.Label(), mc.Name) {
			return nil, fmt.Errorf("已经有叫 %q 的模型了", mc.Name)
		}
	}
	return append(append([]ModelConfig(nil), models...), mc), nil
}

// DeleteModel 删掉 models 里叫 label 的那套。删的是当前那套就改用剩下的第一套。
// 只剩一套时删不了：配置里一套都没有的话启动就报错
func DeleteModel(models []ModelConfig, current, label string) ([]ModelConfig, string, error) {
	idx := indexOfModel(models, label)
	if idx < 0 {
		return nil, "", fmt.Errorf("没有叫 %q 的模型", label)
	}
	if len(models) <= 1 {
		return nil, "", fmt.Errorf("只剩一套模型了，删不掉")
	}
	out := make([]ModelConfig, 0, len(models)-1)
	out = append(out, models[:idx]...)
	out = append(out, models[idx+1:]...)
	next := current
	// 删掉的正是当前那套时，改用剩下的第一套
	if isCurrentIndex(models, current, idx) {
		next = out[0].Label()
	}
	return out, next, nil
}

// withName 把名字补齐：没填就用模型名当名字（和 SaveModels 里一个口径）
func withName(mc ModelConfig) ModelConfig {
	if strings.TrimSpace(mc.Name) == "" {
		mc.Name = mc.Label()
	}
	return mc
}

// indexOfModel 找出叫 label 的那套在列表里的位置。名字和模型名都认，
// 和 PickModel 一个口径；认不出返回 -1
func indexOfModel(models []ModelConfig, label string) int {
	label = strings.TrimSpace(label)
	if label == "" {
		return -1
	}
	for i, mc := range models {
		if matchesModel(mc, label) {
			return i
		}
	}
	return -1
}

// isCurrentIndex 判断第 idx 套是不是当前生效的那套。
// Current 留空时按第一套算，和 CurrentModel() 一个口径
func isCurrentIndex(models []ModelConfig, current string, idx int) bool {
	if idx < 0 || idx >= len(models) {
		return false
	}
	if strings.TrimSpace(current) == "" {
		return idx == 0
	}
	return matchesModel(models[idx], current)
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./src/config/`
Expected: 全部 PASS

- [ ] **Step 5: 提交**

```bash
git add src/config/provider.go src/config/models_test.go
git commit -m "feat:config 包加三个明确动作：改（含改名）、新增、删"
```

---

### Task 2: 终端——表单加「名字」格

**Files:**
- Modify: `src/cli/config_form.go`
- Test: `src/cli/config_form_test.go`

- [ ] **Step 1: 写失败的测试**

追加到 `src/cli/config_form_test.go` 末尾：

```go
// 表单多一格「名字」：新增时是空的，编辑时预填正在改的那套
func TestConfigFormNameField(t *testing.T) {
	// 编辑模式：名字格预填那套的名字
	f := newConfigForm(config.ModelConfig{Name: "快", Type: "DeepSeek", Model: "model-a"}, "快")
	if got := f.value(nameField); got != "快" {
		t.Fatalf("编辑时名字该预填: %q", got)
	}
	if f.editLabel != "快" {
		t.Fatalf("编辑模式该记住正在改哪套: %q", f.editLabel)
	}

	// 新增模式：名字空着，类型给默认的，地址给内置的
	blank := newConfigForm(config.ModelConfig{}, "")
	if blank.editLabel != "" {
		t.Fatalf("新增模式不该有正在改的那套: %q", blank.editLabel)
	}
	if got := blank.value(nameField); got != "" {
		t.Fatalf("新增时名字该是空的: %q", got)
	}
	if got := blank.value(typeField); got != "DeepSeek" {
		t.Fatalf("新增时类型该默认 DeepSeek: %q", got)
	}
	if got := blank.value(urlField); got != config.DefaultDeepSeekApiUrl {
		t.Fatalf("新增时地址该给内置的: %q", got)
	}

	// 名字格能填：填了就是这一套的名字
	f.inputs[nameField].SetValue("  新名字  ")
	mc, err := f.modelConfig()
	if err != nil {
		t.Fatalf("填名字不该报错: %v", err)
	}
	if mc.Name != "新名字" {
		t.Fatalf("名字没取到: %q", mc.Name)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./src/cli/ -run TestConfigFormNameField`
Expected: 编译失败，`undefined: nameField`

- [ ] **Step 3: 实现**

`src/cli/config_form.go`：

3a. `configFields` 多一格「名字」，放最前面：

```go
var configFields = []configField{
	{"名字", "这套模型在 /model 列表里的名字，留空就用模型名"},
	{"类型", "←→ 切换（DeepSeek / OpenAI / MiniMax），都走 OpenAI 兼容协议"},
	{"接口地址", "例如 https://api.deepseek.com，留空就用内置地址"},
	{"密钥", "sk- 开头的那串"},
	{"模型", "例如 deepseek-chat；Ctrl+L 能从接口拉列表挑"},
	{"思考强度", "←→ 切换（默认 / minimal / low / medium / high），默认不发这个参数"},
	{"上下文上限", "模型的上下文窗口（token），可填 128000、128k、1M（1M = 1024k）"},
}
```

3b. 字段常量加 `nameField`：

```go
const (
	nameField = iota
	typeField
	urlField
	keyField
	modelField
	effortField
	limitField
)
```

3c. `configForm` 结构体：把 `name string` 换成 `editLabel string`，并加注释：

```go
	// editLabel 是正在改哪一套模型的名字（/model 认的那把钥匙）。
	// 空串 = 新增一套：名字格空着，保存时追加到列表末尾
	editLabel string
```

3d. `newConfigForm` 改成两个参数：

```go
func newConfigForm(mc config.ModelConfig, editLabel string) configForm {
	apiUrl := strings.TrimSpace(mc.ApiUrl)
	if apiUrl == "" {
		// 没填过地址就先摆上内置的那个：这几家的地址是固定的，少打一遍
		apiUrl = config.DefaultApiUrl(mc.Type)
	}
	values := make([]string, len(configFields))
	values[nameField] = editLabel
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
		inputs:    inputs,
		editLabel: editLabel,
		typ:       normalizeType(mc.Type),
		effort:    strings.ToLower(strings.TrimSpace(mc.ReasoningEffort)),
		pickIdx:   -1,
	}
}
```

3e. `value()`：名字格是普通输入框，走默认那条路就行，不用改。

3f. `modelConfig()`：名字从名字格取：

```go
	mc := config.ModelConfig{
		Name:            f.value(nameField),
		Type:            f.typ,
		ApiUrl:          f.value(urlField),
		ApiKey:          f.value(keyField),
		Model:           f.value(modelField),
		ReasoningEffort: f.value(effortField),
	}
```

3g. `modelFormTitle()`：标题用名字格的值，空了叫「新模型」：

```go
func modelFormTitle(f *configForm) string {
	name := strings.TrimSpace(f.value(nameField))
	if name == "" {
		name = "新模型"
	}
	kind := strings.TrimSpace(f.typ)
	if kind == "" {
		kind = "DeepSeek"
	}
	return name + " · " + kind
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./src/cli/`
Expected: 编译失败——`newConfigForm` 的调用点还都是老签名（`src/cli/config_form.go` 的 `openConfig`、`saveConfig`，以及 `src/cli/config_form_test.go`、`src/cli/cli_test.go` 里的几处）。先把所有调用点补上第二个参数，再跑：

- `src/cli/config_form.go` 的 `openConfig`：

```go
func (m model) openConfig() (tea.Model, tea.Cmd) {
	cur := m.currentModel()
	m.form = newConfigForm(cur, cur.Label())
	m.resizeForm()
	m.state = stateConfig
	m.input.Blur()
	return m.pullModels(false)
}
```

- `src/cli/config_form_test.go` 里三处 `newConfigForm(...)`：`newConfigForm(c.mc, c.mc.Label())`、`newConfigForm(config.ModelConfig{Type: "DeepSeek"}, "")` 之类——凡是**编辑**语义的就传 `mc.Label()`，新增语义传 `""`
- `src/cli/cli_test.go:711`：`newConfigForm(config.ModelConfig{ApiUrl: "https://api.deepseek.com", ApiKey: "sk-1", Model: "deepseek-chat"}, "deepseek-chat")`

改完再跑 `go test ./src/cli/`，Expected: 全部 PASS

- [ ] **Step 5: 提交**

```bash
git add src/cli/config_form.go src/cli/config_form_test.go src/cli/cli_test.go
git commit -m "feat:/config 表单加一格名字，新增时留空就用模型名"
```

---

### Task 3: 终端——保存按模式分派

**Files:**
- Modify: `src/cli/config_form.go`（`saveConfig`）
- Test: `src/cli/config_form_test.go`

- [ ] **Step 1: 写失败的测试**

追加到 `src/cli/config_form_test.go` 末尾：

```go
// 存的时候分两种：新增是追加到末尾，改现有的是改那一条（改名不额外多一套）
func TestSaveConfigAddAndRename(t *testing.T) {
	// 新增：列表多一套，当前那套不动
	m := newModel(config.GlobalConfig{Models: testModels(), Current: "快"}, nil, ".", domain.NewSession("."))
	m.cfgPath = filepath.Join(t.TempDir(), "config.yaml")
	m.state = stateConfig
	m.form = newConfigForm(config.ModelConfig{}, "")
	// ←→ 切到 MiniMax：地址得跟着换成该类型的内置地址，
	// 不然会存下一套"类型是 MiniMax、地址却是 DeepSeek"的配置
	got := m
	for i := 0; i < 2; i++ {
		next, _ := got.switchType(1)
		got = next.(model)
	}
	if got.form.value(typeField) != "MiniMax" {
		t.Fatalf("该切到 MiniMax，实际 %q", got.form.value(typeField))
	}
	if got.form.value(urlField) != config.DefaultMiniMaxApiUrl {
		t.Fatalf("地址该跟着换成 MiniMax 的: %q", got.form.value(urlField))
	}
	got.form.inputs[keyField].SetValue("sk-mm")
	got.form.inputs[modelField].SetValue("MiniMax-M3")
	saved, _ := got.saveConfig()
	after := saved.(model)
	if len(after.cfg.Models) != 3 {
		t.Fatalf("新增后该有三套，实际 %d: %+v", len(after.cfg.Models), after.cfg.Models)
	}
	if after.cfg.Models[2].Model != "MiniMax-M3" {
		t.Fatalf("新的一套没追加到末尾: %+v", after.cfg.Models)
	}
	if after.cfg.Models[2].ApiUrl != config.DefaultMiniMaxApiUrl {
		t.Fatalf("新的一套地址不对: %+v", after.cfg.Models[2])
	}
	if after.cfg.Current != "快" {
		t.Fatalf("新增不该动当前那套: %q", after.cfg.Current)
	}
	// 上下文窗口按当前那套算，不该被新增的带跑
	if after.contextLimit != 64000 {
		t.Fatalf("新增不该动上下文窗口: %d", after.contextLimit)
	}

	// 改名：列表长度不变，Current 跟着更新
	m2 := newModel(config.GlobalConfig{Models: testModels(), Current: "快"}, nil, ".", domain.NewSession("."))
	m2.cfgPath = filepath.Join(t.TempDir(), "config.yaml")
	m2.state = stateConfig
	m2.form = newConfigForm(testModels()[0], "快")
	m2.form.inputs[nameField].SetValue("飞快")
	saved2, _ := m2.saveConfig()
	after2 := saved2.(model)
	if len(after2.cfg.Models) != 2 {
		t.Fatalf("改名不该多出一套，实际 %d: %+v", len(after2.cfg.Models), after2.cfg.Models)
	}
	if after2.cfg.Models[0].Name != "飞快" {
		t.Fatalf("名字没换过来: %+v", after2.cfg.Models[0])
	}
	if after2.cfg.Current != "飞快" {
		t.Fatalf("Current 该跟着改名: %q", after2.cfg.Current)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./src/cli/ -run TestSaveConfigAddAndRename`
Expected: FAIL——`新增后该有三套` 那条挂（现在的 `saveConfig` 走 `ReplaceModel`，名字空时会被当成替换或追加，行为不对）

- [ ] **Step 3: 实现**

把 `src/cli/config_form.go` 的 `saveConfig` 换成：

```go
// saveConfig 把表单里的值写回配置文件，并同步内存里的 cfg —— 不用重启，下一轮就用新配置。
// 新增（editLabel 空）就追加到列表末尾；改现有的是改那一条，名字改了就是改名
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

	models := m.cfg.AvailableModels()
	current := m.cfg.Current
	isNew := m.form.editLabel == ""
	if isNew {
		// 新增一套：追加到末尾，当前那套不动
		next, err := config.AddModel(models, mc)
		if err != nil {
			m.form.err = err.Error()
			return m, nil
		}
		models = next
	} else {
		// 改现有那套：mc.Name 换了就是改名（原地换，列表长度不变）
		next, cur, err := config.EditModel(models, current, m.form.editLabel, mc)
		if err != nil {
			m.form.err = err.Error()
			return m, nil
		}
		models, current = next, cur
	}

	if err := config.SaveModels(m.cfgPath, models, current); err != nil {
		m.form.err = err.Error()
		return m, nil
	}
	m.cfg.Models = models
	m.cfg.Current = current
	// 老的 Provider 段跟着同步一份：还按老格式读配置的地方看到的也是当前那套
	m.syncLegacyProvider()

	// 保存后重新生成表单，密钥回到打码显示
	m.form = newConfigForm(mc, mc.Label())
	// 上下文窗口只在改的就是当前那套时跟着换（新增不换）
	if !isNew {
		m.contextLimit = mc.Limit()
	}
	m.state = stateIdle
	m.input.Focus()
	return m, tea.Sequence(
		m.print(bannerStyle.Render("模型配置已保存")+hintStyle.Render("  → "+m.cfgPathLabel())),
		textarea.Blink,
	)
}
```

再加一个 `syncLegacyProvider`（`src/cli/config_form.go` 末尾，或 `src/cli/ui.go` 里——放 `config_form.go` 更贴切）：

```go
// syncLegacyProvider 让内存里老的 Provider 段跟上当前那套：
// 还按老格式读配置的地方看到的也是现在跑的模型
func (m *model) syncLegacyProvider() {
	cur, err := m.cfg.CurrentModel()
	if err != nil {
		return
	}
	cur.Name = ""
	_ = m.cfg.Provider.Encode(cur)
}
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./src/cli/ ./src/config/`
Expected: 全部 PASS

- [ ] **Step 5: 提交**

```bash
git add src/cli/config_form.go src/cli/config_form_test.go
git commit -m "feat:/config 保存分新增和改两路，改名是原地换"
```

---

### Task 4: 终端——`/model add` 与 `/model del`

**Files:**
- Modify: `src/cli/ui.go`（`model` 结构、`switchModel`、新增三个函数）
- Modify: `src/cli/picker.go`（多一种 picker）
- Test: `src/cli/model_test.go`

- [ ] **Step 1: 写失败的测试**

追加到 `src/cli/model_test.go` 末尾：

```go
// /model add 开一张空白表单，存完列表里多一套
func TestModelAddCommand(t *testing.T) {
	m := newModel(config.GlobalConfig{Models: testModels(), Current: "快"}, nil, ".", domain.NewSession("."))
	m.cfgPath = filepath.Join(t.TempDir(), "config.yaml")
	m.state = stateIdle

	got, _ := m.switchModel("/model add")
	after := got.(model)
	if after.state != stateConfig {
		t.Fatal("/model add 该打开配置表单")
	}
	if after.form.editLabel != "" {
		t.Fatalf("新增模式不该有正在改的那套: %q", after.form.editLabel)
	}
	if got := after.form.value(urlField); got != config.DefaultDeepSeekApiUrl {
		t.Fatalf("新增时地址该给内置的: %q", got)
	}
}

// /model del：第一次只问一句，再打一遍同样的命令才真删
func TestModelDeleteCommand(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	m := newModel(config.GlobalConfig{Models: testModels(), Current: "快"}, nil, ".", domain.NewSession("."))
	m.cfgPath = path

	// 第一次：只提示，不删
	got, _ := m.switchModel("/model del 稳")
	after := got.(model)
	if len(after.cfg.Models) != 2 {
		t.Fatalf("第一次不该真删: %+v", after.cfg.Models)
	}
	if after.pendingDelete != "稳" {
		t.Fatalf("该记住要删哪套: %q", after.pendingDelete)
	}

	// 第二次：真删
	got, _ = after.switchModel("/model del 稳")
	after2 := got.(model)
	if len(after2.cfg.Models) != 1 {
		t.Fatalf("该删掉一套: %+v", after2.cfg.Models)
	}
	if after2.cfg.Current != "快" {
		t.Fatalf("删的不是当前那套，Current 不该动: %q", after2.cfg.Current)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("配置没写回: %v", err)
	}
	if strings.Contains(string(data), "model-b") {
		t.Fatalf("删掉的那套还留在配置里:\n%s", data)
	}
}

// 只剩一套时删不掉
func TestModelDeleteKeepsLast(t *testing.T) {
	m := newModel(config.GlobalConfig{Models: testModels()[:1], Current: "快"}, nil, ".", domain.NewSession("."))
	m.cfgPath = filepath.Join(t.TempDir(), "config.yaml")
	got, _ := m.switchModel("/model del 快")
	if len(got.(model).cfg.Models) != 1 {
		t.Fatalf("只剩一套时不该删: %+v", got.(model).cfg.Models)
	}
}
```

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./src/cli/ -run 'TestModelAddCommand|TestModelDelete'`
Expected: 编译失败，`after.pendingDelete undefined`

- [ ] **Step 3: 实现**

3a. `src/cli/ui.go` 的 `model` 结构体里，`form configForm` 那段后面加：

```go
	// pendingDelete 是上一次请求删掉的那套模型（/model del 名字）。
	// 再打一遍同样的命令才真删——删配置没有后悔药
	pendingDelete string
```

3b. `src/cli/ui.go` 的 `switchModel` 改成：

```go
// switchModel 处理 /model：不带参数就列出手上配置好的几套模型，带了就切过去并写回配置，
// 下一轮请求就用新模型，不用重启。add 开新增表单，del 删掉一套
func (m model) switchModel(input string) (tea.Model, tea.Cmd) {
	fields := strings.Fields(input)
	arg := ""
	if len(fields) > 1 {
		arg = strings.Join(fields[1:], " ")
	}
	// add / del 是保留字：优先当命令解释，模型名不会叫这两个
	verb, rest := "", ""
	if arg != "" {
		verb = strings.ToLower(fields[1])
		if len(fields) > 2 {
			rest = strings.Join(fields[2:], " ")
		}
	}
	switch verb {
	case "add":
		return m.openConfigNew()
	case "del", "delete", "rm":
		return m.deleteModel(rest)
	}

	models := m.cfg.AvailableModels()
	if arg == "" {
		if len(models) == 0 {
			return m, m.print(bannerStyle.Render("模型") + "\n" + hintStyle.Render(
				"  还没配模型：/config 填一套；/model add 也能新增一套"))
		}
		// 不带参数就把这几套列成能按键挑的列表，↑↓ 选、Enter 切过去
		m.picker = modelPicker(models, m.cfg.Current)
		return m, nil
	}

	target, ok := pickModel(models, arg)
	if !ok {
		return m, m.print(errStyle.Render(fmt.Sprintf("没有叫 %q 的模型；/model 看看有哪些", arg)))
	}
	// 内存里也把名字补齐，和写回文件的那份保持一致
	for i := range models {
		if strings.TrimSpace(models[i].Name) == "" {
			models[i].Name = models[i].Label()
		}
	}
	m.cfg.Current = target.Label()
	m.cfg.Models = models
	// 上下文窗口跟着换，界面上的"已用 x%"才不会是按上一套模型算的
	m.contextLimit = target.Limit()

	out := bannerStyle.Render("已切到模型 ") + statusStyle.Render(target.Label()) +
		hintStyle.Render("  "+modelHint(target))
	// 写回配置失败不影响本次运行，说明一下就行
	if err := config.SaveModels(m.cfgPath, models, m.cfg.Current); err != nil {
		out += "\n" + hintStyle.Render("（没能写回配置: "+err.Error()+"，本次运行仍然生效）")
	}
	return m, m.print(out)
}
```

3c. `src/cli/ui.go` 里新增三个函数（放在 `switchModel` 后面）：

```go
// deleteModel 删掉一套模型。名字不给就列出来让你挑；
// 名字给了先问一句，再打一遍同样的命令才真删——删配置没有后悔药
func (m model) deleteModel(name string) (tea.Model, tea.Cmd) {
	models := m.cfg.AvailableModels()
	if len(models) <= 1 {
		return m, m.print(errStyle.Render("只剩一套模型了，删不掉"))
	}
	if name == "" {
		// 不带名字：列出来让你挑
		m.picker = modelDeletePicker(models)
		return m, nil
	}
	target, ok := pickModel(models, name)
	if !ok {
		return m, m.print(errStyle.Render(fmt.Sprintf("没有叫 %q 的模型；/model 看看有哪些", name)))
	}
	// 上一次已经问过、这次是同一个名字：真删
	if m.pendingDelete == target.Label() {
		m.pendingDelete = ""
		return m.removeModel(target)
	}
	m.pendingDelete = target.Label()
	return m, m.print(
		bannerStyle.Render("删掉 "+target.Label()+"？") + "\n" +
		hintStyle.Render("  再打一遍 /model del "+target.Label()+" 就删"))
}

// removeModel 把删掉的那套从配置里去掉，写回文件并同步内存。
// 删掉的正是当前那套时改用剩下的第一套，上下文窗口也跟着换
func (m model) removeModel(target config.ModelConfig) (tea.Model, tea.Cmd) {
	models, current, err := config.DeleteModel(m.cfg.AvailableModels(), m.cfg.Current, target.Label())
	if err != nil {
		return m, m.print(errStyle.Render("删除失败: "+err.Error()))
	}
	if err := config.SaveModels(m.cfgPath, models, current); err != nil {
		return m, m.print(errStyle.Render("删除失败: "+err.Error()))
	}
	m.cfg.Models = models
	m.cfg.Current = current
	m.syncLegacyProvider()
	// 上下文窗口按现在真正在跑的那套算
	if cur, err := m.cfg.CurrentModel(); err == nil {
		m.contextLimit = cur.Limit()
	}
	return m, m.print(bannerStyle.Render("已删掉 "+target.Label()) +
		hintStyle.Render(fmt.Sprintf("  还剩 %d 套，当前用 %s", len(models), m.currentModel().Label())))
}
```

3d. `src/cli/config_form.go` 里加 `openConfigNew`（挨着 `openConfig`）：

```go
// openConfigNew 打开"新增一套"的表单：什么都是空的，名字留空就用模型名
func (m model) openConfigNew() (tea.Model, tea.Cmd) {
	m.form = newConfigForm(config.ModelConfig{}, "")
	m.resizeForm()
	m.state = stateConfig
	m.input.Blur()
	return m, nil
}
```

3e. `src/cli/picker.go`：加 `pickKindModelDelete` 和 `modelDeletePicker`

常量那块：

```go
const (
	pickKindSession pickKind = iota
	pickKindModel
	pickKindMode
	pickKindEffort
	pickKindModelDelete
)
```

`verb()` 加一条：

```go
	case pickKindModelDelete:
		return "删"
```

新增函数（挨着 `modelPicker`）：

```go
// modelDeletePicker 是 /model del 不带名字时的那张列表：挑一套删掉
func modelDeletePicker(models []config.ModelConfig) *picker {
	p := &picker{
		kind:  pickKindModelDelete,
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
```

`pickApply()` 加一条：

```go
	case pickKindModelDelete:
		return m.deleteModel(value)
```

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./src/cli/ ./src/config/`
Expected: 全部 PASS

- [ ] **Step 5: 提交**

```bash
git add src/cli/ui.go src/cli/picker.go src/cli/config_form.go src/cli/model_test.go
git commit -m "feat:/model add 新增一套、/model del 删掉一套"
```

---

### Task 5: 命令表与两边的帮助文案

**Files:**
- Modify: `src/command/command.go`
- Modify: `src/cli/commands.go`
- Modify: `src/webui/commands.go`
- Test: `src/command/command_test.go`

- [ ] **Step 1: 改文案**

`src/command/command.go` 里 `/model` 那条：

```go
	{"/model", "看配置好的几套模型，或切到其中一套；/model add 新增一套，/model del 名字删掉一套"},
```

`src/cli/commands.go` 的 `helpText()` 里那行：

```go
		"  /model 不带参数列出配置好的几套（↑↓ 选、Enter 切过去），同样会写回配置文件，重启还是那套",
		"  /model add 新增一套（名字留空就用模型名），/model del 名字 删掉一套（会再问一次）",
```

`src/webui/commands.go` 的 `helpText()` 里那行：

```go
		"  /model 不带参数列出配置好的几套；/model add 新增一套，/model del 名字 删掉一套",
```

- [ ] **Step 2: 跑测试**

Run: `go test ./src/command/ ./src/cli/ ./src/webui/`
Expected: 全部 PASS（`src/command/command_test.go` 里若有断言文案的地方会红，按实际改了哪句对应调整）

- [ ] **Step 3: 提交**

```bash
git add src/command/command.go src/cli/commands.go src/webui/commands.go
git commit -m "docs:/model 的说明加上 add 和 del"
```

---

### Task 6: 浏览器后端——新增 / 改名 / 删除

**Files:**
- Modify: `src/webui/config.go`
- Modify: `src/webui/commands.go`
- Modify: `src/webui/api.go`
- Modify: `src/webui/web.go`
- Test: `src/webui/commands_test.go`

- [ ] **Step 1: 写失败的测试**

追加到 `src/webui/commands_test.go` 末尾：

```go
// /model add 回的是新增模式的表单
func TestCommandModelAdd(t *testing.T) {
	m := newTestManager(t)
	s := m.create()
	reply := run(t, m, s, "/model add")
	if reply.Kind != replyConfig {
		t.Fatalf("/model add 该弹配置表单，实际 %q", reply.Kind)
	}
	if reply.Config != "new" {
		t.Fatalf("/model add 该是新增模式，实际 %q", reply.Config)
	}
}

// 存的时候带 new=true 是新增，不带是改当前那套
func TestSaveConfigNewAndRename(t *testing.T) {
	m := newTestManager(t)
	m.setConfig(config.GlobalConfig{Models: []config.ModelConfig{
		{Name: "快", Type: "DeepSeek", ApiUrl: "https://a", ApiKey: "sk-a", Model: "model-a"},
	}, Current: "快"})

	// 新增：追加到末尾，当前那套不动
	view, err := m.saveConfig(configRequest{
		New: true, Type: "MiniMax", ApiUrl: "https://api.minimaxi.com/v1",
		ApiKey: "sk-mm", Model: "MiniMax-M3",
	})
	if err != nil {
		t.Fatalf("新增失败: %v", err)
	}
	models := m.Config().AvailableModels()
	if len(models) != 2 {
		t.Fatalf("新增后该有两套，实际 %d: %+v", len(models), models)
	}
	if models[1].Model != "MiniMax-M3" {
		t.Fatalf("新的一套没追加到末尾: %+v", models)
	}
	if m.Config().Current != "快" {
		t.Fatalf("新增不该动当前那套: %q", m.Config().Current)
	}
	if view.Mode != "edit" {
		t.Fatalf("存完该回到编辑模式: %q", view.Mode)
	}

	// 改名：列表长度不变
	if _, err := m.saveConfig(configRequest{
		EditLabel: "快", Name: "飞快", Type: "DeepSeek", ApiUrl: "https://a",
		ApiKey: "sk-a", Model: "model-a",
	}); err != nil {
		t.Fatalf("改名失败: %v", err)
	}
	models = m.Config().AvailableModels()
	if len(models) != 2 {
		t.Fatalf("改名不该多出一套，实际 %d: %+v", len(models), models)
	}
	if models[0].Name != "飞快" {
		t.Fatalf("名字没换过来: %+v", models[0])
	}
	if m.Config().Current != "飞快" {
		t.Fatalf("Current 该跟着改名: %q", m.Config().Current)
	}
}

// 删一套：删的是当前那套就改用剩下的第一套
func TestDeleteConfigModel(t *testing.T) {
	m := newTestManager(t)
	m.setConfig(config.GlobalConfig{Models: []config.ModelConfig{
		{Name: "快", Type: "DeepSeek", ApiUrl: "https://a", ApiKey: "sk-a", Model: "model-a"},
		{Name: "稳", Type: "OpenAI", ApiUrl: "https://b", ApiKey: "sk-b", Model: "model-b"},
	}, Current: "快"})

	if _, err := m.deleteModel("快"); err != nil {
		t.Fatalf("删除失败: %v", err)
	}
	models := m.Config().AvailableModels()
	if len(models) != 1 || models[0].Name != "稳" {
		t.Fatalf("该删掉快那套: %+v", models)
	}
	if m.Config().Current != "稳" {
		t.Fatalf("删掉当前那套该改用剩下的第一套: %q", m.Config().Current)
	}
}
```

> `newTestManager(t)` 定义在 `src/webui/web_test.go`（同包，两个测试文件共用），
> 它把 `cfgPath` 指到临时目录，`m.create()` 建一条会话，`run(t, m, s, "...")` 是跑命令的助手。

- [ ] **Step 2: 跑测试确认失败**

Run: `go test ./src/webui/ -run 'TestCommandModelAdd|TestSaveConfigNewAndRename|TestDeleteConfigModel'`
Expected: 编译失败（`reply.Config`、`configRequest.New` / `EditLabel`、`m.deleteModel` 都不存在）

- [ ] **Step 3: 实现**

3a. `src/webui/commands.go`：`commandReply` 多两个字段，`replyConfirm` 常量：

```go
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
```

```go
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
```

3b. `src/webui/config.go`：`configView` 多两个字段

```go
type configView struct {
	Name      string `json:"name"`
	// EditLabel 是正在改的那套的名字，保存时原样带回来（新增时是空串）
	EditLabel string `json:"edit_label"`
	// Mode 是表单模式："edit" 改当前那套，"new" 新增一套
	Mode      string `json:"mode"`
	Type      string `json:"type"`
	ApiUrl    string `json:"api_url"`
	Model     string `json:"model"`
	ApiKeySet bool   `json:"api_key_set"`
	// ContextLimit 是给人填的写法（128k、1M 都认），没配过就是默认值
	ContextLimit string `json:"context_limit"`
	Effort       string `json:"effort"`
	// Types / Efforts 是下拉框的可选项，前端不用自己写死一份
	Types   []string `json:"types"`
	Efforts []string `json:"efforts"`
	CfgPath string   `json:"cfg_path"`
	// DefaultUrls 是各类型的内置接口地址：前端切类型时能直接把地址带出来，
	// 不用用户再抄一遍（终端界面里切类型也是这个行为）
	DefaultUrls map[string]string `json:"default_urls"`
	// Models 是配置文件里已有的几套模型名，前端提示"改的是哪一套"
	Models []string `json:"models"`
}
```

3c. `src/webui/config.go`：`configForm()` 拆成三个函数

```go
// configForm 按当前生效的那套模型生成表单值
func (m *Manager) configForm() configView {
	cfg := m.Config()
	mc, err := cfg.CurrentModel()
	if err != nil {
		// 还没配过：摆一个空表单，类型给默认的那个，地址给内置的
		mc = config.ModelConfig{}
	}
	out := m.baseConfigView(cfg)
	out.Mode = "edit"
	out.EditLabel = mc.Label()
	out.Name = mc.Label()
	out.Type = normalizeType(mc.Type)
	out.ApiUrl = apiUrlOrDefault(mc)
	out.Model = strings.TrimSpace(mc.Model)
	out.ApiKeySet = strings.TrimSpace(mc.ApiKey) != ""
	out.ContextLimit = domain.FormatTokens(mc.Limit())
	out.Effort = config.EffortLabel(mc.ReasoningEffort)
	return out
}

// newConfigForm 生成一张空白表单：新增一套模型时用
func (m *Manager) newConfigForm() configView {
	cfg := m.Config()
	out := m.baseConfigView(cfg)
	out.Mode = "new"
	out.EditLabel = ""
	out.Name = ""
	out.Type = "DeepSeek"
	out.ApiUrl = config.DefaultApiUrl("DeepSeek")
	out.Model = ""
	out.ApiKeySet = false
	out.ContextLimit = domain.FormatTokens(config.DefaultContextLimit)
	out.Effort = config.EffortLabel("")
	return out
}

// baseConfigView 是两种表单都一样的部分：可选项、配置文件路径、已有的模型名
func (m *Manager) baseConfigView(cfg config.GlobalConfig) configView {
	out := configView{
		Types:       config.ProviderTypes,
		Efforts:     config.EffortLabels(),
		CfgPath:     m.cfgPath,
		DefaultUrls: make(map[string]string, len(config.ProviderTypes)),
	}
	for _, typ := range config.ProviderTypes {
		out.DefaultUrls[typ] = config.DefaultApiUrl(typ)
	}
	for _, model := range cfg.AvailableModels() {
		out.Models = append(out.Models, model.Label())
	}
	return out
}

// apiUrlOrDefault 是表单里该摆的接口地址：填过的用填的，没填的用该类型内置的
func apiUrlOrDefault(mc config.ModelConfig) string {
	if url := strings.TrimSpace(mc.ApiUrl); url != "" {
		return url
	}
	return config.DefaultApiUrl(mc.Type)
}
```

3d. `src/webui/config.go`：`configRequest` 多三个字段

```go
type configRequest struct {
	// Name 是这一套的名字；新增时留空就用模型名
	Name string `json:"name"`
	// EditLabel 是正在改的那套的名字（新增时是空串）
	EditLabel string `json:"edit_label"`
	// New 为真表示这是新增一套，不是改现有那套
	New         bool   `json:"new"`
	Type        string `json:"type"`
	ApiUrl      string `json:"api_url"`
	ApiKey      string `json:"api_key"`
	Model       string `json:"model"`
	ContextLimit string `json:"context_limit"`
	Effort      string `json:"effort"`
}
```

3e. `src/webui/config.go`：`saveConfig` 换掉

```go
// saveConfig 把表单里的值写回配置文件，并同步内存里的配置——不用重启，下一轮就用新的。
// New 为真就是新增一套（追加到末尾，当前那套不动）；否则改 EditLabel 指的那套，
// 名字改了就是改名
func (m *Manager) saveConfig(req configRequest) (configView, error) {
	cfg := m.Config()
	models := cfg.AvailableModels()
	current := cfg.Current

	mc := config.ModelConfig{
		Name:            strings.TrimSpace(req.Name),
		Type:            normalizeType(req.Type),
		ApiUrl:          strings.TrimSpace(req.ApiUrl),
		ApiKey:          strings.TrimSpace(req.ApiKey),
		Model:           strings.TrimSpace(req.Model),
		ReasoningEffort: strings.ToLower(strings.TrimSpace(req.Effort)),
	}
	// 密钥留空就是"不改"：前端拿不到明文，这是它唯一能表达"保持原样"的方式。
	// 新增时不沿用当前那套的密钥——那会把别家的密钥悄悄带进新配置里
	if mc.ApiKey == "" && !req.New {
		if cur, err := cfg.CurrentModel(); err == nil {
			mc.ApiKey = strings.TrimSpace(cur.ApiKey)
		}
	}
	// 地址留空就用内置的那个：这几家的地址是固定的，没必要逼着人抄一遍
	if mc.ApiUrl == "" {
		mc.ApiUrl = config.DefaultApiUrl(mc.Type)
	}
	// 思考强度只认表里那几档，写错了直接报，别塞给接口换一个 400 回来
	effort := strings.ToLower(strings.TrimSpace(req.Effort))
	if _, err := config.ParseEffort(effort); err != nil {
		return configView{}, err
	}
	mc.ReasoningEffort = effort
	limit := strings.TrimSpace(req.ContextLimit)
	if limit == "" {
		mc.ContextLimit = config.DefaultContextLimit
	} else {
		n, err := domain.ParseTokens(limit)
		if err != nil {
			return configView{}, err
		}
		mc.ContextLimit = n
	}
	if mc.ApiUrl == "" || mc.ApiKey == "" || mc.Model == "" {
		return configView{}, errFieldsRequired
	}

	if req.New {
		// 新增一套：追加到末尾，当前那套不动
		next, err := config.AddModel(models, mc)
		if err != nil {
			return configView{}, errString(err.Error())
		}
		models = next
	} else {
		// 改现有那套：名字换了就是改名（原地换，列表长度不变）
		label := strings.TrimSpace(req.EditLabel)
		if label == "" {
			if cur, err := cfg.CurrentModel(); err == nil {
				label = cur.Label()
			}
		}
		next, cur, err := config.EditModel(models, current, label, mc)
		if err != nil {
			return configView{}, errString(err.Error())
		}
		models, current = next, cur
	}

	if err := config.SaveModels(m.cfgPath, models, current); err != nil {
		return configView{}, err
	}
	cfg.Models = models
	cfg.Current = current
	// 老的 Provider 段跟着同步一份：还按老格式读配置的地方看到的也是当前这套
	if cur, err := cfg.CurrentModel(); err == nil {
		cur.Name = ""
		_ = cfg.Provider.Encode(cur)
	}
	m.setConfig(cfg)
	return m.configForm(), nil
}

// deleteModel 删掉叫 name 的那套。删的是当前那套就改用剩下的第一套
func (m *Manager) deleteModel(name string) (configView, error) {
	cfg := m.Config()
	models, current, err := config.DeleteModel(cfg.AvailableModels(), cfg.Current, name)
	if err != nil {
		return configView{}, err
	}
	if err := config.SaveModels(m.cfgPath, models, current); err != nil {
		return configView{}, err
	}
	cfg.Models = models
	cfg.Current = current
	if cur, err := cfg.CurrentModel(); err == nil {
		cur.Name = ""
		_ = cfg.Provider.Encode(cur)
	}
	m.setConfig(cfg)
	return m.configForm(), nil
}
```

3f. `src/webui/commands.go`：`commandModel` 分流

```go
// commandModel 处理 /model：不带参数列出几套，带了就切过去并写回配置。
// add 开新增表单，del 删掉一套
func (m *Manager) commandModel(arg string) commandReply {
	// add / del 是保留字：优先当命令解释，模型名不会叫这两个
	verb, rest := splitVerb(arg)
	switch verb {
	case "add":
		return commandReply{Kind: replyConfig, Config: "new"}
	case "del", "delete", "rm":
		return m.commandDeleteModel(rest)
	}

	cfg := m.Config()
	models := cfg.AvailableModels()
	if len(models) == 0 {
		return textReply("%s", "还没配模型：/config 填一套；/model add 也能新增一套")
	}
	...（后面照旧）
}

// commandDeleteModel 处理 /model del：带名字就回一个要确认的回复，
// 不带名字就把有几套列出来，让人照着写名字
func (m *Manager) commandDeleteModel(name string) commandReply {
	cfg := m.Config()
	models := cfg.AvailableModels()
	if len(models) <= 1 {
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
```

3g. `src/webui/api.go`：`GET /api/config` 支持 `?new=1`，加 `DELETE /api/config/models`

```go
func (m *Manager) handleGetConfig() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ?new=1 是"新增一套"：给一张空白表单，不带就是改当前那套
		if r.URL.Query().Get("new") == "1" {
			writeJSON(w, http.StatusOK, m.newConfigForm())
			return
		}
		writeJSON(w, http.StatusOK, m.configForm())
	})
}
```

```go
// handleDeleteConfigModel 删掉一套模型配置：?name=xxx
func (m *Manager) handleDeleteConfigModel() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimSpace(r.URL.Query().Get("name"))
		if name == "" {
			fail(w, http.StatusBadRequest, "没说要删哪一套")
			return
		}
		view, err := m.deleteModel(name)
		if err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		meta := m.meta()
		writeJSON(w, http.StatusOK, map[string]any{"config": view, "meta": meta})
	})
}
```

3h. `src/webui/web.go`：注册路由（在 `mux.Handle("GET /api/config/models", ...)` 附近）

```go
	mux.Handle("DELETE /api/config/models", m.handleDeleteConfigModel())
```

> `src/webui/api.go` 顶上要 import `strings`（已经有了就当我没说）。

- [ ] **Step 4: 跑测试确认通过**

Run: `go test ./src/webui/ ./src/config/`
Expected: 全部 PASS

- [ ] **Step 5: 提交**

```bash
git add src/webui/config.go src/webui/commands.go src/webui/api.go src/webui/web.go src/webui/commands_test.go
git commit -m "feat:浏览器这边也能新增/改名/删除模型配置"
```

---

### Task 7: 浏览器前端——名字格、按钮、确认框

**Files:**
- Modify: `src/webui/static/index.html`
- Modify: `src/webui/static/app.js`

- [ ] **Step 1: 改 HTML**

`src/webui/static/index.html` 的 `#cfg-grid` 里，「类型」那个 label 之前插入「名字」：

```html
      <label for="cfg-name">名字</label>
      <input id="cfg-name" class="form-control form-control-sm" autocomplete="off" spellcheck="false"
             placeholder="留空就用模型名">

      <label for="cfg-type">类型</label>
```

`#cfg-dialog` 的 `.dialog-actions` 里，在「取消」之前加两个按钮：

```html
      <button type="button" id="cfg-new" class="btn btn-outline-secondary btn-sm">＋ 新增一套</button>
      <button type="button" id="cfg-delete" class="btn btn-outline-danger btn-sm">删除这套</button>
```

- [ ] **Step 2: 改 JS**

2a. `src/webui/static/app.js` 的 `el` 表里加三个：

```js
  cfgDialog: $("cfg-dialog"),
  cfgTitle: $("cfg-title"),
  cfgName: $("cfg-name"),
  cfgType: $("cfg-type"),
  ...
  cfgNew: $("cfg-new"),
  cfgDelete: $("cfg-delete"),
```

2b. `state` 里加两个记号：

```js
  // configMode 是配置表单当前的模式："edit" 改当前那套，"new" 新增一套
  configMode: "edit",
  // configEditLabel 是正在改的那套的名字，保存时原样带回去
  configEditLabel: "",
```

2c. `openConfigDialog` 改成带模式：

```js
// openConfigDialog 打开模型配置表单：值从服务端取（密钥只告诉"配过没有"）。
// mode 是 "new" 时要一张空白表单（新增一套）
async function openConfigDialog(mode = "edit") {
  let view;
  try {
    view = await api(mode === "new" ? "/api/config?new=1" : "/api/config");
  } catch (err) {
    notice(`打不开配置表单：${err.message}`, true);
    return;
  }
  state.configMode = mode;
  applyConfigView(view);
  showConfigError("");
  el.cfgModels.hidden = true;
  el.cfgModels.replaceChildren();
  if (!el.cfgDialog.open) el.cfgDialog.showModal();
  el.cfgModel.focus();
}
```

2d. `applyConfigView` 里摆名字、按模式显示按钮：

```js
function applyConfigView(view) {
  state.configDefaults = view.default_urls || {};
  state.configMode = view.mode || "edit";
  state.configEditLabel = view.edit_label || "";
  fillSelect(el.cfgType, view.types || [], (t) => [t, t], view.type);
  // 思考强度的档位是显示名，提交时要换成档位本身（"默认" 就是空串）
  fillSelect(
    el.cfgEffort,
    (view.efforts || []).map((label) => ({ label, value: label === "默认" ? "" : label })),
    (o) => [o.label, o.value],
    view.effort === "默认" ? "" : view.effort,
  );
  el.cfgName.value = view.name || "";
  el.cfgUrl.value = view.api_url || "";
  el.cfgUrl.placeholder = "留空用该类型内置的地址";
  el.cfgKey.value = "";
  el.cfgKey.placeholder = view.api_key_set ? "已配置，留空表示不改" : "填上密钥";
  el.cfgModel.value = view.model || "";
  el.cfgLimit.value = view.context_limit || "";
  el.cfgTitle.textContent = view.name ? `当前这套：${view.name}` : "还没有配置模型";
  // 新增模式下没有"这一套"可删，也不该显示「新增一套」按钮
  el.cfgDelete.hidden = state.configMode !== "edit";
  el.cfgNew.hidden = state.configMode === "new";
  const models = view.models || [];
  el.cfgPath.textContent = [view.cfg_path, models.length > 1 ? `共 ${models.length} 套：${models.join("、")}` : ""]
    .filter(Boolean)
    .join(" · ");
}
```

2e. `saveConfigForm` 带上名字和模式：

```js
async function saveConfigForm() {
  el.cfgSave.disabled = true;
  showConfigError("");
  try {
    const data = await api("/api/config", {
      method: "POST",
      body: JSON.stringify({
        name: el.cfgName.value.trim(),
        edit_label: state.configEditLabel || "",
        new: state.configMode === "new",
        type: el.cfgType.value,
        api_url: el.cfgUrl.value.trim(),
        api_key: el.cfgKey.value.trim(),
        model: el.cfgModel.value.trim(),
        context_limit: el.cfgLimit.value.trim(),
        effort: el.cfgEffort.value,
      }),
    });
    if (data && data.meta) applyMeta(data.meta);
    if (data && data.config) applyConfigView(data.config);
    el.cfgDialog.close();
    notice("模型配置已保存，下一轮就用新的");
  } catch (err) {
    showConfigError(err.message);
  } finally {
    el.cfgSave.disabled = false;
  }
}
```

> `edit_label` 带的是"正在改哪套"：`state.configEditLabel` 在 2d 的 `applyConfigView` 里
> 从服务端给的 `view.edit_label` 存下来。

2f. `runCommand` 里加 `confirm` 分支：

```js
    switch (reply.kind) {
      case "config":
        await openConfigDialog(reply.config === "new" ? "new" : "edit");
        return;
      case "confirm":
        await runConfirm(reply);
        return;
      case "session":
        await openSession(reply.session);
        loadSessions();
        return;
      default:
        if (reply.text) commandOutput(reply.text);
    }
```

2g. 加 `runConfirm`：

```js
// runConfirm 弹确认框：用户点了确定才真的做那件事
async function runConfirm(reply) {
  const answer = await askConfirm({
    title: "确认",
    text: reply.text || "确定要这么做吗？",
    okLabel: "确定",
  });
  if (answer !== "ok") return;
  const action = reply.confirm || "";
  if (action.startsWith("del-model:")) {
    const name = action.slice("del-model:".length);
    try {
      const data = await api(`/api/config/models?name=${encodeURIComponent(name)}`, { method: "DELETE" });
      if (data && data.meta) applyMeta(data.meta);
      if (data && data.config) applyConfigView(data.config);
      notice(`已删掉 ${name}`);
    } catch (err) {
      notice(`删不掉：${err.message}`, true);
    }
  }
}
```

2h. `bindEvents` 里加三个事件：

```js
  el.cfgNew.addEventListener("click", () => openConfigDialog("new"));
  el.cfgDelete.addEventListener("click", deleteCurrentModel);
```

2i. 加 `deleteCurrentModel`：

```js
// deleteCurrentModel 删掉正在改的那套：先弹确认框，点了确定才真删
async function deleteCurrentModel() {
  const name = (state.configEditLabel || el.cfgName.value || "").trim();
  if (!name) {
    showConfigError("还没配过模型，没有能删的");
    return;
  }
  const answer = await askConfirm({
    title: "删除模型配置",
    text: `删掉 ${name}？`,
    okLabel: "删掉",
  });
  if (answer !== "ok") return;
  try {
    const data = await api(`/api/config/models?name=${encodeURIComponent(name)}`, { method: "DELETE" });
    if (data && data.meta) applyMeta(data.meta);
    if (data && data.config) applyConfigView(data.config);
    el.cfgDialog.close();
    notice(`已删掉 ${name}`);
  } catch (err) {
    showConfigError(`删不掉：${err.message}`);
  }
}
```

- [ ] **Step 3: 跑测试**

Run: `go test ./src/webui/`
Expected: 全部 PASS（前端没有 JS 测试，靠 HTTP 层测试 + 手工验证）

- [ ] **Step 4: 手工验一遍**

```bash
go build -o cli.exe ./src/cli
./cli.exe --web 127.0.0.1:8099
```

浏览器打开后依次确认：

1. 顶栏模型徽标点开 → 表单里有「名字」一格，预填当前那套的名字
2. 把名字改成别的 → 保存 → 顶栏徽标跟着变，列表里没多出一套
3. 点「＋ 新增一套」→ 表单变空白（名字空、类型 DeepSeek、地址内置的）→ 填地址密钥模型 → 保存 → 顶栏徽标**没变**（新增不切），路径那一行显示「共 N 套」
4. 输入框打 `/model del` → 回一段列出各套
5. 输入框打 `/model del 某套` → 弹确认框 → 确定 → 那套没了，顶栏跟着换
6. `/model add` → 弹空白表单

- [ ] **Step 5: 提交**

```bash
git add src/webui/static/index.html src/webui/static/app.js
git commit -m "feat:浏览器配置表单加名字格和新增/删除按钮"
```

---

### Task 8: 全量回归

**Files:** 无（只跑检查）

- [ ] **Step 1: 构建**

Run: `go build ./...`
Expected: 无输出（成功）

- [ ] **Step 2: 静态检查**

Run: `go vet ./...`
Expected: 无输出

- [ ] **Step 3: 全量测试**

Run: `go test ./...`
Expected: 全部 PASS

- [ ] **Step 4: 终端里手工过一遍**

```bash
go build -o cli.exe ./src/cli
./cli.exe
```

依次确认：

1. `/model` → 列出各套，↑↓ 挑，Enter 切过去（老行为没坏）
2. `/model add` → 空白表单 → 填完保存 → `/model` 里多一套，当前那套没变
3. `/config` → 名字格预填当前那套的名字 → 改个名字保存 → `/model` 里那套改了名，列表长度没变
4. `/model del` → 列表里挑一套 → 提示「再打一遍」→ 再打一遍 → 真删
5. `/model del 名字`（打一次）→ 只提示，不删

- [ ] **Step 5: 看一眼 `doc/TODO.md`**

`doc/TODO.md` 的「多模型」那一节现在写的是：

```
- [x] 配置可以写多套模型（`config.yaml` 的 `Models` 列表 + `Current` 指定默认用哪套），
      `/model` 列出并切换，切换会写回配置；`/config` 改的是当前这套。
      老配置只写 `Provider` 一段照常能跑，保存时会把 Models / Current / Provider 一起写齐。
      新增 `provider.New(config.ModelConfig)`，Type 认 DeepSeek / OpenAI（都是 OpenAI 兼容接口）。
```

把它改成：

```
- [x] 配置可以写多套模型（`config.yaml` 的 `Models` 列表 + `Current` 指定默认用哪套），
      `/model` 列出并切换，切换会写回配置；`/config` 改的是当前这套。
      老配置只写 `Provider` 一段照常能跑，保存时会把 Models / Current / Provider 一起写齐。
      新增 `provider.New(config.ModelConfig)`，Type 认 DeepSeek / OpenAI（都是 OpenAI 兼容接口）。
- [x] `/model add` 新增一套、`/model del 名字` 删掉一套，不用手改 `config.yaml` 了。
      `/config` 表单多一格「名字」：留空就用模型名，改了就是给这套改名（原地换，不会多出一套）。
      config 层是三个明确动作 `EditModel` / `AddModel` / `DeleteModel`，
      终端和浏览器两边共用（浏览器那边删除走确认框）。
```

- [ ] **Step 6: 提交**

```bash
git add doc/TODO.md
git commit -m "docs:待办清单里补上模型配置的新增和删除"
```

---

## 完成的标准

- `go build ./...`、`go vet ./...`、`go test ./...` 全过
- 终端里 `/model add` 能新增一套（当前那套不动）、`/model del` 能删掉一套（要再打一遍确认）
- `/config` 表单里能改名，改完列表长度不变、顶栏/当前那套跟着更新
- 浏览器里同样能新增（顶栏徽标 → 配置表单 → ＋ 新增一套）和删除（确认框）
- 剩一套时两边都删不掉
