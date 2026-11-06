package webui

import (
	"encoding/json"
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hamusuta/agent-dev/src/domain"
	"hamusuta/agent-dev/src/tool"
)

// 会话状态：没跑过是 idle，跑着是 running，跑完是 done，出错/中断是 waiting，
// 有确认挂着也是 waiting
func TestSessionStatus(t *testing.T) {
	m := newTestManager(t)
	s := m.create()

	if got := s.Status(); got != statusIdle {
		t.Fatalf("新会话该是 idle，实际 %q", got)
	}

	// 跑着：running
	withFakeProvider(t, &fakeProvider{steps: []fakeStep{{content: "好"}}})
	if err := s.Start(m.Config(), "你好", nil); err != nil {
		t.Fatalf("起一轮失败: %v", err)
	}
	if got := s.Status(); got != statusRunning {
		t.Fatalf("跑着该是 running，实际 %q", got)
	}

	// 等它跑完：done
	waitIdle(t, s)
	if got := s.Status(); got != statusDone {
		t.Fatalf("跑完该是 done，实际 %q", got)
	}

	// 中断：waiting（等着人决定下一步）
	withFakeProvider(t, &fakeProvider{steps: []fakeStep{{content: "慢慢来", wait: true}}})
	if err := s.Start(m.Config(), "再问一句", nil); err != nil {
		t.Fatalf("起一轮失败: %v", err)
	}
	if !s.Cancel() {
		t.Fatal("这一轮该能中断")
	}
	waitIdle(t, s)
	if got := s.Status(); got != statusWaiting {
		t.Fatalf("被中断之后该是 waiting，实际 %q", got)
	}
}

// waitIdle 等这条会话跑完（最多 5 秒）
func waitIdle(t *testing.T, s *Session) {
	t.Helper()
	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) && s.Running() {
		time.Sleep(10 * time.Millisecond)
	}
	if s.Running() {
		t.Fatal("这一轮没停下来")
	}
}

// running/waiting 的判定只看 approvals 有没有挂着
func TestSessionStatusWaitingOnApproval(t *testing.T) {
	m := newTestManager(t)
	s := m.create()
	s.mu.Lock()
	s.approvals["ap-0"] = make(chan domain.ApprovalAnswer, 1)
	s.mu.Unlock()
	if got := s.Status(); got != statusWaiting {
		t.Fatalf("有确认挂着该是 waiting，实际 %q", got)
	}
}

// 列表要给每条会话带上状态：转圈/黄/绿的依据就在这儿
func TestListIncludesStatus(t *testing.T) {
	m := newTestManager(t)
	ws := m.defaultSpace()
	s := m.createIn(ws)
	if err := ws.Store.Save(s.Data()); err != nil {
		t.Fatalf("保存会话失败: %v", err)
	}
	// 再存一条"以前聊过"的：内存里没挂着，磁盘上有内容 → done
	old := domain.NewSession(ws.Path)
	old.AppendUser("很久以前")
	if err := ws.Store.Save(old); err != nil {
		t.Fatalf("保存老会话失败: %v", err)
	}

	infos, err := m.listIn(ws)
	if err != nil {
		t.Fatalf("列会话失败: %v", err)
	}
	got := make(map[string]string, len(infos))
	for _, info := range infos {
		got[info.Id] = info.Status
	}
	if got[s.Data().Id] != statusIdle {
		t.Fatalf("刚建的会话该是 idle，实际 %q", got[s.Data().Id])
	}
	if got[old.Id] != statusDone {
		t.Fatalf("磁盘上的老会话该是 done，实际 %q", got[old.Id])
	}
}

