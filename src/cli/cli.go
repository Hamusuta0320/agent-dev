package main

import (
	"fmt"
	"os"
	"path/filepath"

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

	workdir, cfg, err := loadConfig("config.yaml")
	if err != nil {
		fmt.Fprintln(os.Stderr, err)
		os.Exit(1)
	}

	cur := domain.NewSession(workdir)
	m := newModel(cfg, session.NewStore(session.DefaultDir(workdir)), workdir, cur)

	p := tea.NewProgram(m)
	if _, err := p.Run(); err != nil {
		fmt.Fprintln(os.Stderr, "界面启动失败", err)
		os.Exit(1)
	}
	// 退出时把还没跑完的后台任务一起收掉，别留一堆看不见的进程
	tool.StopAllTasks()
}

// loadConfig 确保 config.yaml 存在并解析成配置，返回工作目录和配置
func loadConfig(path string) (string, config.GlobalConfig, error) {
	var cfg config.GlobalConfig

	if _, err := util.EnsureFile(path, ""); err != nil {
		return "", cfg, fmt.Errorf("创建 %s 失败: %w", path, err)
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return "", cfg, fmt.Errorf("读取配置文件失败: %w", err)
	}
	if err := yaml.Unmarshal(data, &cfg); err != nil {
		return "", cfg, fmt.Errorf("解析配置文件失败: %w", err)
	}
	// 模式写错了别等到界面里才发现，启动就报清楚
	if _, err := domain.ParseMode(cfg.Mode); err != nil {
		return "", cfg, fmt.Errorf("配置文件 %s 的 Mode 有问题: %w", path, err)
	}

	workdir, err := os.Getwd()
	if err != nil {
		return "", cfg, fmt.Errorf("获取当前目录失败: %w", err)
	}
	workdir, err = filepath.Abs(workdir)
	if err != nil {
		return "", cfg, fmt.Errorf("解析当前目录失败: %w", err)
	}
	return workdir, cfg, nil
}
