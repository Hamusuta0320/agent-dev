package session

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hamusuta/agent-dev/src/domain"
)

// 删会话就是删那个文件，文件不在也不算错
func TestStoreDelete(t *testing.T) {
	store := NewStore(filepath.Join(t.TempDir(), "sessions"))
	s := domain.NewSession(".")
	s.AppendUser("聊两句")
	if err := store.Save(s); err != nil {
		t.Fatalf("保存失败: %v", err)
	}

	ok, err := store.Delete(s.Id)
	if err != nil || !ok {
		t.Fatalf("第一次删该成功: %v %v", ok, err)
	}
	if _, err := os.Stat(filepath.Join(store.Dir, s.Id+".json")); !os.IsNotExist(err) {
		t.Fatalf("文件该没了: %v", err)
	}
	// 再删一次：文件本来就不在，不算错误
	ok, err = store.Delete(s.Id)
	if err != nil || ok {
		t.Fatalf("重复删该是 ok=false 且不报错: %v %v", ok, err)
	}

	// 跨目录的 id 挡住
	if _, err := store.Delete("../evil"); err == nil {
		t.Fatal("带路径的 id 该报错")
	}
}

// 工作区删除两种模式：仅移除展示是搬进 .trash，purge 是真删
func TestRemoveWorkspace(t *testing.T) {
	root := t.TempDir()
	id := "demo-abc123"
	dir := filepath.Join(root, id)
	store := NewStore(dir)
	s := domain.NewSession(".")
	s.AppendUser("这条会话要留着")
	if err := store.SaveWorkspace("."); err != nil {
		t.Fatalf("写工作区信息失败: %v", err)
	}
	if err := store.Save(s); err != nil {
		t.Fatalf("保存会话失败: %v", err)
	}

	// 仅移除展示：目录搬进 .trash，会话文件还在，而且不再出现在工作区列表里
	dest, err := RemoveWorkspace(root, id, false)
	if err != nil {
		t.Fatalf("移除工作区失败: %v", err)
	}
	if _, err := os.Stat(dir); !os.IsNotExist(err) {
		t.Fatalf("原目录该没了: %v", err)
	}
	if _, err := os.Stat(filepath.Join(dest, s.Id+".json")); err != nil {
		t.Fatalf("搬过去的会话文件该还在: %v", err)
	}
	if !strings.HasPrefix(dest, filepath.Join(root, trashDir)) {
		t.Fatalf("该搬进 %s，实际 %s", trashDir, dest)
	}
	list, err := ListWorkspaces(root)
	if err != nil {
		t.Fatalf("列工作区失败: %v", err)
	}
	for _, ws := range list {
		if ws.Id == id {
			t.Fatalf(".trash 里的那份不该再被当成工作区: %+v", list)
		}
		if ws.Id == trashDir {
			t.Fatalf("%s 自己也不该被当成工作区: %+v", trashDir, list)
		}
	}

	// 再来一遍 purge：这次真删
	dir2 := filepath.Join(root, "demo2-def456")
	store2 := NewStore(dir2)
	if err := store2.SaveWorkspace("."); err != nil {
		t.Fatalf("写工作区信息失败: %v", err)
	}
	if err := store2.Save(s); err != nil {
		t.Fatalf("保存会话失败: %v", err)
	}
	if _, err := RemoveWorkspace(root, "demo2-def456", true); err != nil {
		t.Fatalf("purge 删工作区失败: %v", err)
	}
	if _, err := os.Stat(dir2); !os.IsNotExist(err) {
		t.Fatalf("purge 之后目录该没了: %v", err)
	}

	// 找不到的工作区要说清楚
	if _, err := RemoveWorkspace(root, "nope-hash", false); err == nil {
		t.Fatal("不存在的工作区该报错")
	}
	// 非法 id 挡住
	for _, bad := range []string{"", "..", "a/b", `.hidden`} {
		if _, err := RemoveWorkspace(root, bad, false); err == nil {
			t.Fatalf("id %q 该被挡住", bad)
		}
	}
}

// 只删会话目录，绝不碰工作目录本身
func TestRemoveWorkspaceKeepsWorkDir(t *testing.T) {
	root := t.TempDir()
	work := t.TempDir()
	if err := os.WriteFile(filepath.Join(work, "main.go"), []byte("package main"), 0o644); err != nil {
		t.Fatalf("准备文件失败: %v", err)
	}
	id, err := WorkspaceId(work)
	if err != nil {
		t.Fatalf("算工作区 id 失败: %v", err)
	}
	store := NewStore(filepath.Join(root, id))
	if err := store.SaveWorkspace(work); err != nil {
		t.Fatalf("写工作区信息失败: %v", err)
	}

	if _, err := RemoveWorkspace(root, id, true); err != nil {
		t.Fatalf("删工作区失败: %v", err)
	}
	if _, err := os.Stat(filepath.Join(work, "main.go")); err != nil {
		t.Fatalf("工作目录里的文件不该被删: %v", err)
	}
}
