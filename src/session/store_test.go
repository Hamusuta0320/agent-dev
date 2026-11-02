package session

import (
	"os"
	"path/filepath"
	"testing"
	"time"

	"hamusuta/agent-dev/src/domain"
)

func TestSaveAndLoad(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))

	s := domain.NewSession(`D:\code\demo`)
	s.AppendUser("跑下测试")
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
