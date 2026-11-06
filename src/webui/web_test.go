package webui

import (
	"bufio"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"net/http"
	"net/http/httptest"
	"os"
	"path/filepath"
	"strings"
	"sync/atomic"
	"testing"
	"time"

	"hamusuta/agent-dev/src/config"
	"hamusuta/agent-dev/src/domain"
	"hamusuta/agent-dev/src/session"
)

// fakeProvider 是测试用的假模型：按脚本吐事件，不打接口、不花 token。
// script 每次 Chat 调用取一步，用完了就当作"不再调工具"收尾
type fakeProvider struct {
	steps  []fakeStep
	calls  int32
	onChat func(ctx context.Context) // 每一步前先做点事（比如发个审批）
}

type fakeStep struct {
	reasoning string
	content   string
	// tool 非空就让 core 去调这个工具，名字要真实存在（Read 之类），
	// 参数自己给；core 会照着跑
	tool string
	args string
	// wait 模拟工具调用中途被中断：一直等到 ctx 结束
	wait bool
}

func (p *fakeProvider) Chat(ctx context.Context, messages []domain.AIChatMessage, tools []domain.ToolSpec, onDelta func(domain.StreamEvent)) (domain.ChatResult, error) {
	n := int(atomic.AddInt32(&p.calls, 1)) - 1
	if p.onChat != nil {
		p.onChat(ctx)
	}
	if n >= len(p.steps) {
		return domain.ChatResult{Content: "完事了"}, nil
	}
	step := p.steps[n]
	if step.wait {
		<-ctx.Done()
		return domain.ChatResult{}, ctx.Err()
	}
	// 真实 provider 都判了 onDelta 为空（比如 core 拟标题那次就不关心流式增量），
	// 这里也得判，不然走到那条路上会 nil panic
	if step.reasoning != "" && onDelta != nil {
		onDelta(domain.StreamEvent{Kind: domain.StreamReasoning, Text: step.reasoning})
	}
	if step.content != "" && onDelta != nil {
		onDelta(domain.StreamEvent{Kind: domain.StreamContent, Text: step.content})
	}
	res := domain.ChatResult{
		Content:   step.content,
		Reasoning: step.reasoning,
		Usage:     domain.Usage{PromptTokens: 1000, CompletionTokens: 20, TotalTokens: 1020},
	}
	if step.tool != "" {
		res.ToolCalls = []domain.ToolCall{{Id: fmt.Sprintf("call-%d", n), Name: step.tool, Arguments: step.args}}
	}
	return res, nil
}

// withFakeProvider 把一个假 provider 装上，返回收工时恢复环境的函数
func withFakeProvider(t *testing.T, p domain.Provider) {
	t.Helper()
	old := newProvider
	newProvider = func(config.ModelConfig) (domain.Provider, error) { return p, nil }
	t.Cleanup(func() { newProvider = old })
}

// newTestManager 造一个指向临时目录的 Manager，配置里有一套假模型。
// 目录结构和线上一致：会话存在 <root>/<工作区 id>/ 里，工作区就是启动目录
func newTestManager(t *testing.T) *Manager {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "sessions")
	id, err := session.WorkspaceId(dir)
	if err != nil {
		t.Fatalf("算工作区 id 失败: %v", err)
	}
	store := session.NewStore(filepath.Join(root, id))
	cfg := config.GlobalConfig{
		Models: []config.ModelConfig{{
			Name: "测", Type: "DeepSeek", ApiUrl: "https://example.invalid",
			ApiKey: "sk-test", Model: "test-model", ContextLimit: 40000,
		}},
		Current: "测",
		Mode:    "full",
	}
	return newManager(Options{
		WorkDir: dir,
		Root:    root,
		CfgPath: filepath.Join(dir, "config.yaml"),
		Cfg:     cfg,
		Store:   store,
	})
}

// collect 从订阅者那里攒事件，等到 pred 成立或超时
func collect(t *testing.T, ch <-chan []byte, until func([]event) bool) []event {
	t.Helper()
	var got []event
	deadline := time.After(5 * time.Second)
	for {
		select {
		case data, open := <-ch:
			if !open {
				return got
			}
			var ev event
			if err := json.Unmarshal(data, &ev); err != nil {
				t.Fatalf("事件不是合法 JSON: %v: %s", err, data)
			}
			got = append(got, ev)
			if until != nil && until(got) {
				return got
			}
		case <-deadline:
			return got
		}
	}
}

// 等到最后一个事件是收尾事件
func untilFinish() func([]event) bool {
	return func(evs []event) bool {
		if len(evs) == 0 {
			return false
		}
		switch evs[len(evs)-1].Kind {
		case kindDone, kindError, kindCancelled:
			return true
		}
		return false
	}
}

