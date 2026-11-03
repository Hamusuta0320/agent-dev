package session

import (
	"encoding/json"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"

	"hamusuta/agent-dev/src/domain"
)

func TestSaveAndLoad(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	img, err := domain.NewImage([]byte("\x89PNG\r\n\x1a\n 假装这是截图"))
	if err != nil {
		t.Fatalf("造图片失败: %v", err)
	}

	s := domain.NewSession(`D:\code\demo`)
	s.AppendUser("跑下测试", img)
	s.AppendAssistant("我看看", "先读文件", []domain.ToolCall{{Id: "call_1", Name: "Read", Arguments: `{"path":"a.go"}`}})
	s.AppendToolResult(domain.ToolCall{Id: "call_1", Name: "Read"}, "1→package a")
	s.AppendAssistant("改好了", "收尾", nil)

	if err := store.Save(s); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	got, err := store.Load(s.Id)
	if err != nil {
		t.Fatalf("读取失败: %v", err)
	}
	if got.Id != s.Id || got.WorkDir != s.WorkDir {
		t.Fatalf("会话基本信息不对: %+v", got)
	}
	if len(got.Messages) != 4 {
		t.Fatalf("应该有 4 条消息，实际 %d", len(got.Messages))
	}
	// 图片跟着会话存下来了，恢复会话之后还能接着问
	if len(got.Messages[0].Images) != 1 || got.Messages[0].Images[0].Data != img.Data {
		t.Fatalf("图片没存住: %+v", got.Messages[0])
	}
	if got.Messages[1].Reasoning != "先读文件" || got.Messages[1].ToolCalls[0].Name != "Read" {
		t.Fatalf("思考或工具调用丢了: %+v", got.Messages[1])
	}
	if got.Messages[2].ToolName != "Read" || got.Messages[2].ToolCallId != "call_1" {
		t.Fatalf("工具结果丢了: %+v", got.Messages[2])
	}

	ids, err := store.List()
	if err != nil || len(ids) != 1 || ids[0] != s.Id {
		t.Fatalf("会话列表不对: %v %v", ids, err)
	}
}

func TestLoadRejectsBadId(t *testing.T) {
	store := NewStore(t.TempDir())
	if _, err := store.Load("../config"); err == nil {
		t.Fatal("跨目录的 id 应该被拒绝")
	}
}

// /resume 的列表要按修改时间从新到旧排，最新的排最前
func TestListInfoNewestFirst(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	ids := []string{"20240101-100000-0001", "20240102-100000-0002", "20240103-100000-0003"}
	for i, id := range ids {
		s := &domain.Session{Id: id}
		s.AppendUser("第 " + id + " 段对话")
		if err := store.Save(s); err != nil {
			t.Fatalf("保存失败: %v", err)
		}
		// 写入几乎是同一瞬间，显式改一下修改时间，排序才有确定性
		when := time.Date(2024, 1, i+1, 10, 0, 0, 0, time.UTC)
		if err := os.Chtimes(filepath.Join(store.Dir, id+".json"), when, when); err != nil {
			t.Fatalf("改修改时间失败: %v", err)
		}
	}

	infos, err := store.ListInfo()
	if err != nil {
		t.Fatalf("列会话摘要失败: %v", err)
	}
	if len(infos) != len(ids) {
		t.Fatalf("应该有 %d 个会话，实际 %d 个", len(ids), len(infos))
	}
	for i, info := range infos {
		want := ids[len(ids)-1-i] // 最新的在最前
		if info.Id != want {
			t.Fatalf("第 %d 个应该是 %s，实际 %s", i, want, info.Id)
		}
		if info.Size <= 0 {
			t.Fatalf("会话 %s 的大小不对: %d", info.Id, info.Size)
		}
	}
}

// 目录还没建出来时列表是空的，不该报错
func TestListInfoMissingDir(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "not-created"))
	infos, err := store.ListInfo()
	if err != nil || len(infos) != 0 {
		t.Fatalf("空目录应该返回空列表: %v %v", infos, err)
	}
}

// 工作区 id 由目录路径决定：同一个目录永远得到同一个 id，不同目录不会撞
func TestWorkspaceId(t *testing.T) {
	root := t.TempDir()
	a := filepath.Join(root, "demo")
	b := filepath.Join(root, "other")

	idA, err := WorkspaceId(a)
	if err != nil {
		t.Fatalf("算工作区 id 失败: %v", err)
	}
	again, err := WorkspaceId(a)
	if err != nil || again != idA {
		t.Fatalf("同一个目录应该得到同一个 id: %q vs %q", idA, again)
	}
	// 相对路径、带 . 的写法都该归到同一个工作区
	messy := filepath.Join(a, ".", "..", "demo")
	if id, err := WorkspaceId(messy); err != nil || id != idA {
		t.Fatalf("%q 应该和 %q 是同一个工作区: %q %v", messy, a, id, err)
	}

	idB, err := WorkspaceId(b)
	if err != nil || idB == idA {
		t.Fatalf("不同目录应该是不同工作区: %q %q", idA, idB)
	}

	// id 要能直接当目录名：不带分隔符，前面留着目内名
	if strings.ContainsAny(idA, `/\:`) {
		t.Fatalf("id 不该带路径分隔符: %q", idA)
	}
	if !strings.HasPrefix(idA, "demo-") {
		t.Fatalf("id 应该带上目录名方便辨认: %q", idA)
	}
}

