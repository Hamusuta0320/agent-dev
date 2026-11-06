package webui

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"hamusuta/agent-dev/src/command"
	"hamusuta/agent-dev/src/config"
	"hamusuta/agent-dev/src/domain"
)

// run 直接调 runCommand，绕过 HTTP：这些用例验的是命令本身
func run(t *testing.T, m *Manager, s *Session, input string) commandReply {
	t.Helper()
	return m.runCommand(s, input)
}

// /help 列出全部命令，和终端界面用的是同一张表
func TestCommandHelp(t *testing.T) {
	m := newTestManager(t)
	s := m.create()
	reply := run(t, m, s, "/help")

	if reply.Kind != replyText {
		t.Fatalf("/help 该回一段文本，实际 %q", reply.Kind)
	}
	for _, want := range []string{"/config", "/model", "/resume", "Enter 发送"} {
		if !strings.Contains(reply.Text, want) {
			t.Fatalf("/help 里少了 %q:\n%s", want, reply.Text)
		}
	}
}

// 不认识的 /命令 要给出相近的，而不是当成提问
func TestCommandUnknown(t *testing.T) {
	m := newTestManager(t)
	s := m.create()
	reply := run(t, m, s, "/mod")

	if !strings.Contains(reply.Text, "没有叫 /mod 的命令") {
		t.Fatalf("该说没有这条命令:\n%s", reply.Text)
	}
	if !strings.Contains(reply.Text, "/model") {
		t.Fatalf("该列出相近的 /model:\n%s", reply.Text)
	}
}

// /mode 不带参数列出三种模式，带了就切换并写回配置，回包带上新的 meta
func TestCommandMode(t *testing.T) {
	m := newTestManager(t)
	s := m.create()

	list := run(t, m, s, "/mode")
	for _, want := range []string{"工作模式", "只读", "编辑需授权", "完全授权", "▸"} {
		if !strings.Contains(list.Text, want) {
			t.Fatalf("/mode 列表里少了 %q:\n%s", want, list.Text)
		}
	}

	reply := run(t, m, s, "/mode read")
	if reply.Meta == nil || reply.Meta.Mode != "read" {
		t.Fatalf("切完该带上新的 meta，实际 %+v", reply.Meta)
	}
	if got := s.Mode(); got != domain.ModeReadOnly {
		t.Fatalf("会话的模式没跟着切: %v", got)
	}
	data, err := os.ReadFile(m.cfgPath)
	if err != nil {
		t.Fatalf("配置没写回: %v", err)
	}
	if !strings.Contains(string(data), "Mode: read") {
		t.Fatalf("写回的配置不对:\n%s", data)
	}
}

// /model 列出手上的几套，按名字切过去并写回配置
func TestCommandModel(t *testing.T) {
	m := newTestManager(t)
	m.setConfig(config.GlobalConfig{
		Models: []config.ModelConfig{
			{Name: "快", Type: "DeepSeek", ApiUrl: "https://a", ApiKey: "k1", Model: "model-a", ContextLimit: 64000},
			{Name: "稳", Type: "OpenAI", ApiUrl: "https://b", ApiKey: "k2", Model: "model-b", ContextLimit: 256000},
		},
		Current: "快",
		Mode:    "full",
	})
	s := m.create()

	list := run(t, m, s, "/model")
	for _, want := range []string{"共 2 套", "当前用 快", "model-b"} {
		if !strings.Contains(list.Text, want) {
			t.Fatalf("/model 列表里少了 %q:\n%s", want, list.Text)
		}
	}

	reply := run(t, m, s, "/model 稳")
	if reply.Meta == nil || reply.Meta.Model != "稳" {
		t.Fatalf("切完该带上新的 meta，实际 %+v", reply.Meta)
	}
	if reply.Meta.ContextLimit != 256000 {
		t.Fatalf("上下文窗口该跟着换，实际 %d", reply.Meta.ContextLimit)
	}
	data, err := os.ReadFile(m.cfgPath)
	if err != nil {
		t.Fatalf("配置没写回: %v", err)
	}
	if !strings.Contains(string(data), "Current: 稳") {
		t.Fatalf("写回的配置里该是稳:\n%s", data)
	}

	// 认不出的名字不切
	if got := run(t, m, s, "/model 不存在"); !strings.Contains(got.Text, "没有叫") {
		t.Fatalf("认不出的模型该提示一句，实际:\n%s", got.Text)
	}
	if cur := m.Config().Current; cur != "稳" {
		t.Fatalf("认不出的模型不该改掉当前那套: %q", cur)
	}
}