// 一轮跑完：事件按顺序推到订阅者，会话落盘，state 回到"没在跑"
func TestSessionRunBroadcasts(t *testing.T) {
	m := newTestManager(t)
	withFakeProvider(t, &fakeProvider{steps: []fakeStep{
		{reasoning: "想一下", content: "你好"},
	}})

	s := m.create()
	ch, unsubscribe := s.Subscribe()
	defer unsubscribe()

	if err := s.Start(m.Config(), "打个招呼", nil); err != nil {
		t.Fatalf("起一轮失败: %v", err)
	}
	evs := collect(t, ch, untilFinish())

	kinds := make([]string, 0, len(evs))
	for _, ev := range evs {
		kinds = append(kinds, ev.Kind)
	}
	// 开头是 started，结尾是 done，中间得有思考、正文、用量
	order := []string{kindStarted, kindStep, kindReasoning, kindContent, kindUsage, kindAnswerEnd, kindDone}
	at := 0
	for _, want := range order {
		found := false
		for ; at < len(kinds); at++ {
			if kinds[at] == want {
				found = true
				at++
				break
			}
		}
		if !found {
			t.Fatalf("事件里少了 %q，实际顺序 %v", want, kinds)
		}
	}
	if s.Running() {
		t.Fatal("跑完了还标着在跑")
	}
	// 提问进了会话，也落了盘
	msgs := s.Data().Messages
	if len(msgs) < 2 || msgs[0].Content != "打个招呼" {
		t.Fatalf("会话里没记下这次提问: %+v", msgs)
	}
	saved, err := m.defaultSpace().Store.Load(s.Data().Id)
	if err != nil {
		t.Fatalf("会话没落盘: %v", err)
	}
	if len(saved.Messages) != len(msgs) {
		t.Fatalf("落盘的消息数不对: %d vs %d", len(saved.Messages), len(msgs))
	}
}

// 工具调用要成对出现：先"调用中"，再带状态和耗时的结果
func TestSessionToolEvents(t *testing.T) {
	m := newTestManager(t)
	// 工具各自按进程的当前目录解析相对路径，这里给绝对路径，免得依赖测试跑在哪儿
	path := filepath.Join(m.workdir, "a.txt")
	if err := os.WriteFile(path, []byte("内容"), 0o644); err != nil {
		t.Fatal(err)
	}
	withFakeProvider(t, &fakeProvider{steps: []fakeStep{
		{tool: "Read", args: fmt.Sprintf(`{"intent":"看看文件","path":%q}`, path)},
		{content: "看完了"},
	}})
	m.cfg.Mode = "full"

	s := m.create()
	ch, unsubscribe := s.Subscribe()
	defer unsubscribe()
	if err := s.Start(m.Config(), "读一下", nil); err != nil {
		t.Fatalf("起一轮失败: %v", err)
	}
	evs := collect(t, ch, untilFinish())

	var starting, finished *event
	for i := range evs {
		switch evs[i].Kind {
		case kindToolCall:
			starting = &evs[i]
		case kindToolEnd:
			finished = &evs[i]
		}
	}
	if starting == nil || finished == nil {
		t.Fatalf("工具事件没成对出现: %+v", evs)
	}
	if starting.Tool != "Read" || starting.Intent != "看看文件" {
		t.Fatalf("调用那条信息不对: %+v", starting)
	}
	if starting.Status != domain.ToolStatusRunning.Label() {
		t.Fatalf("调用中的状态不对: %q", starting.Status)
	}
	if finished.CallId != starting.CallId {
		t.Fatalf("结果和调用对不上: %q vs %q", finished.CallId, starting.CallId)
	}
	if finished.Tool != "Read" || !strings.Contains(finished.Text, "内容") {
		t.Fatalf("结果那条不对: %+v", finished)
	}
}

