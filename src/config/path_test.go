package config

import (
	"path/filepath"
	"testing"
)

// 配置跟着人走：路径落在用户目录下，和启动时所在的工作目录无关
func TestDefaultPathUnderHome(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows 上 os.UserHomeDir 读的是这个

	path, err := DefaultPath()
	if err != nil {
		t.Fatalf("取配置路径失败: %v", err)
	}
	want := filepath.Join(home, ".agent-dev", "config.yaml")
	if path != want {
		t.Fatalf("配置应该在 %s，实际 %s", want, path)
	}

	// 和会话放在同一棵树下，只是各占一层
	dir, err := DefaultDir()
	if err != nil {
		t.Fatalf("取配置目录失败: %v", err)
	}
	if dir != filepath.Dir(path) {
		t.Fatalf("目录 %s 和文件 %s 不在同一层", dir, path)
	}
}

// 老位置就是启动目录（项目目录）里的 config.yaml
func TestLegacyPath(t *testing.T) {
	work := t.TempDir()
	want := filepath.Join(work, "config.yaml")
	if got := LegacyPath(work); got != want {
		t.Fatalf("老配置路径应该是 %s，实际 %s", want, got)
	}
}
