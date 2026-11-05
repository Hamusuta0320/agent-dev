package config

import (
	"fmt"
	"os"
	"path/filepath"
)

// DefaultDir 返回配置所在的目录：<用户目录>/.agent-dev，和会话放在同一棵树下
func DefaultDir() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("找不到用户目录: %w", err)
	}
	return filepath.Join(home, ".agent-dev"), nil
}

// DefaultPath 返回配置文件的路径：<用户目录>/.agent-dev/config.yaml。
//
// 配置跟着人走，不跟工作区绑定：在哪个目录启动用的都是同一份，模型和模式不会
// 因为换了项目就没了；会话才按工作区分开。
func DefaultPath() (string, error) {
	dir, err := DefaultDir()
	if err != nil {
		return "", err
	}
	return filepath.Join(dir, "config.yaml"), nil
}

// LegacyPath 是老版本的配置位置：<workDir>/config.yaml，也就是项目目录里那份。
// 配置搬到用户目录之后，它只用来迁移一次
func LegacyPath(workDir string) string {
	return filepath.Join(workDir, "config.yaml")
}