// 编辑需授权模式下，写操作会停下来等确认：这里验证事件送到了、
// 拒绝之后这一轮如实收尾（不写文件）
func TestSessionApprovalDenied(t *testing.T) {
	m := newTestManager(t)
	m.cfg.Mode = "ask"
	target := filepath.Join(m.workdir, "new.txt")

	withFakeProvider(t, &fakeProvider{steps: []fakeStep{
		{tool: "Write", args: fmt.Sprintf(`{"intent":"写个文件","path":%q,"content":"x"}`, target)},
	}})

	s := m.create()
	ch, unsubscribe := s.Subscribe()
	defer unsubscribe()
	if err := s.Start(m.Config(), "写一下", nil); err != nil {
		t.Fatalf("起一轮失败: %v", err)
	}

	// 先等到确认事件
	var approvalEvent *event
	for _, ev := range collect(t, ch, func(evs []event) bool {
		for _, e := range evs {
			if e.Kind == kindApproval {
				return true
			}
		}
		return false
	}) {
		if ev.Kind == kindApproval {
			e := ev
			approvalEvent = &e
		}
	}
	if approvalEvent == nil || approvalEvent.Approval == nil {
		t.Fatal("没等到确认事件")
	}
	if approvalEvent.Approval.ID == "" {
		t.Fatal("确认事件得带上 id，否则前端没法指认是哪一项")
	}
	if approvalEvent.Approval.Kind != "write_file" || !strings.Contains(approvalEvent.Approval.Target, "new.txt") {
		t.Fatalf("确认内容不对: %+v", approvalEvent.Approval)
	}

	// 拒绝并说明
	if !s.Answer(approvalEvent.Approval.ID, domain.ApprovalAnswer{Feedback: "别写这个文件"}) {
		t.Fatal("回答没送进去")
	}
	// 同一个 id 只能答一次
	if s.Answer(approvalEvent.Approval.ID, domain.ApprovalAnswer{Allowed: true}) {
		t.Fatal("已经答过的确认不该能再答一次")
	}

	collect(t, ch, untilFinish())
	if _, err := os.Stat(target); !errors.Is(err, os.ErrNotExist) {
		t.Fatalf("拒绝了还是把文件写了: %v", err)
	}
	// 拒绝的理由要转给模型：会话里那次工具结果得带着这句话
	var found bool
	for _, msg := range s.Data().Messages {
		if msg.Role == domain.RoleTool && strings.Contains(msg.Content, "别写这个文件") {
			found = true
		}
	}
	if !found {
		t.Fatalf("拒绝的理由没转给模型: %+v", s.Data().Messages)
	}
}

// 放行之后工具真的跑，文件真的写出来
func TestSessionApprovalAllowed(t *testing.T) {
	m := newTestManager(t)
	m.cfg.Mode = "ask"
	target := filepath.Join(m.workdir, "written.txt")

	withFakeProvider(t, &fakeProvider{steps: []fakeStep{
		{tool: "Write", args: fmt.Sprintf(`{"intent":"写个文件","path":%q,"content":"写进去了"}`, target)},
	}})

	s := m.create()
	ch, unsubscribe := s.Subscribe()
	defer unsubscribe()
	if err := s.Start(m.Config(), "写一下", nil); err != nil {
		t.Fatalf("起一轮失败: %v", err)
	}
	var id string
	for _, ev := range collect(t, ch, func(evs []event) bool {
		for _, e := range evs {
			if e.Kind == kindApproval {
				return true
			}
		}
		return false
	}) {
		if ev.Kind == kindApproval && ev.Approval != nil {
			id = ev.Approval.ID
		}
	}
	if id == "" {
		t.Fatal("没等到确认事件")
	}
	if !s.Answer(id, domain.ApprovalAnswer{Allowed: true}) {
		t.Fatal("回答没送进去")
	}
	collect(t, ch, untilFinish())

	data, err := os.ReadFile(target)
	if err != nil {
		t.Fatalf("放行了却没写出来: %v", err)
	}
	if string(data) != "写进去了" {
		t.Fatalf("写的内容不对: %q", data)
	}
}

// 中断：这一轮以 cancelled 收尾，会话不脏
func TestSessionCancel(t *testing.T) {
	m := newTestManager(t)
	withFakeProvider(t, &fakeProvider{steps: []fakeStep{{wait: true}}})

	s := m.create()
	ch, unsubscribe := s.Subscribe()
	defer unsubscribe()
	if err := s.Start(m.Config(), "跑个长的", nil); err != nil {
		t.Fatalf("起一轮失败: %v", err)
	}
	// 等它真的开始跑
	deadline := time.Now().Add(3 * time.Second)
	for !s.Running() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if !s.Cancel() {
		t.Fatal("中断没生效")
	}
	evs := collect(t, ch, untilFinish())
	if len(evs) == 0 || evs[len(evs)-1].Kind != kindCancelled {
		t.Fatalf("收尾事件该是 cancelled: %+v", evs)
	}
	if s.Running() {
		t.Fatal("中断之后还标着在跑")
	}
	// 再中断一次是空操作，不该报错也不该崩
	if s.Cancel() {
		t.Fatal("已经结束了还能中断")
	}
}

// 一轮没跑完时不许再起一轮：前端靠这个 409 把消息排起来
func TestSessionBusy(t *testing.T) {
	m := newTestManager(t)
	withFakeProvider(t, &fakeProvider{steps: []fakeStep{{wait: true}}})

	s := m.create()
	if err := s.Start(m.Config(), "第一条", nil); err != nil {
		t.Fatalf("起一轮失败: %v", err)
	}
	if err := s.Start(m.Config(), "第二条", nil); !errors.Is(err, ErrBusy) {
		t.Fatalf("应该返回 ErrBusy，实际 %v", err)
	}
	s.Cancel()
	// 收尾之后还能再起一轮
	deadline := time.Now().Add(3 * time.Second)
	for s.Running() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	withFakeProvider(t, &fakeProvider{steps: []fakeStep{{content: "好了"}}})
	if err := s.Start(m.Config(), "再来一条", nil); err != nil {
		t.Fatalf("跑完之后该能再起一轮: %v", err)
	}
	collect(t, mustSubscribe(t, s), untilFinish())
}

