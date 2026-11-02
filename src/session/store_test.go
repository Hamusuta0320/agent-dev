package session

import (
	"path/filepath"
	"testing"

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