// /effort 改的是当前这套模型的思考强度，会写回配置
func TestCommandEffort(t *testing.T) {
	m := newTestManager(t)
	m.setConfig(config.GlobalConfig{
		Models: []config.ModelConfig{{
			Name: "测", Type: "DeepSeek", ApiUrl: "https://a", ApiKey: "k", Model: "m", ReasoningEffort: "low",
		}},
		Current: "测",
		Mode:    "full",
	})
	s := m.create()

	list := run(t, m, s, "/effort")
	for _, want := range []string{"当前 low", "▸ low", "minimal", "默认"} {
		if !strings.Contains(list.Text, want) {
			t.Fatalf("/effort 列表里少了 %q:\n%s", want, list.Text)
		}
	}

	reply := run(t, m, s, "/effort high")
	if reply.Meta == nil || reply.Meta.Effort != "high" {
		t.Fatalf("改完该带上新的 meta，实际 %+v", reply.Meta)
	}
	cur, err := m.Config().CurrentModel()
	if err != nil {
		t.Fatalf("取当前模型失败: %v", err)
	}
	if cur.ReasoningEffort != "high" {
		t.Fatalf("内存里的思考强度没改: %q", cur.ReasoningEffort)
	}
	data, err := os.ReadFile(m.cfgPath)
	if err != nil {
		t.Fatalf("配置没写回: %v", err)
	}
	if !strings.Contains(string(data), "ReasoningEffort: high") {
		t.Fatalf("写回的配置里没有 high:\n%s", data)
	}
}

// /new 在同一个工作区开一条新会话，回包告诉前端切过去
func TestCommandNewAndResume(t *testing.T) {
	m := newTestManager(t)
	s := m.create()

	created := run(t, m, s, "/new")
	if created.Kind != replySession || created.Session == "" {
		t.Fatalf("/new 该回一条新会话的 id，实际 %+v", created)
	}
	if created.Session == s.Data().Id {
		t.Fatal("/new 该开一条新的，不是原来那条")
	}

	// 新会话已经落盘，所以 /resume 列表里看得见它
	list := run(t, m, s, "/resume")
	if !strings.Contains(list.Text, created.Session) {
		t.Fatalf("/resume 列表里该有新会话 %s:\n%s", created.Session, list.Text)
	}

	// /resume 1 挑最新的那条
	first := run(t, m, s, "/resume 1")
	if first.Kind != replySession {
		t.Fatalf("/resume 1 该回一条会话 id，实际 %+v", first)
	}
	if _, err := m.open(first.Session); err != nil {
		t.Fatalf("挑出来的会话该能打开: %v", err)
	}

	// 序号越界要说清楚
	if got := run(t, m, s, "/resume 99"); !strings.Contains(got.Text, "超出范围") {
		t.Fatalf("越界该提示一句:\n%s", got.Text)
	}
}

// /session 报当前会话；/todo、/skills 至少不该崩
func TestCommandSessionTodoSkills(t *testing.T) {
	m := newTestManager(t)
	s := m.create()
	s.Data().Title = "聊点什么"

	reply := run(t, m, s, "/session")
	for _, want := range []string{s.Data().Id, "聊点什么", "共 0 条消息"} {
		if !strings.Contains(reply.Text, want) {
			t.Fatalf("/session 里少了 %q:\n%s", want, reply.Text)
		}
	}

	if got := run(t, m, s, "/todo"); !strings.Contains(got.Text, "待办") {
		t.Fatalf("/todo 该有一句说明:\n%s", got.Text)
	}
	if got := run(t, m, s, "/skills"); got.Text == "" {
		t.Fatal("/skills 该有输出")
	}
}

// /config 不打印文本，回一个"打开表单"的信号
func TestCommandConfigOpensForm(t *testing.T) {
	m := newTestManager(t)
	s := m.create()

	if reply := run(t, m, s, "/config"); reply.Kind != replyConfig {
		t.Fatalf("/config 该让前端打开表单，实际 %+v", reply)
	}
}

// /api/commands 给出补全候选：内置命令 + 装好的技能，两边都带名字和说明
func TestAPICommands(t *testing.T) {
	m := newTestManager(t)
	srv := newTestServer(t, m)

	code, body := sendJSON(t, http.MethodGet, srv.URL+"/api/commands", "")
	if code != http.StatusOK {
		t.Fatalf("拉命令表该 200，实际 %d: %s", code, body)
	}
	var got completions
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("命令表不是 JSON: %v", err)
	}
	if len(got.Commands) == 0 {
		t.Fatal("命令表该有内容")
	}
	names := make(map[string]bool, len(got.Commands))
	for _, spec := range got.Commands {
		names[spec.Name] = true
		if !strings.HasPrefix(spec.Name, "/") {
			t.Fatalf("命令名该带前导斜杠: %q", spec.Name)
		}
	}
	for _, want := range []string{"/help", "/config", "/model"} {
		if !names[want] {
			t.Fatalf("命令表里少了 %q", want)
		}
	}
}