func mustSubscribe(t *testing.T, s *Session) <-chan []byte {
	t.Helper()
	ch, unsubscribe := s.Subscribe()
	t.Cleanup(unsubscribe)
	return ch
}

// 没配模型时起一轮要报 ErrNoModel，而不是让人等一个永远不来的响应
func TestSessionNoModel(t *testing.T) {
	m := newTestManager(t)
	m.cfg = config.GlobalConfig{}
	s := m.create()
	if err := s.Start(m.Config(), "在吗", nil); !errors.Is(err, ErrNoModel) {
		t.Fatalf("应该返回 ErrNoModel，实际 %v", err)
	}
}

// 没订阅者也在跑：没人看的时候事件丢了没关系，别卡住这一轮
func TestSessionRunsWithoutSubscriber(t *testing.T) {
	m := newTestManager(t)
	withFakeProvider(t, &fakeProvider{steps: []fakeStep{{content: "没人看也照跑"}}})
	s := m.create()
	if err := s.Start(m.Config(), "你好", nil); err != nil {
		t.Fatalf("起一轮失败: %v", err)
	}
	deadline := time.Now().Add(3 * time.Second)
	for s.Running() && time.Now().Before(deadline) {
		time.Sleep(10 * time.Millisecond)
	}
	if s.Running() {
		t.Fatal("没订阅者时卡住了")
	}
	if got := s.Data().LastAssistant(); got == "" {
		t.Fatalf("回答没记下来: %+v", s.Data().Messages)
	}
}

// ---------------- HTTP 层 ----------------

// newTestServer 把 Manager 的路由挂到 httptest 上
func newTestServer(t *testing.T, m *Manager) *httptest.Server {
	t.Helper()
	srv := httptest.NewServer(m.routes())
	t.Cleanup(srv.Close)
	return srv
}

// sendJSON 发一个 JSON 请求，返回状态码和响应体
func sendJSON(t *testing.T, method, url string, body string) (int, string) {
	t.Helper()
	var reader io.Reader
	if body != "" {
		reader = strings.NewReader(body)
	}
	req, err := http.NewRequest(method, url, reader)
	if err != nil {
		t.Fatal(err)
	}
	if body != "" {
		req.Header.Set("Content-Type", "application/json")
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("%s %s 失败: %v", method, url, err)
	}
	defer res.Body.Close()
	data, _ := io.ReadAll(res.Body)
	return res.StatusCode, string(data)
}

