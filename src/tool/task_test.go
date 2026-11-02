package tool

import (
	"context"
	"encoding/json"
	"regexp"
	"runtime"
	"strconv"
	"strings"
	"testing"
	"time"
)

func runTask(t *testing.T, args map[string]any) (string, error) {
	t.Helper()
	raw, err := json.Marshal(args)
	if err != nil {
		t.Fatalf("序列化参数失败: %v", err)
	}
	return TaskTool{}.Execute(context.Background(), raw)
}

var taskIDPattern = regexp.MustCompile(`task_\d+`)

// startTask 起一个后台任务并返回它的 id，测试结束时把任务清干净
func startTask(t *testing.T, args map[string]any) string {
	t.Helper()
	args["background"] = true
	out, err := runBash(t, args)
	if err != nil {
		t.Fatalf("后台启动失败: %v", err)
	}
	id := taskIDPattern.FindString(out)
	if id == "" {
		t.Fatalf("返回里应该有任务 id: %s", out)
	}
	return id
}

// 后台任务要能跑完、查状态、拿到输出
func TestBackgroundTaskLifecycle(t *testing.T) {
	t.Cleanup(backgroundTasks.clear)

	id := startTask(t, map[string]any{"command": "echo bg-done"})

	out, err := runTask(t, map[string]any{"action": "wait", "task_id": id, "timeout_seconds": 20})
	if err != nil {
		t.Fatalf("wait 失败: %v", err)
	}
	if !strings.Contains(out, "已完成") || !strings.Contains(out, "exit_code: 0") {
		t.Fatalf("任务应该跑完并报成功: %s", out)
	}
	if !strings.Contains(out, "bg-done") {
		t.Fatalf("wait 应该带上任务输出: %s", out)
	}

	out, err = runTask(t, map[string]any{"action": "output", "task_id": id})
	if err != nil {
		t.Fatalf("output 失败: %v", err)
	}
	if !strings.Contains(out, "bg-done") || !strings.Contains(out, "已完成") {
		t.Fatalf("output 应该返回状态和输出: %s", out)
	}

	list, err := runTask(t, map[string]any{"action": "list"})
	if err != nil {
		t.Fatalf("list 失败: %v", err)
	}
	if !strings.Contains(list, id) || !strings.Contains(list, "已完成") {
		t.Fatalf("list 里应该有这个任务: %s", list)
	}
}

// 任务还在跑的时候 output 要说清楚这是阶段性输出，wait 等不到也不能当成失败
func TestBackgroundTaskRunning(t *testing.T) {
	if runtime.GOOS == "windows" && activeShell().Name == "cmd" {
		t.Skip("cmd 下没有统一的 sleep 命令，跳过")
	}
	t.Cleanup(backgroundTasks.clear)

	id := startTask(t, map[string]any{"command": sleepCommand(3), "timeout_seconds": 60})

	out, err := runTask(t, map[string]any{"action": "output", "task_id": id})
	if err != nil {
		t.Fatalf("output 失败: %v", err)
	}
	if !strings.Contains(out, "运行中") || !strings.Contains(out, "还在跑") {
		t.Fatalf("任务还在跑时要说明白: %s", out)
	}

	out, err = runTask(t, map[string]any{"action": "wait", "task_id": id, "timeout_seconds": 1})
	if err != nil {
		t.Fatalf("wait 失败: %v", err)
	}
	if !strings.Contains(out, "还在跑") {
		t.Fatalf("等不到时应如实说还在跑: %s", out)
	}
}

// action=stop 要把任务真的停下来，不能只是标记一下
func TestBackgroundTaskStop(t *testing.T) {
	if runtime.GOOS == "windows" && activeShell().Name == "cmd" {
		t.Skip("cmd 下没有统一的 sleep 命令，跳过")
	}
	t.Cleanup(backgroundTasks.clear)

	id := startTask(t, map[string]any{"command": sleepCommand(30), "timeout_seconds": 60})

	start := time.Now()
	out, err := runTask(t, map[string]any{"action": "stop", "task_id": id})
	if err != nil {
		t.Fatalf("stop 失败: %v", err)
	}
	if !strings.Contains(out, "已停止") {
		t.Fatalf("stop 之后状态应该是已停止: %s", out)
	}
	if elapsed := time.Since(start); elapsed > 10*time.Second {
		t.Fatalf("stop 没有真的把进程停下来，耗时 %s", elapsed)
	}

	// 已经停了再停一次也不该报错，只是提示一句
	out, err = runTask(t, map[string]any{"action": "stop", "task_id": id})
	if err != nil {
		t.Fatalf("重复 stop 不该报错: %v", err)
	}
	if !strings.Contains(out, "已经结束") {
		t.Fatalf("重复 stop 应该提示已经结束: %s", out)
	}
}