// 通过 HTTP 执行一条命令：/mode 切完回包里带 meta，前端据此刷新顶栏
func TestHTTPCommand(t *testing.T) {
	m := newTestManager(t)
	srv := newTestServer(t, m)
	s := m.create()

	code, body := sendJSON(t, http.MethodPost, srv.URL+"/api/sessions/"+s.Data().Id+"/command",
		`{"command":"/mode ask"}`)
	if code != http.StatusOK {
		t.Fatalf("执行命令该 200，实际 %d: %s", code, body)
	}
	var reply commandReply
	if err := json.Unmarshal([]byte(body), &reply); err != nil {
		t.Fatalf("回包不是 JSON: %v", err)
	}
	if reply.Meta == nil || reply.Meta.Mode != "ask" {
		t.Fatalf("回包里该带新的 meta: %+v", reply)
	}

	// 空命令挡住
	if code, _ := sendJSON(t, http.MethodPost, srv.URL+"/api/sessions/"+s.Data().Id+"/command",
		`{"command":"  "}`); code != http.StatusBadRequest {
		t.Fatalf("空命令该 400，实际 %d", code)
	}
}

// /config 表单：读出来的密钥只说"配过了没有"，保存时留空就是不改
func TestHTTPConfigForm(t *testing.T) {
	m := newTestManager(t)
	srv := newTestServer(t, m)

	code, body := sendJSON(t, http.MethodGet, srv.URL+"/api/config", "")
	if code != http.StatusOK {
		t.Fatalf("读表单该 200，实际 %d: %s", code, body)
	}
	var view configView
	if err := json.Unmarshal([]byte(body), &view); err != nil {
		t.Fatalf("表单不是 JSON: %v", err)
	}
	if !view.ApiKeySet {
		t.Fatal("配置里有密钥，表单该说配过了")
	}
	if strings.Contains(body, "sk-test") {
		t.Fatalf("密钥不能明文回给前端:\n%s", body)
	}
	if view.Type != "DeepSeek" || view.Model != "test-model" || view.ContextLimit != "39k" {
		t.Fatalf("表单值不对: %+v", view)
	}
	if len(view.Types) != len(config.ProviderTypes) || len(view.Efforts) != len(config.EffortLevels) {
		t.Fatalf("下拉框的可选项没带上: %+v", view)
	}

	// 改模型名和思考强度，密钥留空：密钥要保住
	code, body = sendJSON(t, http.MethodPost, srv.URL+"/api/config", `{
		"type":"OpenAI","api_url":"https://b","api_key":"","model":"model-b",
		"context_limit":"128k","effort":"high"}`)
	if code != http.StatusOK {
		t.Fatalf("保存该 200，实际 %d: %s", code, body)
	}
	cur, err := m.Config().CurrentModel()
	if err != nil {
		t.Fatalf("取当前模型失败: %v", err)
	}
	if cur.Model != "model-b" || cur.Type != "OpenAI" || cur.ReasoningEffort != "high" {
		t.Fatalf("保存后的配置不对: %+v", cur)
	}
	if cur.ApiKey != "sk-test" {
		t.Fatalf("密钥留空该沿用原来的，实际 %q", cur.ApiKey)
	}
	if cur.ContextLimit != 128*1024 {
		t.Fatalf("上下文上限该按 128k 算，实际 %d", cur.ContextLimit)
	}
	data, err := os.ReadFile(m.cfgPath)
	if err != nil {
		t.Fatalf("配置没写回: %v", err)
	}
	if !strings.Contains(string(data), "model-b") || !strings.Contains(string(data), "ReasoningEffort: high") {
		t.Fatalf("写回的配置不对:\n%s", data)
	}

	// 少填东西要报清楚
	if code, body := sendJSON(t, http.MethodPost, srv.URL+"/api/config",
		`{"type":"OpenAI","api_url":"https://b","api_key":"","model":"","context_limit":"","effort":""}`); code != http.StatusBadRequest {
		t.Fatalf("模型没填该 400，实际 %d", code)
	} else if !strings.Contains(body, "都得填") {
		t.Fatalf("错误信息该说清要填什么: %s", body)
	}
}

