package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// TestMain 把整个测试进程的家目录挪到临时目录。
//
// newModel 默认把 cfgPath 指向默认配置路径，测试里只要触发 /model、/effort、
// /config 这些写回路径，就会覆盖用户真实的配置——2026-11-05 就有一个用例这么把
// 真配置写成了 testModels() 里的 sk-a / model-a 假数据。单个用例写对了也挡不住
// 以后新加的用例，所以从进程层面兜住。
//
// 两道锁缺一不可：
//   - 这个文件必须一直进 git。2026-11-06 又坏了一次，原因是当时它还是未跟踪文件，
//     有人在 git worktree 里检出某个提交跑测试，干净检出里压根没有它。
//   - defaultCfgPath 自己也有一道兜底：跑测试时直接落到临时目录，不依赖这个文件。
//
// 这里管的是家目录下的其它东西（会话、技能、约定文件），那些不走 defaultCfgPath。
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "agent-dev-test-home")
	if err != nil {
		panic("准备临时家目录失败: " + err.Error())
	}
	// 把 .agent-dev 先建出来，写配置的路径才会真的落在临时目录里，和真实环境一致
	if err := os.MkdirAll(filepath.Join(home, ".agent-dev"), 0o755); err != nil {
		panic("准备临时配置目录失败: " + err.Error())
	}
	os.Setenv("HOME", home)
	os.Setenv("USERPROFILE", home) // Windows 上 os.UserHomeDir 读的是这个
	os.Setenv(testHomeEnv, home)   // 给下面的自检认，确认这一层真的生效了

	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// testHomeEnv 记着 TestMain 挪过去的临时家目录。它只给测试用：
// 自检靠它判断"这一层防护在不在"，生产代码不读
const testHomeEnv = "AGENT_DEV_TEST_HOME"

// 配置路径必须落在临时目录里——这是"测试别写坏用户真配置"的最后一道锁。
// 谁把 TestMain 或 defaultCfgPath 的兜底改坏了，这条会先红，而不是等到
// 用户的配置被写成假数据才发现
func TestConfigPathIsIsolated(t *testing.T) {
	got := filepath.Clean(defaultCfgPath())
	if got == "" || strings.HasPrefix(got, "..") {
		t.Fatalf("测试里的配置路径不对: %q", got)
	}
	// 两道锁任意一道生效都算数：defaultCfgPath 的兜底（临时目录），
	// 或者 TestMain 把家目录挪走之后解析出来的路径
	temp := filepath.Clean(os.TempDir())
	home := filepath.Clean(os.Getenv(testHomeEnv))
	underTemp := strings.HasPrefix(strings.ToLower(got), strings.ToLower(temp))
	underHome := home != "" && strings.HasPrefix(strings.ToLower(got), strings.ToLower(home))
	if !underTemp && !underHome {
		t.Fatalf("测试里的配置路径跑到真实目录去了（会把用户的真配置写坏）: %q", got)
	}
}
