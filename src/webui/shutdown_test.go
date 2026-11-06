package webui

import (
	"context"
	"encoding/json"
	"net"
	"net/http"
	"path/filepath"
	"testing"
	"time"

	"hamusuta/agent-dev/src/config"
	"hamusuta/agent-dev/src/session"
)

// serveOptions 造一份和线上一致的启动参数（临时目录 + 一套假模型）
func serveOptions(t *testing.T) Options {
	t.Helper()
	dir := t.TempDir()
	root := filepath.Join(dir, "sessions")
	id, err := session.WorkspaceId(dir)
	if err != nil {
		t.Fatalf("算工作区 id 失败: %v", err)
	}
	return Options{
		WorkDir: dir,
		Root:    root,
		CfgPath: filepath.Join(dir, "config.yaml"),
		Cfg: config.GlobalConfig{
			Models: []config.ModelConfig{{
				Name: "测", Type: "DeepSeek", ApiUrl: "https://example.invalid",
				ApiKey: "sk-test", Model: "test-model", ContextLimit: 40000,
			}},
			Current: "测",
			Mode:    "full",
		},
		Store: session.NewStore(filepath.Join(root, id)),
	}
}

// startRealServe 在随机端口上起一个真的 Serve，等它开始监听后返回
func startRealServe(t *testing.T) (string, context.CancelFunc, <-chan error) {
	t.Helper()
	l, err := net.Listen("tcp", "127.0.0.1:0")
	if err != nil {
		t.Fatalf("找空闲端口失败: %v", err)
	}
	addr := l.Addr().String()
	l.Close()

	opt := serveOptions(t)
	opt.Addr = addr
	ctx, cancel := context.WithCancel(context.Background())
	done := make(chan error, 1)
	go func() { done <- Serve(ctx, opt) }()

	deadline := time.Now().Add(5 * time.Second)
	for time.Now().Before(deadline) {
		c, err := net.DialTimeout("tcp", addr, 200*time.Millisecond)
		if err == nil {
			c.Close()
			return addr, cancel, done
		}
		time.Sleep(50 * time.Millisecond)
	}
	cancel()
	t.Fatalf("Serve 没起来")
	return "", nil, nil
}

// waitShutdown 取消 ctx 并返回 Serve 结束花了多久；超时则判失败
func waitShutdown(t *testing.T, cancel context.CancelFunc, done <-chan error, limit time.Duration) time.Duration {
	t.Helper()
	start := time.Now()
	cancel()
	select {
	case <-done:
		return time.Since(start)
	case <-time.After(limit):
		t.Fatalf("关闭卡住：%s 内没返回（这就是 Ctrl+C 要按好几次的原因）", limit)
		return 0
	}
}

// 空转时关闭应该很快
func TestServeShutdownIdle(t *testing.T) {
	_, cancel, done := startRealServe(t)
	if elapsed := waitShutdown(t, cancel, done, 10*time.Second); elapsed > 2*time.Second {
		t.Fatalf("空转关闭太慢: %s", elapsed)
	} else {
		t.Logf("空转关闭耗时 %s", elapsed)
	}
}

// 关键一条：挂着 SSE 长连接时，关闭不能等。
// SSE handler 里 select 的是 r.Context()，而 Shutdown 不会取消 request context，
// 所以这条连接会一直挂着，把 Shutdown 拖到超时——用户看到的就是"按一次没反应"
func TestServeShutdownWithOpenSSE(t *testing.T) {
	addr, cancel, done := startRealServe(t)

	// 建会话
	resp, err := http.Post("http://"+addr+"/api/sessions", "application/json", nil)
	if err != nil {
		cancel()
		t.Fatalf("建会话失败: %v", err)
	}
	var created struct {
		Id string `json:"id"`
	}
	_ = json.NewDecoder(resp.Body).Decode(&created)
	resp.Body.Close()
	if created.Id == "" {
		cancel()
		t.Skip("会话建不出来，跳过")
	}

	// 连上 SSE 并等到首字节，确认它真的在推流
	sseResp, err := http.Get("http://" + addr + "/api/sessions/" + created.Id + "/events")
	if err != nil {
		cancel()
		t.Fatalf("连 SSE 失败: %v", err)
	}
	defer sseResp.Body.Close()
	if sseResp.StatusCode != http.StatusOK {
		cancel()
		t.Fatalf("SSE 状态码 %d", sseResp.StatusCode)
	}

	elapsed := waitShutdown(t, cancel, done, 12*time.Second)
	t.Logf("挂着 SSE 时关闭耗时 %s", elapsed)
	if elapsed > 2*time.Second {
		t.Fatalf("挂着 SSE 时关闭太慢: %s（应该是毫秒级）", elapsed)
	}
}