// 新配置里一套模型都没有时，/config 保存一套就该能跑
func TestHTTPConfigFormFirstModel(t *testing.T) {
	m := newTestManager(t)
	m.setConfig(config.GlobalConfig{Mode: "ask"})
	srv := newTestServer(t, m)

	code, body := sendJSON(t, http.MethodPost, srv.URL+"/api/config", `{
		"type":"DeepSeek","api_url":"","api_key":"sk-new","model":"deepseek-chat",
		"context_limit":"","effort":""}`)
	if code != http.StatusOK {
		t.Fatalf("第一次保存配置该 200，实际 %d: %s", code, body)
	}
	cur, err := m.Config().CurrentModel()
	if err != nil {
		t.Fatalf("保存后该有当前模型: %v", err)
	}
	// 地址留空就用内置的那个；名字没有就用模型名顶上
	if cur.ApiUrl != config.DefaultDeepSeekApiUrl {
		t.Fatalf("地址该退回内置的，实际 %q", cur.ApiUrl)
	}
	if cur.Label() != "deepseek-chat" {
		t.Fatalf("没名字该用模型名当名字，实际 %q", cur.Label())
	}
	if cur.ContextLimit != config.DefaultContextLimit {
		t.Fatalf("上下文上限该用默认值，实际 %d", cur.ContextLimit)
	}
	meta := m.meta()
	if meta.Model != "deepseek-chat" {
		t.Fatalf("顶栏的模型名该跟着变，实际 %q", meta.Model)
	}
}

// 拉模型列表要求地址和密钥都在：缺了要说清楚缺哪个
func TestListModelsNeedsAddress(t *testing.T) {
	m := newTestManager(t)
	if _, err := m.listModels("", ""); err == nil || !strings.Contains(err.Error(), "接口地址") {
		t.Fatalf("地址没填该报错: %v", err)
	}

	// 地址有了、密钥留空时用配置里那个，于是会真的去打 https://a —— 那是假地址，
	// 用一个必定失败的地址确认"报的是请求失败"而不是"密钥没填"
	if _, err := m.listModels("https://127.0.0.1:1/v1", ""); err == nil {
		t.Fatal("打不通的地址该报错")
	}
}

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

