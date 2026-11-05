package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 老版本把 config.yaml 放在项目目录里，启动时会复制到用户目录；
// 新位置已经有了就不再动它，免得把人手改的配置又盖回去
func TestLoadConfigMigratesLegacyFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows 上 os.UserHomeDir 读的是这个

	work := t.TempDir()
	t.Chdir(work)
	legacy := filepath.Join(work, "config.yaml")
	if err := os.WriteFile(legacy, []byte("Mode: read\nMaxSteps: 7\n"), 0o644); err != nil {
		t.Fatalf("准备老配置失败: %v", err)
	}

	path := filepath.Join(home, ".agent-dev", "config.yaml")
	workdir, cfg, notice, err := loadConfig(path)
	if err != nil {
		t.Fatalf("加载配置失败: %v", err)
	}
	if workdir != work {
		t.Fatalf("工作目录应该是 %s，实际 %s", work, workdir)
	}
	if cfg.Mode != "read" || cfg.Steps() != 7 {
		t.Fatalf("迁移过来的配置没读到: %+v", cfg)
	}
	if !strings.Contains(notice, "复制") {
		t.Fatalf("应该说明把老配置搬过来了: %q", notice)
	}
	// 只复制不删除：老文件里可能存着密钥，留着当备份
	if _, err := os.Stat(legacy); err != nil {
		t.Fatalf("老配置应该留着: %v", err)
	}

	// 新位置已经有配置时不再迁移，也不提示，内容以新位置的为准
	if err := os.WriteFile(path, []byte("Mode: full\n"), 0o644); err != nil {
		t.Fatalf("改写新配置失败: %v", err)
	}
	if _, cfg, notice, err := loadConfig(path); err != nil {
		t.Fatalf("第二次加载失败: %v", err)
	} else if cfg.Mode != "full" || notice != "" {
		t.Fatalf("新位置的配置不该被覆盖: mode=%q notice=%q", cfg.Mode, notice)
	}
}

// 第一次跑的时候用户目录下还没有 .agent-dev，目录得自己建出来
func TestLoadConfigCreatesHomeFile(t *testing.T) {
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home)
	t.Chdir(t.TempDir()) // 这个目录里没有老配置

	path := filepath.Join(home, ".agent-dev", "config.yaml")
	if _, _, notice, err := loadConfig(path); err != nil {
		t.Fatalf("加载配置失败: %v", err)
	} else if notice != "" {
		t.Fatalf("没有老配置就不该有提示: %q", notice)
	}
	if _, err := os.Stat(path); err != nil {
		t.Fatalf("配置应该被创建出来: %v", err)
	}
}