// 会话目录落在用户目录下，按工作区分子目录
func TestDefaultDirUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows 上 os.UserHomeDir 读的是这个

	work := filepath.Join(t.TempDir(), "myproj")
	dir, err := DefaultDir(work)
	if err != nil {
		t.Fatalf("算会话目录失败: %v", err)
	}
	id, err := WorkspaceId(work)
	if err != nil {
		t.Fatalf("算工作区 id 失败: %v", err)
	}
	want := filepath.Join(home, ".agent-dev", "sessions", id)
	if dir != want {
		t.Fatalf("会话目录应该是 %q，实际 %q", want, dir)
	}

	// 别的目录启动就是另一个工作区目录，互相看不到
	other, err := DefaultDir(filepath.Join(t.TempDir(), "another"))
	if err != nil || other == dir {
		t.Fatalf("不同目录应该分开放的: %q %q %v", other, dir, err)
	}
}

// 工作区信息记在会话目录里，列会话时不能把它当成一条会话
func TestSaveWorkspaceNotListed(t *testing.T) {
	dir := filepath.Join(t.TempDir(), "sessions")
	store := NewStore(dir)
	work := t.TempDir()
	if err := store.SaveWorkspace(work); err != nil {
		t.Fatalf("保存工作区信息失败: %v", err)
	}

	data, err := os.ReadFile(filepath.Join(dir, workspaceFile))
	if err != nil {
		t.Fatalf("工作区信息文件没写出来: %v", err)
	}
	var ws Workspace
	if err := json.Unmarshal(data, &ws); err != nil {
		t.Fatalf("工作区信息解析失败: %v", err)
	}
	if ws.Path != filepath.Clean(work) {
		t.Fatalf("工作区路径不对: %q vs %q", ws.Path, filepath.Clean(work))
	}
	if ws.Id != "sessions" {
		t.Fatalf("工作区 id 应该是会话目录名: %q", ws.Id)
	}

	ids, err := store.List()
	if err != nil || len(ids) != 0 {
		t.Fatalf("工作区信息不该出现在会话列表里: %v %v", ids, err)
	}
	infos, err := store.ListInfo()
	if err != nil || len(infos) != 0 {
		t.Fatalf("工作区信息不该出现在会话摘要里: %v %v", infos, err)
	}
}

// 老目录里的会话复制过来，原件留着；重复迁移不会重复复制，也不会覆盖新的
func TestMigrate(t *testing.T) {
	root := t.TempDir()
	oldDir := filepath.Join(root, "old")
	newDir := filepath.Join(root, "new")
	if err := os.MkdirAll(oldDir, 0o755); err != nil {
		t.Fatalf("准备老目录失败: %v", err)
	}

	old := &domain.Session{Id: "20240101-100000-0001"}
	old.AppendUser("老会话说的话")
	oldStore := NewStore(oldDir)
	if err := oldStore.Save(old); err != nil {
		t.Fatalf("保存老会话失败: %v", err)
	}

	n, err := Migrate(oldDir, newDir)
	if err != nil || n != 1 {
		t.Fatalf("应该迁过来 1 个，实际 %d 个: %v", n, err)
	}
	got, err := NewStore(newDir).Load(old.Id)
	if err != nil {
		t.Fatalf("迁移后读不到会话: %v", err)
	}
	if len(got.Messages) != 1 || got.Messages[0].Content != "老会话说的话" {
		t.Fatalf("迁移的内容不对: %+v", got.Messages)
	}
	if _, err := os.Stat(filepath.Join(oldDir, old.Id+".json")); err != nil {
		t.Fatalf("老文件应该留着: %v", err)
	}

	// 再跑一次不会重复复制
	if n, err := Migrate(oldDir, newDir); err != nil || n != 0 {
		t.Fatalf("重复迁移不该再复制: %d %v", n, err)
	}
	// 新目录里已有的文件不会被老文件盖掉
	newer := &domain.Session{Id: old.Id, WorkDir: "新内容"}
	newer.AppendUser("新会话说的话")
	if err := NewStore(newDir).Save(newer); err != nil {
		t.Fatalf("保存新会话失败: %v", err)
	}
	if _, err := Migrate(oldDir, newDir); err != nil {
		t.Fatalf("迁移失败: %v", err)
	}
	if got, err := NewStore(newDir).Load(old.Id); err != nil || got.Messages[0].Content != "新会话说的话" {
		t.Fatalf("同 id 的新会话不该被老文件覆盖: %+v %v", got, err)
	}
}

// 老目录不存在（第一次在新版本里启动）时什么都不做
func TestMigrateMissingOldDir(t *testing.T) {
	root := t.TempDir()
	n, err := Migrate(filepath.Join(root, "nope"), filepath.Join(root, "new"))
	if err != nil || n != 0 {
		t.Fatalf("老目录不存在应该安静跳过: %d %v", n, err)
	}
	if _, err := os.Stat(filepath.Join(root, "new")); err == nil {
		t.Fatal("没事可迁时不该建目录")
	}
}
