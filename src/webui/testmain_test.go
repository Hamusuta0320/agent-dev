package webui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hamusuta/agent-dev/src/config"
)

// TestMain 把家目录挪到临时目录，理由和 src/cli 的 testmain_test.go 一样：
// 测试里任何走默认路径的读写都只该落在临时目录，不能碰用户真实的家目录。
//
// 浏览器界面这边每个用例都显式给了 cfgPath，但技能、约定文件这些还是从家目录读，
// 将来也可能有用例漏给路径。进程级兜住比逐个用例靠自觉可靠。
func TestMain(m *testing.M) {
	home, err := os.MkdirTemp("", "agent-dev-webui-test-home")
	if err != nil {
		panic("准备临时家目录失败: " + err.Error())
	}
	os.Setenv("HOME", home)
	os.Setenv("USERPROFILE", home) // Windows 上 os.UserHomeDir 读的是这个
	os.Setenv(testHomeEnv, home)

	code := m.Run()
	os.RemoveAll(home)
	os.Exit(code)
}

// testHomeEnv 记着 TestMain 挪过去的临时家目录，只给测试自检用
const testHomeEnv = "AGENT_DEV_TEST_HOME"

// 默认配置路径必须落在临时家目录里：这一层要是没了，某个漏给 cfgPath 的用例
// 就会把用户的真配置写坏
func TestDefaultConfigPathIsIsolated(t *testing.T) {
	home := filepath.Clean(os.Getenv(testHomeEnv))
	if home == "" {
		t.Fatal("TestMain 没跑：测试的家目录没被挪到临时目录")
	}
	path, err := config.DefaultPath()
	if err != nil {
		t.Fatalf("取默认配置路径失败: %v", err)
	}
	if !strings.HasPrefix(strings.ToLower(filepath.Clean(path)), strings.ToLower(home)) {
		t.Fatalf("默认配置路径跑到真实目录去了（会把用户的真配置写坏）: %q", path)
	}
}