// 删会话：带 purge 才真删文件，不带只摘内存（老的 TestHTTPDeleteSession 验的是后一半）
func TestHTTPDeleteSessionWithPurge(t *testing.T) {
	m := newTestManager(t)
	srv := newTestServer(t, m)
	ws := m.defaultSpace()

	// 不带 purge：文件还在
	keep := m.createIn(ws)
	if err := ws.Store.Save(keep.Data()); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if code, body := sendJSON(t, http.MethodDelete, srv.URL+"/api/sessions/"+keep.Data().Id, ""); code != http.StatusNoContent {
		t.Fatalf("删会话该 204，实际 %d: %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(ws.Store.Dir, keep.Data().Id+".json")); err != nil {
		t.Fatalf("不带 purge 时文件该还在: %v", err)
	}

	// 带 purge：文件也没了
	gone := m.createIn(ws)
	if err := ws.Store.Save(gone.Data()); err != nil {
		t.Fatalf("保存失败: %v", err)
	}
	if code, body := sendJSON(t, http.MethodDelete, srv.URL+"/api/sessions/"+gone.Data().Id+"?purge=1", ""); code != http.StatusNoContent {
		t.Fatalf("带 purge 删会话该 204，实际 %d: %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(ws.Store.Dir, gone.Data().Id+".json")); !os.IsNotExist(err) {
		t.Fatalf("带 purge 时文件该没了: %v", err)
	}
}

// 删工作区：仅移除展示 / 连同文件删除，两种都要从列表里消失
func TestHTTPDeleteWorkspace(t *testing.T) {
	m := newTestManager(t)
	srv := newTestServer(t, m)

	// 加两个工作区来删
	dir1 := t.TempDir()
	dir2 := t.TempDir()
	for _, dir := range []string{dir1, dir2} {
		if code, body := sendJSON(t, http.MethodPost, srv.URL+"/api/workspaces",
			`{"path":"`+strings.ReplaceAll(dir, `\`, `\\`)+`"}`); code != http.StatusOK {
			t.Fatalf("加工作区该 200，实际 %d: %s", code, body)
		}
	}
	spaces := m.workspaces()
	var id1, id2 string
	for _, ws := range spaces {
		switch ws.Path {
		case dir1:
			id1 = ws.Id
		case dir2:
			id2 = ws.Id
		}
	}
	if id1 == "" || id2 == "" {
		t.Fatalf("没找到刚加的两个工作区: %+v", spaces)
	}
	// 给 id2 存一条会话，好验证"搬走但没删"
	ws2, ok := m.space(id2)
	if !ok {
		t.Fatalf("取不到工作区 %s", id2)
	}
	saved := domain.NewSession(dir2)
	saved.AppendUser("留着")
	if err := ws2.Store.Save(saved); err != nil {
		t.Fatalf("保存会话失败: %v", err)
	}

	// id1：仅移除展示
	code, body := sendJSON(t, http.MethodDelete, srv.URL+"/api/workspaces/"+id1, "")
	if code != http.StatusOK {
		t.Fatalf("删工作区该 200，实际 %d: %s", code, body)
	}
	var resp struct {
		Purged bool   `json:"purged"`
		Dest   string `json:"dest"`
	}
	if err := json.Unmarshal([]byte(body), &resp); err != nil {
		t.Fatalf("回包不是 JSON: %v", err)
	}
	if resp.Purged {
		t.Fatal("不带 purge 时不该标成已清理")
	}
	if !strings.Contains(resp.Dest, ".trash") {
		t.Fatalf("该搬进 .trash，实际 %s", resp.Dest)
	}

	// id2：连同文件删除
	if code, body := sendJSON(t, http.MethodDelete, srv.URL+"/api/workspaces/"+id2+"?purge=1", ""); code != http.StatusOK {
		t.Fatalf("purge 删工作区该 200，实际 %d: %s", code, body)
	}
	if _, err := os.Stat(filepath.Join(m.root, id2)); !os.IsNotExist(err) {
		t.Fatalf("purge 之后会话目录该没了: %v", err)
	}

	// 两个都不在列表里了；工作目录本身还在
	for _, ws := range m.workspaces() {
		if ws.Id == id1 || ws.Id == id2 {
			t.Fatalf("删掉的工作区不该还在列表里: %+v", m.workspaces())
		}
	}
	for _, dir := range []string{dir1, dir2} {
		if _, err := os.Stat(dir); err != nil {
			t.Fatalf("工作目录本身不该被删: %v", err)
		}
	}
}

// 有会话在跑的工作区不给删：那一轮还在往目录里写文件
func TestHTTPDeleteWorkspaceBusy(t *testing.T) {
	m := newTestManager(t)
	withFakeProvider(t, &fakeProvider{steps: []fakeStep{{content: "好", wait: true}}})
	srv := newTestServer(t, m)
	ws := m.defaultSpace()
	s := m.createIn(ws)
	if err := s.Start(m.Config(), "慢慢跑", nil); err != nil {
		t.Fatalf("起一轮失败: %v", err)
	}
	defer stopSession(t, s)

	if code, body := sendJSON(t, http.MethodDelete, srv.URL+"/api/workspaces/"+ws.Id, ""); code != http.StatusConflict {
		t.Fatalf("跑着的时候该 409，实际 %d: %s", code, body)
	}
}

// runtime 接口：待办清单按会话归，后台任务按会话过滤
func TestRuntimeView(t *testing.T) {
	m := newTestManager(t)
	srv := newTestServer(t, m)
	s := m.create()

	code, body := sendJSON(t, http.MethodGet, srv.URL+"/api/sessions/"+s.Data().Id+"/runtime", "")
	if code != http.StatusOK {
		t.Fatalf("拉 runtime 该 200，实际 %d: %s", code, body)
	}
	var rt runtimeView
	if err := json.Unmarshal([]byte(body), &rt); err != nil {
		t.Fatalf("不是 JSON: %v", err)
	}
	if rt.TodoTotal != 0 || len(rt.Tasks) != 0 {
		t.Fatalf("刚开始两样都该是空的: %+v", rt)
	}
	if rt.Status != statusIdle {
		t.Fatalf("状态该一并带上，实际 %q", rt.Status)
	}

	// 假装这一轮里模型列了清单
	s.emit(domain.Event{
		Kind: domain.EventToolCall,
		Call: domain.ToolCall{Id: "c1", Name: "TodoWrite",
			Arguments: `{"intent":"列计划","todos":[` +
				`{"content":"看代码","status":"completed"},` +
				`{"content":"写改动","status":"in_progress"}]}`},
	})
	_, body = sendJSON(t, http.MethodGet, srv.URL+"/api/sessions/"+s.Data().Id+"/runtime", "")
	if err := json.Unmarshal([]byte(body), &rt); err != nil {
		t.Fatalf("不是 JSON: %v", err)
	}
	if rt.TodoTotal != 2 || rt.TodoDone != 1 {
		t.Fatalf("清单该是 1/2 完成，实际 %+v", rt)
	}
	if len(rt.Todos) != 2 || rt.Todos[1].Content != "写改动" {
		t.Fatalf("清单内容不对: %+v", rt.Todos)
	}

	// 待办是进程级的一份，另一个会话不该看见这条的
	other := m.create()
	if got := other.Todos(); len(got) != 0 {
		t.Fatalf("别的会话不该看见这份清单: %+v", got)
	}
}

// 后台任务按会话过滤：结果里带 task_id=task_N 的才算这条会话起的
func TestRuntimeTasksBelongToSession(t *testing.T) {
	m := newTestManager(t)
	s := m.create()

	if got := taskIdsIn("已在后台启动 task_7（后台，超时 1m）\n命令: sleep 5\n用 Task action=output/wait/stop 跟进，task_id=task_7"); len(got) != 1 || got[0] != "task_7" {
		t.Fatalf("该认出 task_7，实际 %v", got)
	}
	if got := taskIdsIn("exit_code: 0\nmain.go"); len(got) != 0 {
		t.Fatalf("普通结果不该认出任务: %v", got)
	}

	s.emit(domain.Event{
		Kind: domain.EventToolResult,
		Text: "已在后台启动 task_3（后台）\ntask_id=task_3",
	})
	if ids := s.TaskIds(); len(ids) != 1 || ids[0] != "task_3" {
		t.Fatalf("该记下 task_3，实际 %v", ids)
	}
	// 重复出现不重复记
	s.emit(domain.Event{
		Kind: domain.EventToolResult,
		Text: "用 Task 跟进，task_id=task_3",
	})
	if ids := s.TaskIds(); len(ids) != 1 {
		t.Fatalf("同一个任务不该记两遍，实际 %v", ids)
	}
}

// 参数写坏时不能说成"清空"：ok=false 是"别动原来那份"，不是"清单空了"
func TestParseTodos(t *testing.T) {
	got, ok := tool.ParseTodoArgs(`{"todos":[{"content":"a","status":"pending"}]}`)
	if !ok || len(got) != 1 || got[0].Content != "a" {
		t.Fatalf("解析不对: %+v %v", got, ok)
	}
	if _, ok := tool.ParseTodoArgs(""); ok {
		t.Fatal("空参数该说解析不出来")
	}
	if _, ok := tool.ParseTodoArgs("{坏掉的"); ok {
		t.Fatal("坏参数该说解析不出来")
	}
	if _, ok := tool.ParseTodoArgs(`{"intent":"只传了意图"}`); ok {
		t.Fatal("没带 todos 字段该说解析不出来，不能当成清空")
	}
	// 显式空数组才是"清空清单"
	got, ok = tool.ParseTodoArgs(`{"todos":[]}`)
	if !ok || len(got) != 0 {
		t.Fatalf("空数组该解析成空清单: %+v %v", got, ok)
	}
}

// 待办状态用的是 tool 包那三个词，别自己另发明一套
func TestTodoStatusWords(t *testing.T) {
	if tool.TodoCompleted != "completed" || tool.TodoInProgress != "in_progress" {
		t.Fatalf("待办状态词变了，前端和 CLI 都靠它们: %v %v", tool.TodoCompleted, tool.TodoInProgress)
	}
}
