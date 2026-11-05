package main

import (
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"strings"

	"hamusuta/agent-dev/src/config"
	"hamusuta/agent-dev/src/domain"
	"hamusuta/agent-dev/src/session"
	"hamusuta/agent-dev/src/tool"
	"hamusuta/agent-dev/src/util"

	tea "charm.land/bubbletea/v2"
	"gopkg.in/yaml.v3"
)

func main() {
	// 先把控制台切到 UTF-8，否则 Windows 下界面里的中文会乱码
	useUTF8Console()

	cfgPath, err := config.DefaultPath()
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	workdir, cfg, cfgNotice, err := loadConfig(cfgPath)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	cur := domain.NewSession(workdir)
	store, notice, err := openStore(workdir)
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}
	m := newModel(cfg, store, workdir, cur)
	m.cfgPath = cfgPath
	m.notice = joinNotice(notice, cfgNotice)

	p := tea.NewProgram(m)
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "界面启动失败", err)
		os.Exit(1)
	}
	// 退出时把还没跑完的后台任务一起收掉，别留一堆看不见的进程
	tool.StopAllTasks()
}

// openStore 准备当前工作区的会话目录：会话统一放在用户目录下，按工作区分开。
// 顺便把老版本留在项目里的 <workDir>/.agent/sessions 迁过来（只复制，不删原件），
// 返回一句要显示给用户的说明。
func openStore(workdir string) (*session.Store, string, error) {
	dir, err := session.DefaultDir(workdir)
	if err != nil {
		return nil, "", fmt.Errorf("准备会话目录失败: %w", err)
	}
	store := session.NewStore(dir)
	// 工作区信息只是方便排查，写不进去不该拦住启动
	_ = store.SaveWorkspace(workdir)

	notice := ""
	n, err := session.Migrate(session.LegacyDir(workdir), dir)
	if err != nil {
		notice = fmt.Sprintf("迁移老会话时出了点问题: %v", err)
	} else if n > 0 {
		notice = fmt.Sprintf("已把 %d 个老会话从 %s 复制过来，/resume 能看到",
			n, session.LegacyDir(workdir))
	}
	return store, notice, nil
}

// joinNotice 把几条要顺带说一句的事情拼成一句，空着的跳过
func joinNotice(parts ...string) string {
	var kept []string
	for _, p := range parts {
		if strings.TrimSpace(p) != "" {
			kept = append(kept, p)
		}
	}
	return strings.Join(kept, "\n")
}

// migrateConfig 把老版本留在项目目录里的 config.yaml 复制到新位置，返回要提示的话。
//
// 只在目标还不存在时复制，且只复制不删除：里面存着密钥，搬家的路上不能丢东西。
// 老文件留着当备份，要不要删由用户自己决定。
func migrateConfig(path, workdir string) (string, error) {
	legacy := config.LegacyPath(workdir)
	if filepath.Clean(legacy) == filepath.Clean(path) {
		return "", nil
	}
	if _, err := os.Stat(path); err == nil {
		return "", nil // 新位置已经有配置了，别覆盖
	} else if !errors.Is(err, os.ErrNotExist) {
		return "", err
	}
	data, err := os.ReadFile(legacy)
	if errors.Is(err, os.ErrNotExist) {
		return "", nil
	}
	if err != nil {
		return "", fmt.Errorf("读取老配置 %s 失败: %w", legacy, err)
	}
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", fmt.Errorf("创建配置目录失败: %w", err)
	}
	if err := os.WriteFile(path, data, 0o644); err != nil {
		return "", fmt.Errorf("写入 %s 失败: %w", path, err)
	}
	return fmt.Sprintf("已把 %s 复制到 %s（老文件留着没删），以后配置都从用户目录读",
		legacy, path), nil
}

// loadConfig 确保配置文件存在并解析成配置，返回工作目录、配置和一句要提示的话。
// 配置放在用户目录下，和会话一样跟人走；老版本留在项目目录里的那份会顺手迁过来
func loadConfig(path string) (string, config.GlobalConfig, string, error) {
	var cfg config.GlobalConfig

	workdir, err := os.Getwd()
	if err != nil {
		return "", cfg, "", fmt.Errorf("获取当前目录失败: %w", err)
	}
	workdir, err = filepath.Abs(workdir)
	if err != nil {
		return "", cfg, "", fmt.Errorf("解析当前目录失败: %w", err)
	}

	notice, err := migrateConfig(path, workdir)
	if err != nil {
		return "", cfg, "", err
	}

	// 父目录可能还不存在（第一次在这台机器上跑），先建出来
	if err := os.MkdirAll(filepath.Dir(path), 0o755); err != nil {
		return "", cfg, "", fmt.Errorf("创建配置目录失败: %w", err)
	}
	if _, err := util.EnsureFile(path, ""); err != nil {
		return "", cfg, "", fmt.Errorf("创建 %s 失败: %w", path, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", cfg, "", fmt.Errorf("读取配置文件失败: %w", err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return "", cfg, "", fmt.Errorf("解析配置文件失败: %w", err)
	}
	// 模式写错了别等到界面里才发现，启动就报清楚
	if _, err := domain.ParseMode(cfg.Mode); err != nil {
		return "", cfg, "", fmt.Errorf("配置文件 %s 的 Mode 有问题: %w", path, err)
	}

	return workdir, cfg, notice, nil
}