// 后台任务同样受 timeout_seconds 约束：到点自己停掉，不用模型来收尾
func TestBackgroundTaskTimeout(t *testing.T) {
	if runtime.GOOS == "windows" && activeShell().Name == "cmd" {
		t.Skip("cmd 下没有统一的 sleep 命令，跳过")
	}
	t.Cleanup(backgroundTasks.clear)

	id := startTask(t, map[string]any{"command": sleepCommand(30), "timeout_seconds": 1})

	out, err := runTask(t, map[string]any{"action": "wait", "task_id": id, "timeout_seconds": 20})
	if err != nil {
		t.Fatalf("wait 失败: %v", err)
	}
	if !strings.Contains(out, "超时已终止") {
		t.Fatalf("超过 timeout_seconds 的任务应该被终止: %s", out)
	}
}

// 后台任务不跟着当前这一轮走：这一轮被中断了，后台照样跑
func TestBackgroundTaskSurvivesCancel(t *testing.T) {
	t.Cleanup(backgroundTasks.clear)

	ctx, cancel := context.WithCancel(context.Background())
	cancel() // 这一轮已经中断

	raw, err := json.Marshal(map[string]any{"command": "echo still-here", "background": true})
	if err != nil {
		t.Fatalf("序列化参数失败: %v", err)
	}
	out, err := (BashTool{}).Execute(ctx, raw)
	if err != nil {
		t.Fatalf("后台启动不该受这一轮中断影响: %v", err)
	}
	id := taskIDPattern.FindString(out)
	if id == "" {
		t.Fatalf("返回里应该有任务 id: %s", out)
	}
	if res, err := runTask(t, map[string]any{"action": "wait", "task_id": id, "timeout_seconds": 20}); err != nil || !strings.Contains(res, "still-here") {
		t.Fatalf("任务应该照常跑完: %v / %s", err, res)
	}
}

// tail_lines 只留最后几行，别把整段输出灌给模型
func TestTaskOutputTailLines(t *testing.T) {
	t.Cleanup(backgroundTasks.clear)

	id := startTask(t, map[string]any{"command": "echo line-1; echo line-2; echo line-3"})
	if _, err := runTask(t, map[string]any{"action": "wait", "task_id": id, "timeout_seconds": 20}); err != nil {
		t.Fatalf("wait 失败: %v", err)
	}

	out, err := runTask(t, map[string]any{"action": "output", "task_id": id, "tail_lines": 1})
	if err != nil {
		t.Fatalf("output 失败: %v", err)
	}
	// 命令行正文里也有 line-1，所以按"整行"来判断哪些行真的输出了
	if !strings.Contains(out, "\nline-3") {
		t.Fatalf("最后一行应该留着: %s", out)
	}
	if strings.Contains(out, "\nline-1") || strings.Contains(out, "\nline-2") {
		t.Fatalf("tail_lines=1 不该带出前面的行: %s", out)
	}
	if !strings.Contains(out, "前面还有 2 行") {
		t.Fatalf("被截掉的行数要说明: %s", out)
	}
}

// 参数给错时说清楚怎么改，别让模型瞎猜
func TestTaskBadArgs(t *testing.T) {
	t.Cleanup(backgroundTasks.clear)

	if _, err := runTask(t, map[string]any{"action": "explode"}); err == nil {
		t.Fatal("不认识的 action 应该报错")
	}
	if _, err := runTask(t, map[string]any{}); err == nil {
		t.Fatal("缺 action 应该报错")
	}
	_, err := runTask(t, map[string]any{"action": "output", "task_id": "task_999"})
	if err == nil || !strings.Contains(err.Error(), "没有这个任务") {
		t.Fatalf("任务不存在要明确报出来: %v", err)
	}

	// 后台启动失败（工作目录不存在）要当场报错，而不是留个注定失败的任务
	_, err = runBash(t, map[string]any{
		"command":    "echo x",
		"workdir":    t.TempDir() + "/nope",
		"background": true,
	})
	if err == nil || !strings.Contains(err.Error(), "工作目录") {
		t.Fatalf("后台启动也要校验工作目录: %v", err)
	}
}

// 任务位子满了就挤掉最早的已结束任务，都还在跑就拒绝新的
func TestTaskRegistryEvictsFinished(t *testing.T) {
	t.Cleanup(backgroundTasks.clear)

	// 伪造 maxBackgroundTasks 个已结束的任务，不走真实进程，测起来快
	for i := 0; i < maxBackgroundTasks; i++ {
		task := &bgTask{done: make(chan struct{}), out: newOutputBuffer(64)}
		task.finish(taskDone, 0, "")
		if err := backgroundTasks.add(task); err != nil {
			t.Fatalf("第 %d 个任务应该放得下: %v", i+1, err)
		}
	}
	first := backgroundTasks.list()[0].ID

	finished := &bgTask{done: make(chan struct{}), out: newOutputBuffer(64)}
	finished.finish(taskDone, 0, "")
	if err := backgroundTasks.add(finished); err != nil {
		t.Fatalf("挤掉旧任务之后应该放得下: %v", err)
	}
	if n := len(backgroundTasks.list()); n != maxBackgroundTasks {
		t.Fatalf("任务数应该保持在 %d，实际 %d", maxBackgroundTasks, n)
	}
	if _, ok := backgroundTasks.get(first); ok {
		t.Fatalf("最早的已结束任务 %s 应该被挤掉", first)
	}

	// 位子被运行中的任务占满时，宁可不接也别把还在跑的丢掉
	backgroundTasks.clear()
	for i := 0; i < maxBackgroundTasks; i++ {
		task := &bgTask{done: make(chan struct{}), out: newOutputBuffer(64)}
		if err := backgroundTasks.add(task); err != nil {
			t.Fatalf("第 %d 个任务应该放得下: %v", i+1, err)
		}
	}
	if err := backgroundTasks.add(&bgTask{done: make(chan struct{}), out: newOutputBuffer(64)}); err == nil {
		t.Fatal("都在跑的时候应该拒绝新任务")
	}
}