// 新建会话、发消息、SSE 收事件，一条链路走完
func TestHTTPFlow(t *testing.T) {
	m := newTestManager(t)
	withFakeProvider(t, &fakeProvider{steps: []fakeStep{{content: "收到了"}}})
	srv := newTestServer(t, m)

	// meta 说清楚现在用哪套模型、什么模式
	code, body := sendJSON(t, http.MethodGet, srv.URL+"/api/meta", "")
	if code != http.StatusOK {
		t.Fatalf("meta 该 200，实际 %d: %s", code, body)
	}
	var metaResp meta
	if err := json.Unmarshal([]byte(body), &metaResp); err != nil {
		t.Fatalf("meta 不是 JSON: %v", err)
	}
	if metaResp.Model != "测" || metaResp.Mode != "full" || metaResp.ContextLimit != 40000 {
		t.Fatalf("meta 内容不对: %+v", metaResp)
	}

	// 新建会话
	code, body = sendJSON(t, http.MethodPost, srv.URL+"/api/sessions", "")
	if code != http.StatusOK {
		t.Fatalf("新建会话该 200，实际 %d: %s", code, body)
	}
	var snap snapshot
	if err := json.Unmarshal([]byte(body), &snap); err != nil {
		t.Fatalf("快照不是 JSON: %v", err)
	}
	if snap.Id == "" || len(snap.Messages) != 0 {
		t.Fatalf("新会话该是空的: %+v", snap)
	}

	// 空消息挡住
	if code, _ := sendJSON(t, http.MethodPost, srv.URL+"/api/sessions/"+snap.Id+"/messages",
		`{"prompt":"","images":[]}`); code != http.StatusBadRequest {
		t.Fatalf("空消息该 400，实际 %d", code)
	}

	// 正常发一条
	if code, body := sendJSON(t, http.MethodPost, srv.URL+"/api/sessions/"+snap.Id+"/messages",
		`{"prompt":"你好"}`); code != http.StatusNoContent {
		t.Fatalf("发消息该 204，实际 %d: %s", code, body)
	}

	// 等这一轮跑完再拉快照，历史里应该有一问一答
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, body = sendJSON(t, http.MethodGet, srv.URL+"/api/sessions/"+snap.Id, "")
		var cur snapshot
		if err := json.Unmarshal([]byte(body), &cur); err != nil {
			t.Fatalf("快照不是 JSON: %v", err)
		}
		if !cur.Running && len(cur.Messages) >= 2 {
			if cur.Messages[0].Role != "user" || cur.Messages[0].Content != "你好" {
				t.Fatalf("历史第一条该是提问: %+v", cur.Messages[0])
			}
			if cur.Messages[1].Role != "assistant" || cur.Messages[1].Content != "收到了" {
				t.Fatalf("历史第二条该是回答: %+v", cur.Messages[1])
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("这一轮没跑完")
}

// 会话路径里的 id 不能跨目录：随便拼个 ../ 不该读到别处的文件
func TestHTTPBadSessionId(t *testing.T) {
	m := newTestManager(t)
	srv := newTestServer(t, m)
	for _, id := range []string{"..", "../etc", "a/b"} {
		if code, _ := sendJSON(t, http.MethodGet, srv.URL+"/api/sessions/"+id, ""); code == http.StatusOK {
			t.Fatalf("id %q 不该读到东西", id)
		}
	}
}

// 模式切换要写回配置文件，并且把已经打开的会话一起切过去
func TestHTTPSetMode(t *testing.T) {
	m := newTestManager(t)
	withFakeProvider(t, &fakeProvider{steps: []fakeStep{{content: "好"}}})
	srv := newTestServer(t, m)
	s := m.create()

	code, body := sendJSON(t, http.MethodPost, srv.URL+"/api/mode", `{"mode":"read"}`)
	if code != http.StatusOK {
		t.Fatalf("切模式该 200，实际 %d: %s", code, body)
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

	// 认不出的模式挡住，配置不变
	if code, _ := sendJSON(t, http.MethodPost, srv.URL+"/api/mode", `{"mode":"半自动"}`); code != http.StatusBadRequest {
		t.Fatalf("认不出的模式该 400，实际 %d", code)
	}
	if got := s.Mode(); got != domain.ModeReadOnly {
		t.Fatalf("认不出的模式不该改掉当前模式: %v", got)
	}
}

// 切模式要当场推给连着的页面：别的标签页顶栏那个下拉框不能停在旧值上，
// 停在旧值上会让人以为切换没生效（甚至照着旧值又切回去）
func TestSetModeNotifiesPages(t *testing.T) {
	m := newTestManager(t)
	s := m.create()
	ch, stop := s.Subscribe()
	defer stop()

	if err := m.setMode(domain.ModeAsk); err != nil {
		t.Fatalf("切模式失败: %v", err)
	}
	evs := collect(t, ch, func(evs []event) bool {
		return len(evs) > 0
	})
	if len(evs) == 0 {
		t.Fatal("切模式之后该推一条事件过来")
	}
	if evs[0].Kind != kindMeta {
		t.Fatalf("推过来的不该是 %q", evs[0].Kind)
	}
	if evs[0].Meta == nil || evs[0].Meta.Mode != "ask" || evs[0].Meta.ModeLabel != "编辑需授权" {
		t.Fatalf("meta 里的模式不对: %+v", evs[0].Meta)
	}
}

// 换模型：POST /api/model 把当前那套换掉，配置文件要写回，
// 顶栏拿到的 meta 也跟着换（前端靠它刷徽标），别的标签页也得收到推进来的一条
func TestHTTPSetModel(t *testing.T) {
	m := newTestManager(t)
	// 再配一套：切换这事得有两套才有意义
	m.setConfig(addOtherModel(m.Config()))
	srv := newTestServer(t, m)
	s := m.create()
	ch, stop := s.Subscribe()
	defer stop()

	code, body := sendJSON(t, http.MethodPost, srv.URL+"/api/model", `{"model":"另一套"}`)
	if code != http.StatusOK {
		t.Fatalf("切模型该 200，实际 %d: %s", code, body)
	}
	var got meta
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("回包不是合法的 meta: %v: %s", err, body)
	}
	if got.Model != "另一套" {
		t.Fatalf("回包的 meta 还是旧模型: %+v", got)
	}
	if cur, err := m.Config().CurrentModel(); err != nil || cur.Label() != "另一套" {
		t.Fatalf("内存里没切过去: %+v %v", cur, err)
	}
	data, err := os.ReadFile(m.cfgPath)
	if err != nil {
		t.Fatalf("配置没写回: %v", err)
	}
	if !strings.Contains(string(data), "Current: 另一套") {
		t.Fatalf("写回的配置里 Current 不对:\n%s", data)
	}
	// 开着的页面（另一个标签页）也得知道换了：SSE 上要有一条 meta
	evs := collect(t, ch, func(evs []event) bool { return len(evs) > 0 })
	if len(evs) == 0 || evs[0].Kind != kindMeta || evs[0].Meta == nil || evs[0].Meta.Model != "另一套" {
		t.Fatalf("切模型之后该推一条新模型的 meta: %+v", evs)
	}

	// 认不出的名字挡住，当前那套不动
	if code, _ := sendJSON(t, http.MethodPost, srv.URL+"/api/model", `{"model":"不存在"}`); code != http.StatusBadRequest {
		t.Fatalf("认不出的模型该 400，实际 %d", code)
	}
	if cur, err := m.Config().CurrentModel(); err != nil || cur.Label() != "另一套" {
		t.Fatalf("认不出的模型不该改掉当前那套: %+v %v", cur, err)
	}
}

// addOtherModel 在当前配置上再挂一套假模型，给"得有两套才谈得上切换"的用例用
func addOtherModel(cfg config.GlobalConfig) config.GlobalConfig {
	cfg.Models = append(cfg.Models, config.ModelConfig{
		Name: "另一套", Type: "OpenAI", ApiUrl: "https://example.invalid",
		ApiKey: "sk-test", Model: "other-model", ContextLimit: 128000,
	})
	return cfg
}

// 消息发出去之后，SSE 那条连接真的收到事件流
func TestHTTPEvents(t *testing.T) {
	m := newTestManager(t)
	withFakeProvider(t, &fakeProvider{steps: []fakeStep{{content: "流式回答"}}})
	srv := newTestServer(t, m)
	s := m.create()

	req, err := http.NewRequest(http.MethodGet, srv.URL+"/api/sessions/"+s.Data().Id+"/events", nil)
	if err != nil {
		t.Fatal(err)
	}
	res, err := http.DefaultClient.Do(req)
	if err != nil {
		t.Fatalf("连 SSE 失败: %v", err)
	}
	defer res.Body.Close()
	if ct := res.Header.Get("Content-Type"); !strings.HasPrefix(ct, "text/event-stream") {
		t.Fatalf("Content-Type 不对: %q", ct)
	}

	// 订阅是连上之后才挂上去的，稍等一下再发消息
	time.Sleep(50 * time.Millisecond)
	if err := s.Start(m.Config(), "说点什么", nil); err != nil {
		t.Fatalf("起一轮失败: %v", err)
	}

	// 从流里读到 done 为止
	dec := json.NewDecoder(newLineReader(res.Body))
	deadline := time.After(5 * time.Second)
	for {
		select {
		case <-deadline:
			t.Fatal("5 秒内没等到收尾事件")
		default:
		}
		var ev event
		if err := dec.Decode(&ev); err != nil {
			t.Fatalf("读事件失败: %v", err)
		}
		if ev.Kind == kindContent && ev.Text != "流式回答" {
			t.Fatalf("正文事件内容不对: %+v", ev)
		}
		if ev.Kind == kindDone {
			return
		}
	}
}

// lineReader 把 SSE 的 "data: {...}" 行翻成一条条 JSON 给 decoder 读。
// 心跳那种 ": ping" 注释行、空行都跳过
type lineReader struct {
	r       *bufio.Reader
	pending []byte
}

func newLineReader(r io.Reader) *lineReader {
	return &lineReader{r: bufio.NewReader(r)}
}

func (l *lineReader) Read(p []byte) (int, error) {
	for len(l.pending) == 0 {
		line, err := l.r.ReadString('\n')
		if err != nil {
			return 0, err
		}
		line = strings.TrimSpace(line)
		if line == "" || strings.HasPrefix(line, ":") || !strings.HasPrefix(line, "data:") {
			continue
		}
		payload := strings.TrimSpace(strings.TrimPrefix(line, "data:"))
		l.pending = []byte(payload + "\n")
	}
	n := copy(p, l.pending)
	l.pending = l.pending[n:]
	return n, nil
}

// 删除会话只摘内存，不动磁盘上的文件——关掉标签页不该顺手删掉一整段对话
func TestHTTPDeleteSession(t *testing.T) {
	m := newTestManager(t)
	srv := newTestServer(t, m)
	code, body := sendJSON(t, http.MethodPost, srv.URL+"/api/sessions", "")
	if code != http.StatusOK {
		t.Fatalf("新建会话失败: %d %s", code, body)
	}
	var snap snapshot
	if err := json.Unmarshal([]byte(body), &snap); err != nil {
		t.Fatal(err)
	}

	if code, _ := sendJSON(t, http.MethodDelete, srv.URL+"/api/sessions/"+snap.Id, ""); code != http.StatusNoContent {
		t.Fatalf("删除该 204，实际 %d", code)
	}
	// 磁盘上还在
	if _, err := m.defaultSpace().Store.Load(snap.Id); err != nil {
		t.Fatalf("磁盘上的会话不该被删: %v", err)
	}
}

// 运行中拉快照：能看见本次提问、并且标着在跑。
// 这类请求走的是磁盘上那份（内存里那份 core 正在写），所以这里顺带验证
// 第一轮开始时确实已经把提问落盘了
func TestSnapshotWhileRunning(t *testing.T) {
	m := newTestManager(t)
	withFakeProvider(t, &fakeProvider{steps: []fakeStep{{wait: true}}})
	srv := newTestServer(t, m)

	code, body := sendJSON(t, http.MethodPost, srv.URL+"/api/sessions", "")
	if code != http.StatusOK {
		t.Fatalf("新建会话失败: %d %s", code, body)
	}
	var snap snapshot
	if err := json.Unmarshal([]byte(body), &snap); err != nil {
		t.Fatal(err)
	}
	if code, body := sendJSON(t, http.MethodPost, srv.URL+"/api/sessions/"+snap.Id+"/messages",
		`{"prompt":"跑个长的"}`); code != http.StatusNoContent {
		t.Fatalf("发消息该 204，实际 %d: %s", code, body)
	}

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		_, body := sendJSON(t, http.MethodGet, srv.URL+"/api/sessions/"+snap.Id, "")
		var cur snapshot
		if err := json.Unmarshal([]byte(body), &cur); err != nil {
			t.Fatal(err)
		}
		if cur.Running && len(cur.Messages) > 0 {
			if len(cur.Messages) != 1 || cur.Messages[0].Content != "跑个长的" {
				t.Fatalf("运行中该看到本次提问: %+v", cur.Messages)
			}
			// 运行中不许再起一轮，前端靠这个把消息排起来
			if code, _ := sendJSON(t, http.MethodPost, srv.URL+"/api/sessions/"+snap.Id+"/messages",
				`{"prompt":"再来一条"}`); code != http.StatusConflict {
				t.Fatalf("运行中再发该 409，实际 %d", code)
			}
			// 收尾
			if code, _ := sendJSON(t, http.MethodPost, srv.URL+"/api/sessions/"+snap.Id+"/cancel", ""); code != http.StatusNoContent {
				t.Fatalf("中断该 204，实际 %d", code)
			}
			return
		}
		time.Sleep(20 * time.Millisecond)
	}
	t.Fatal("没等到它在跑")
}

// 页面本身要能取到：说明 go:embed 打包成功了（漏了 embed 这里就 404）
func TestStaticIndex(t *testing.T) {
	m := newTestManager(t)
	srv := newTestServer(t, m)
	for _, path := range []string{"/", "/app.js", "/style.css",
		"/vendor/bootstrap.min.css", "/vendor/bootstrap.bundle.min.js"} {
		code, body := sendJSON(t, http.MethodGet, srv.URL+path, "")
		if code != http.StatusOK {
			t.Fatalf("%s 该 200，实际 %d", path, code)
		}
		if len(body) == 0 {
			t.Fatalf("%s 是空的", path)
		}
	}
	_, body := sendJSON(t, http.MethodGet, srv.URL+"/", "")
	if !strings.Contains(body, "app.js") {
		t.Fatalf("index.html 里没引到 app.js:\n%s", body)
	}
}

// 输入框上方那三块（待办、后台任务、排队）必须排在同一个容器里、按固定的顺序：
// 以前排队块单独夹在对话区和输入区之间，只有一条 1px 边框和输入区里的后台任务贴着，
// 两块看着就糊成一片。顺序也得是 待办 → 后台任务 → 排队 → 输入框
func TestComposerLayoutOrder(t *testing.T) {
	_, body := sendJSON(t, http.MethodGet, newTestServer(t, newTestManager(t)).URL+"/", "")
	if body == "" {
		t.Fatal("首页是空的")
	}
	// 切成单行，方便按顺序找标记
	flat := strings.NewReplacer("\r", "", "\n", " ").Replace(body)
	at := func(marker string) int {
		i := strings.Index(flat, marker)
		if i < 0 {
			t.Fatalf("index.html 里找不到 %q", marker)
		}
		return i
	}

	composer := at(`id="composer"`)
	todos := at(`id="todos"`)
	tasks := at(`id="tasks"`)
	queue := at(`id="queue"`)
	input := at(`id="input"`)

	for name, pos := range map[string]int{
		"待办清单": todos, "后台任务": tasks, "排队消息": queue, "输入框": input,
	} {
		if pos < composer {
			t.Fatalf("%s 该在输入区容器里，实际在它前面", name)
		}
	}
	if !(todos < tasks && tasks < queue && queue < input) {
		t.Fatalf("底部顺序该是 待办 → 后台任务 → 排队 → 输入框，实际 %d %d %d %d",
			todos, tasks, queue, input)
	}
}

// 启动目录自动算一个工作区，列表里标成默认
func TestWorkspacesDefault(t *testing.T) {
	m := newTestManager(t)
	spaces := m.workspaces()
	if len(spaces) != 1 {
		t.Fatalf("该只有一个工作区，实际 %d 个: %+v", len(spaces), spaces)
	}
	if !spaces[0].Default {
		t.Fatalf("启动目录应该是默认工作区: %+v", spaces[0])
	}
	if spaces[0].Path != m.workdir {
		t.Fatalf("工作区路径该是 %s，实际 %s", m.workdir, spaces[0].Path)
	}
}

// 加一个工作区：新会话落在那个目录里，两个工作区的会话列表互不串门
func TestWorkspacesAddAndIsolate(t *testing.T) {
	m := newTestManager(t)
	srv := newTestServer(t, m)

	other := filepath.Join(t.TempDir(), "另一个项目")
	if err := os.MkdirAll(other, 0o755); err != nil {
		t.Fatal(err)
	}
	code, body := sendJSON(t, http.MethodPost, srv.URL+"/api/workspaces",
		fmt.Sprintf(`{"path":%q}`, other))
	if code != http.StatusOK {
		t.Fatalf("加工作区该 200，实际 %d: %s", code, body)
	}
	var info workspaceInfo
	if err := json.Unmarshal([]byte(body), &info); err != nil {
		t.Fatalf("返回的不是工作区信息: %v: %s", err, body)
	}
	if info.Path != other || info.Id == "" || info.Default {
		t.Fatalf("工作区信息不对: %+v", info)
	}

	// 在新工作区里开一条会话
	code, body = sendJSON(t, http.MethodPost, srv.URL+"/api/sessions",
		fmt.Sprintf(`{"workspace":%q}`, info.Id))
	if code != http.StatusOK {
		t.Fatalf("在新工作区建会话该 200，实际 %d: %s", code, body)
	}
	var snap snapshot
	if err := json.Unmarshal([]byte(body), &snap); err != nil {
		t.Fatalf("返回的不是会话快照: %v: %s", err, body)
	}
	if snap.WorkDir != other {
		t.Fatalf("新会话该在新工作区里干活，实际 %s", snap.WorkDir)
	}

	// 列表按工作区分开：默认那个还是空的，新工作区里有一条
	code, body = sendJSON(t, http.MethodGet, srv.URL+"/api/sessions", "")
	if code != http.StatusOK || strings.TrimSpace(body) != "[]" {
		t.Fatalf("默认工作区该没有会话，实际 %d: %s", code, body)
	}
	code, body = sendJSON(t, http.MethodGet, srv.URL+"/api/sessions?ws="+info.Id, "")
	if code != http.StatusOK {
		t.Fatalf("按工作区拉列表该 200，实际 %d: %s", code, body)
	}
	var infos []sessionInfo
	if err := json.Unmarshal([]byte(body), &infos); err != nil {
		t.Fatalf("返回的不是会话列表: %v: %s", err, body)
	}
	if len(infos) != 1 || infos[0].Id != snap.Id {
		t.Fatalf("新工作区该正好有那一条会话，实际 %+v", infos)
	}
}

// 路径不存在、不是目录、工作区 id 不认识，都得老实报错
func TestWorkspacesBadInput(t *testing.T) {
	m := newTestManager(t)
	srv := newTestServer(t, m)

	missing := filepath.Join(t.TempDir(), "不存在的目录")
	if code, body := sendJSON(t, http.MethodPost, srv.URL+"/api/workspaces",
		fmt.Sprintf(`{"path":%q}`, missing)); code != http.StatusBadRequest {
		t.Fatalf("不存在的目录该 400，实际 %d: %s", code, body)
	}

	file := filepath.Join(t.TempDir(), "a.txt")
	if err := os.WriteFile(file, []byte("x"), 0o644); err != nil {
		t.Fatal(err)
	}
	if code, body := sendJSON(t, http.MethodPost, srv.URL+"/api/workspaces",
		fmt.Sprintf(`{"path":%q}`, file)); code != http.StatusBadRequest {
		t.Fatalf("文件不是目录，该 400，实际 %d: %s", code, body)
	}

	if code, body := sendJSON(t, http.MethodGet, srv.URL+"/api/sessions?ws=nope", ""); code != http.StatusNotFound {
		t.Fatalf("不认识的工作区该 404，实际 %d: %s", code, body)
	}
	if code, body := sendJSON(t, http.MethodPost, srv.URL+"/api/sessions", `{"workspace":"nope"}`); code != http.StatusNotFound {
		t.Fatalf("往不认识的工作区建会话该 404，实际 %d: %s", code, body)
	}
}
