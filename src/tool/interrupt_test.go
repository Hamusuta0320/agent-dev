package tool

import (
	"context"
	"encoding/json"
	"os/exec"
	"runtime"
	"strconv"
	"testing"
	"time"
)

// runWithCancel 跑一条命令，delay 之后取消 ctx，返回执行耗时。
// 超过 limit 还没返回就直接判失败——这正是"Ctrl+C 停不了"的样子
func runWithCancel(t *testing.T, command string, delay, limit time.Duration) time.Duration {
	t.Helper()
	raw, err := json.Marshal(map[string]any{"command": command})
	if err != nil {
		t.Fatalf("序列化参数失败: %v", err)
	}
	ctx, cancel := context.WithCancel(context.Background())
	defer cancel()

	go func() {
		time.Sleep(delay)
		cancel()
	}()

	type result struct {
		out string
		err error
	}
	done := make(chan result, 1)
	start := time.Now()
	go func() {
		out, err := (BashTool{}).Execute(ctx, raw)
		done <- result{out, err}
	}()

	select {
	case r := <-done:
		elapsed := time.Since(start)
		if r.err != nil {
			t.Fatalf("中断不该当成工具错误: %v", r.err)
		}
		return elapsed
	case <-time.After(limit):
		t.Fatalf("%s: 取消后 %s 还没返回（Ctrl+C 卡死）", command, limit)
		return 0
	}
}

// 中断普通命令要快：这是最基本的一条
func TestInterruptFast(t *testing.T) {
	command := "sleep 30"
	if runtime.GOOS == "windows" {
		command = "Start-Sleep -Seconds 30"
	}
	if elapsed := runWithCancel(t, command, 300*time.Millisecond, 10*time.Second); elapsed > 3*time.Second {
		t.Fatalf("中断太慢: %s", elapsed)
	}
}

// 命令 fork 出子进程时也要能停机
func TestInterruptChildProcess(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("子进程场景按 windows 写")
	}
	if _, err := exec.LookPath("python"); err != nil {
		t.Skip("没装 python，跳过")
	}
	elapsed := runWithCancel(t, `python -c "import time; time.sleep(30)"`, 300*time.Millisecond, 10*time.Second)
	if elapsed > 3*time.Second {
		t.Fatalf("中断太慢: %s", elapsed)
	}
}

// 子进程脱离进程树时（cmd /c start /b 这种）taskkill /T 追不到它，
// 它攥着 stdout 管道不放，exec 的 Wait 就会一直等 EOF。
// 这条守的是 cmd.WaitDelay：没有它，这里会一直挂到测试超时
func TestInterruptDetachedChild(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("脱离进程树是 windows 上 taskkill /T 的局限")
	}
	const command = `cmd /c "start /b ping -n 30 127.0.0.1"`
	// 脱离出去的那个 ping 我们杀不到，跑完自己清一下，别留在机器上
	t.Cleanup(func() {
		_ = exec.Command("taskkill", "/F", "/IM", "PING.EXE").Run()
	})

	elapsed := runWithCancel(t, command, 500*time.Millisecond, 15*time.Second)
	// WaitDelay 是一秒，加上调度余量，三秒足够
	if elapsed > 3*time.Second {
		t.Fatalf("脱离进程树时中断太慢: %s", elapsed)
	}
	t.Logf("脱离进程树也能在 %s 内返回", elapsed)
}

// 正常跑完的命令不该被 WaitDelay 拖慢：它没有取消，WaitDelay 根本不计时
func TestNoWaitDelayOnNormalExit(t *testing.T) {
	command := "echo hi"
	if runtime.GOOS == "windows" {
		command = "Write-Output hi"
	}
	raw, _ := json.Marshal(map[string]any{"command": command})
	start := time.Now()
	out, err := (BashTool{}).Execute(context.Background(), raw)
	elapsed := time.Since(start)
	if err != nil {
		t.Fatalf("正常命令不该报错: %v", err)
	}
	if elapsed > 3*time.Second {
		t.Fatalf("正常命令被拖慢了: %s", elapsed)
	}
	t.Logf("正常命令耗时 %s，输出 %s", elapsed, strconv.Quote(out))
}