// 编辑模式下不填名字就是"不改名字"，不能默默换成模型名
func TestSaveConfigKeepsNameWhenEmpty(t *testing.T) {
	m := newTestManager(t)
	m.setConfig(config.GlobalConfig{Models: []config.ModelConfig{
		{Name: "快", Type: "DeepSeek", ApiUrl: "https://a", ApiKey: "sk-a", Model: "model-a"},
	}, Current: "快"})

	if _, err := m.saveConfig(configRequest{
		Type: "DeepSeek", ApiUrl: "https://a", ApiKey: "sk-a", Model: "model-a",
	}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	models := m.Config().AvailableModels()
	if len(models) != 1 || models[0].Name != "快" {
		t.Fatalf("名字不该被换掉: %+v", models)
	}
	if m.Config().Current != "快" {
		t.Fatalf("当前那套不该变: %q", m.Config().Current)
	}
}

// HTTP 这一层：GET /api/config?new=1 给空白表单，DELETE /api/config/models 删掉一套
func TestHTTPConfigAddAndDelete(t *testing.T) {
	m := newTestManager(t)
	m.setConfig(config.GlobalConfig{Models: []config.ModelConfig{
		{Name: "快", Type: "DeepSeek", ApiUrl: "https://a", ApiKey: "sk-a", Model: "model-a"},
		{Name: "稳", Type: "OpenAI", ApiUrl: "https://b", ApiKey: "sk-b", Model: "model-b"},
	}, Current: "快"})
	srv := newTestServer(t, m)

	// 新增模式的表单是空白的
	code, body := sendJSON(t, http.MethodGet, srv.URL+"/api/config?new=1", "")
	if code != http.StatusOK {
		t.Fatalf("读新增表单该 200，实际 %d: %s", code, body)
	}
	var view configView
	if err := json.Unmarshal([]byte(body), &view); err != nil {
		t.Fatalf("表单不是 JSON: %v", err)
	}
	if view.Mode != "new" || view.EditLabel != "" || view.Name != "" {
		t.Fatalf("该是空白的新增表单: %+v", view)
	}
	if view.ApiUrl != config.DefaultApiUrl("DeepSeek") {
		t.Fatalf("地址该给内置的，实际 %q", view.ApiUrl)
	}

	// 删掉一套：删的是当前那套，Current 跟着换
	code, body = sendJSON(t, http.MethodDelete, srv.URL+"/api/config/models?name="+"稳", "")
	if code != http.StatusOK {
		t.Fatalf("删除该 200，实际 %d: %s", code, body)
	}
	models := m.Config().AvailableModels()
	if len(models) != 1 || models[0].Name != "快" {
		t.Fatalf("该删掉稳那套: %+v", models)
	}
	if m.Config().Current != "快" {
		t.Fatalf("删的不是当前那套，Current 不该动: %q", m.Config().Current)
	}

	// 没说要删哪一套，该报清楚
	if code, _ := sendJSON(t, http.MethodDelete, srv.URL+"/api/config/models", ""); code != http.StatusBadRequest {
		t.Fatalf("没带名字该 400，实际 %d", code)
	}
}

// /model del 名字 回一个要确认的回复：前端弹确认框，点了确定再发删除请求
func TestCommandModelDelAsksConfirm(t *testing.T) {
	m := newTestManager(t)
	m.setConfig(config.GlobalConfig{Models: []config.ModelConfig{
		{Name: "快", Type: "DeepSeek", ApiUrl: "https://a", ApiKey: "sk-a", Model: "model-a"},
		{Name: "稳", Type: "OpenAI", ApiUrl: "https://b", ApiKey: "sk-b", Model: "model-b"},
	}, Current: "快"})
	s := m.create()

	reply := run(t, m, s, "/model del 稳")
	if reply.Kind != replyConfirm {
		t.Fatalf("/model del 该要确认，实际 %q", reply.Kind)
	}
	if reply.Confirm != "del-model:稳" {
		t.Fatalf("确认动作不对: %q", reply.Confirm)
	}
	// 只是问一句，还没删
	if len(m.Config().AvailableModels()) != 2 {
		t.Fatalf("确认前不该真删: %+v", m.Config().AvailableModels())
	}

	// 不带名字就列出来让人照着写
	list := run(t, m, s, "/model del")
	if !strings.Contains(list.Text, "快") || !strings.Contains(list.Text, "稳") {
		t.Fatalf("/model del 该列出各套模型:\n%s", list.Text)
	}

	// 只剩一套时删不掉
	m.setConfig(config.GlobalConfig{Models: []config.ModelConfig{
		{Name: "快", Type: "DeepSeek", ApiUrl: "https://a", ApiKey: "sk-a", Model: "model-a"},
	}, Current: "快"})
	if got := run(t, m, s, "/model del 快"); !strings.Contains(got.Text, "只剩一套") {
		t.Fatalf("只剩一套该说删不掉:\n%s", got.Text)
	}
}

// 改的不是当前那套时，密钥留空是沿用**那套**的密钥，不能拿当前那套的盖上去
func TestSaveConfigKeepsEditedKey(t *testing.T) {
	m := newTestManager(t)
	m.setConfig(config.GlobalConfig{Models: []config.ModelConfig{
		{Name: "快", Type: "DeepSeek", ApiUrl: "https://a", ApiKey: "sk-A", Model: "model-a"},
		{Name: "稳", Type: "OpenAI", ApiUrl: "https://b", ApiKey: "sk-B", Model: "model-b"},
	}, Current: "快"})

	// 改"稳"，密钥留空：sk-B 该保住
	if _, err := m.saveConfig(configRequest{
		EditLabel: "稳", Name: "稳", Type: "OpenAI", ApiUrl: "https://b",
		ApiKey: "", Model: "model-b",
	}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	models := m.Config().AvailableModels()
	if len(models) != 2 {
		t.Fatalf("该还是两套: %+v", models)
	}
	if models[1].ApiKey != "sk-B" {
		t.Fatalf("改的那套的密钥被覆盖了: %+v", models[1])
	}
	if models[0].ApiKey != "sk-A" {
		t.Fatalf("当前那套的密钥不该动: %+v", models[0])
	}

	// 改当前那套时，密钥留空照样沿用当前那套的
	if _, err := m.saveConfig(configRequest{
		EditLabel: "快", Name: "快", Type: "DeepSeek", ApiUrl: "https://a",
		ApiKey: "", Model: "model-a",
	}); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	models = m.Config().AvailableModels()
	if models[0].ApiKey != "sk-A" {
		t.Fatalf("改当前那套时密钥该沿用: %+v", models[0])
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

// 命令表里的每条命令，两个界面都得认识：这里顺带守住"加了命令忘了接分支"这件事
func TestAllCommandsHandled(t *testing.T) {
	m := newTestManager(t)
	s := m.create()

	for _, name := range command.Names() {
		reply := run(t, m, s, name)
		if reply.Kind == "" {
			t.Fatalf("%s 没回任何东西", name)
		}
		if strings.Contains(reply.Text, "不认识这条命令") {
			t.Fatalf("%s 掉进了兜底分支", name)
		}
	}
}