// 任务跑完要主动告诉模型一次，而且只报一次
func TestTaskNotifications(t *testing.T) {
	t.Cleanup(backgroundTasks.clear)

	id := startTask(t, map[string]any{"command": "echo notify-me"})
	task, ok := backgroundTasks.get(id)
	if !ok {
		t.Fatalf("任务 %s 应该登记着", id)
	}
	select {
	case <-task.done:
	case <-time.After(20 * time.Second):
		t.Fatal("任务没跑完")
	}

	notes := TaskNotifications()
	if len(notes) != 1 {
		t.Fatalf("应该有一条通知，实际 %d: %v", len(notes), notes)
	}
	for _, want := range []string{id, "已完成", "exit_code: 0", "echo notify-me"} {
		if !strings.Contains(notes[0], want) {
			t.Fatalf("通知里少了 %q: %s", want, notes[0])
		}
	}

	// 报过就不再重复，免得每一轮都刷一遍
	if again := TaskNotifications(); len(again) != 0 {
		t.Fatalf("同一个任务不该通知两次: %v", again)
	}
}

// 模型已经用 Task 主动看过的任务，不用再补一条通知
func TestTaskNotificationsSkipsSeenTask(t *testing.T) {
	t.Cleanup(backgroundTasks.clear)

	id := startTask(t, map[string]any{"command": "echo seen"})
	if _, err := runTask(t, map[string]any{"action": "wait", "task_id": id, "timeout_seconds": 20}); err != nil {
		t.Fatalf("wait 失败: %v", err)
	}
	if notes := TaskNotifications(); len(notes) != 0 {
		t.Fatalf("已经看过结果的任务不该再通知: %v", notes)
	}

	// 先 wait 到结束、再 output 一次，同样算看过
	id = startTask(t, map[string]any{"command": "echo seen-2"})
	time.Sleep(500 * time.Millisecond)
	if _, err := runTask(t, map[string]any{"action": "output", "task_id": id}); err != nil {
		t.Fatalf("output 失败: %v", err)
	}
	if notes := TaskNotifications(); len(notes) != 0 {
		t.Fatalf("output 看过的任务不该再通知: %v", notes)
	}
}

// 还在跑的任务不该被通知，等真结束了再报
func TestTaskNotificationsSkipsRunning(t *testing.T) {
	if runtime.GOOS == "windows" && activeShell().Name == "cmd" {
		t.Skip("cmd 下没有统一的 sleep 命令，跳过")
	}
	t.Cleanup(backgroundTasks.clear)

	startTask(t, map[string]any{"command": sleepCommand(3), "timeout_seconds": 60})
	if notes := TaskNotifications(); len(notes) != 0 {
		t.Fatalf("还在跑的任务不该通知: %v", notes)
	}
}

// 被停掉、超时的任务也要有一条通知，里面带上原因
func TestTaskNotificationsIncludeReason(t *testing.T) {
	if runtime.GOOS == "windows" && activeShell().Name == "cmd" {
		t.Skip("cmd 下没有统一的 sleep 命令，跳过")
	}
	t.Cleanup(backgroundTasks.clear)

	id := startTask(t, map[string]any{"command": sleepCommand(30), "timeout_seconds": 60})
	// 直接停掉，不走 Task 工具，这样通知还能留下来
	task, _ := backgroundTasks.get(id)
	task.cancel()
	<-task.done

	notes := TaskNotifications()
	if len(notes) != 1 || !strings.Contains(notes[0], "已停止") {
		t.Fatalf("被停掉的任务应该有一条通知并说明原因: %v", notes)
	}
}

// timeout_seconds 越界要夹紧，别让模型传个天文数字把上限绕过去
func TestBashTimeoutClamp(t *testing.T) {
	if got := bashTimeout(0); got != defaultBashTimeout {
		t.Fatalf("没传超时应该用默认值，实际 %s", got)
	}
	if got := bashTimeout(5); got != 5*time.Second {
		t.Fatalf("指定的超时没生效: %s", got)
	}
	if got := bashTimeout(86400); got != maxBashTimeout {
		t.Fatalf("超长超时要夹到上限，实际 %s", got)
	}
	if got := bashTimeout(-3); got != defaultBashTimeout {
		t.Fatalf("负数超时应该退回默认值，实际 %s", got)
	}
}

// sleepCommand 给出跨平台的 sleep 写法
func sleepCommand(seconds int) string {
	if runtime.GOOS == "windows" {
		return "Start-Sleep -Seconds " + strconv.Itoa(seconds)
	}
	return "sleep " + strconv.Itoa(seconds)
}
