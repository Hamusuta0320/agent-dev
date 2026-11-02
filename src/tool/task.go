package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os/exec"
	"strings"
	"sync"
	"time"
)

const (
	// maxBackgroundTasks 同时留着的后台任务上限。跑完的旧任务会被新的挤掉，
	// 都在跑时不再接收新的，免得内存和进程越攒越多
	maxBackgroundTasks = 20
	// defaultTaskWait 是 Task action=wait 默认等多久
	defaultTaskWait = 30 * time.Second
	// maxTaskWait 是 Task action=wait 最多能等多久
	maxTaskWait = 120 * time.Second
)

// taskStatus 是后台任务的状态
type taskStatus int

const (
	taskRunning taskStatus = iota
	taskDone
	taskTimeout
	taskStopped
	taskFailed
)

// Label 是状态给人看的文案
func (s taskStatus) Label() string {
	switch s {
	case taskRunning:
		return "运行中"
	case taskDone:
		return "已完成"
	case taskTimeout:
		return "超时已终止"
	case taskStopped:
		return "已停止"
	case taskFailed:
		return "启动失败"
	default:
		return "未知状态"
	}
}

// bgTask 是一个后台任务：命令在单独的 goroutine 里跑，输出攒在内存里，
// 结束状态（退出码 / 超时 / 被停掉）既可以用 Task 工具查，也会在结束时
// 自动出现在下一轮的系统提示词里，省得模型干等着或者反复轮询。
type bgTask struct {
	ID        string
	Command   string
	Workdir   string
	Timeout   time.Duration
	StartedAt time.Time

	out    *outputBuffer
	done   chan struct{}
	cancel context.CancelFunc

	mu         sync.Mutex
	status     taskStatus
	exitCode   int
	reason     string
	finishedAt time.Time
	// announced 记着"结束这件事有没有告诉过模型"，保证同一个任务只提醒一次
	announced bool
}

// finish 记下任务是怎么结束的，并叫醒在等的调用方
func (t *bgTask) finish(status taskStatus, exitCode int, reason string) {
	t.mu.Lock()
	if t.status != taskRunning {
		t.mu.Unlock()
		return
	}
	t.status, t.exitCode, t.reason, t.finishedAt = status, exitCode, reason, time.Now()
	t.mu.Unlock()
	close(t.done)
}

// state 取一份状态快照，给展示用
func (t *bgTask) state() (taskStatus, int, string, time.Time) {
	t.mu.Lock()
	defer t.mu.Unlock()
	return t.status, t.exitCode, t.reason, t.finishedAt
}

// Elapsed 返回任务已经跑了多久（结束后就是总耗时）
func (t *bgTask) elapsed() time.Duration {
	status, _, _, finishedAt := t.state()
	if status == taskRunning || finishedAt.IsZero() {
		return time.Since(t.StartedAt)
	}
	return finishedAt.Sub(t.StartedAt)
}

// summary 是任务的一行摘要：id、状态、目录、命令
func (t *bgTask) summary() string {
	status, exitCode, reason, _ := t.state()
	detail := status.Label()
	if status == taskDone {
		detail = fmt.Sprintf("%s（exit %d）", status.Label(), exitCode)
	}
	if reason != "" {
		detail += "：" + reason
	}
	return fmt.Sprintf("%s · %s · 耗时 %s · 命令: %s",
		t.ID, detail, shortDuration(t.elapsed()), firstLine(t.Command))
}

// taskRegistry 进程内所有后台任务，按创建顺序排。
// 工具实例本身是无状态的，任务得挂在这里才留得住。
type taskRegistry struct {
	mu    sync.Mutex
	seq   int
	tasks []*bgTask
}

var backgroundTasks = &taskRegistry{}

// add 登记一个新任务并分配 id。位子满了先挤掉最早的已结束任务，都还在跑就拒绝
func (r *taskRegistry) add(t *bgTask) error {
	r.mu.Lock()
	defer r.mu.Unlock()
	if len(r.tasks) >= maxBackgroundTasks {
		kept := r.tasks[:0]
		dropped := false
		for _, old := range r.tasks {
			if !dropped && old.state0() != taskRunning {
				dropped = true
				continue
			}
			kept = append(kept, old)
		}
		r.tasks = kept
		if !dropped {
			return fmt.Errorf("后台任务已经 %d 个，都在跑；先用 Task action=stop 停掉一些，或者等它们结束", len(r.tasks))
		}
	}
	r.seq++
	t.ID = fmt.Sprintf("task_%d", r.seq)
	r.tasks = append(r.tasks, t)
	return nil
}

// state0 只看状态，给上面的清理逻辑用
func (t *bgTask) state0() taskStatus {
	s, _, _, _ := t.state()
	return s
}

// markAnnounced 标记"结束这件事已经说过"，第一次标记时返回 true。
// 任务还在跑时不算，等真结束了再报
func (t *bgTask) markAnnounced() bool {
	t.mu.Lock()
	defer t.mu.Unlock()
	if t.announced || t.status == taskRunning {
		return false
	}
	t.announced = true
	return true
}

// notification 是给模型看的一行提醒：任务结束了、退出码、耗时和命令
func (t *bgTask) notification() string {
	status, exitCode, reason, _ := t.state()
	line := fmt.Sprintf("- %s %s（exit_code: %d，耗时 %s）：%s",
		t.ID, status.Label(), exitCode, shortDuration(t.elapsed()), firstLine(t.Command))
	if reason != "" {
		line += "（" + reason + "）"
	}
	return line
}

// TaskNotifications 返回"已经结束、但还没告诉过模型"的后台任务，取过就不再重复。
// core 每轮拼系统提示词时调它，模型不用轮询也能知道后台跑完了
func TaskNotifications() []string {
	return backgroundTasks.takeNotifications()
}

// takeNotifications 摘出待通知的任务并标记成已通知
func (r *taskRegistry) takeNotifications() []string {
	r.mu.Lock()
	tasks := append([]*bgTask{}, r.tasks...)
	r.mu.Unlock()

	var out []string
	for _, t := range tasks {
		if t.markAnnounced() {
			out = append(out, t.notification())
		}
	}
	return out
}

func (r *taskRegistry) get(id string) (*bgTask, bool) {
	r.mu.Lock()
	defer r.mu.Unlock()
	for _, t := range r.tasks {
		if t.ID == id {
			return t, true
		}
	}
	return nil, false
}

func (r *taskRegistry) list() []*bgTask {
	r.mu.Lock()
	defer r.mu.Unlock()
	return append([]*bgTask{}, r.tasks...)
}

// stopAllTasks 停掉所有还没跑完的后台任务：CLI 退出之后没人能再管它们，
// 留着就是一堆看不见的进程。已经结束的任务不受影响
func (r *taskRegistry) stopAllTasks() {
	for _, t := range r.list() {
		if t.state0() != taskRunning || t.cancel == nil {
			continue
		}
		t.cancel()
		<-t.done
	}
}

// StopAllTasks 停掉所有还没跑完的后台任务，进程退出前调用
func StopAllTasks() {
	backgroundTasks.stopAllTasks()
}

// clear 停掉所有任务并把登记表清空，测试收尾用
func (r *taskRegistry) clear() {
	r.stopAllTasks()
	r.mu.Lock()
	r.tasks = nil
	r.seq = 0
	r.mu.Unlock()
}

// startBackgroundTask 把命令放到后台跑，立刻返回任务句柄。
// 后台任务不受这一轮的 Context 影响（这一轮结束或被中断都不该把它带走），
// 只受 timeout 限制；要提前停掉请用 Task action=stop。
func startBackgroundTask(args BashArgs, timeout time.Duration) (*bgTask, error) {
	runCtx, cancel := context.WithTimeout(context.Background(), timeout)
	cmd, err := shellCommand(runCtx, args)
	if err != nil {
		cancel()
		return nil, err
	}
	task := &bgTask{
		Command:   args.Command,
		Workdir:   strings.TrimSpace(args.Workdir),
		Timeout:   timeout,
		StartedAt: time.Now(),
		out:       newOutputBuffer(maxBashOutput),
		done:      make(chan struct{}),
		cancel:    cancel,
		status:    taskRunning,
	}
	cmd.Stdout = task.out
	cmd.Stderr = task.out

	// 先真的启动起来，起不来（shell 不存在之类）当场报给模型，别拖到后台才失败
	if err := cmd.Start(); err != nil {
		cancel()
		return nil, fmt.Errorf("启动后台命令失败（shell=%s）: %w", activeShell().Name, err)
	}
	if err := backgroundTasks.add(task); err != nil {
		killTree(cmd.Process)
		_ = cmd.Wait()
		cancel()
		return nil, err
	}

	go func() {
		defer cancel()
		err := cmd.Wait()
		switch {
		case errors.Is(runCtx.Err(), context.DeadlineExceeded):
			task.finish(taskTimeout, -1, fmt.Sprintf("超过 %s，已终止", shortDuration(timeout)))
		case runCtx.Err() != nil:
			task.finish(taskStopped, -1, "被 Task action=stop 停下了")
		default:
			code := 0
			if err != nil {
				var exitErr *exec.ExitError
				if errors.As(err, &exitErr) {
					code = exitErr.ExitCode()
				} else {
					task.finish(taskFailed, -1, err.Error())
					return
				}
			}
			task.finish(taskDone, code, "")
		}
	}()
	return task, nil
}

type TaskTool struct{}

func (t TaskTool) Name() string {
	return "Task"
}

// Mutating 是 false：它自己不改磁盘，只跟进 Bash 起的后台任务。
// 只读模式下也留着，方便看看之前起的任务跑完没有、或者把它停掉
func (t TaskTool) Mutating() bool { return false }

func (t TaskTool) Description() string {
	return "跟进 Bash 用 background=true 起的后台任务。" +
		"action=list 列出所有任务；action=output 读某个任务的输出（tail_lines 只要最后几行）；" +
		"action=wait 等任务跑完（最多 timeout_seconds 秒，默认 30，最长 120）；action=stop 把任务停掉。" +
		"output/wait/stop 都要带 task_id。" +
		"任务结束时会自动出现在下一轮的系统提示词里，所以只有现在就需要结果时才用 action=wait，" +
		"别用 sleep 之类的方式反复轮询。"
}

func (t TaskTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"action": map[string]any{
				"type":        "string",
				"enum":        []string{"list", "output", "wait", "stop"},
				"description": "要做什么：list 列任务、output 看输出、wait 等结束、stop 停掉",
			},
			"task_id": map[string]any{
				"type":        "string",
				"description": "任务 id（Bash 后台启动时返回的，形如 task_1），output/wait/stop 必填",
			},
			"tail_lines": map[string]any{
				"type":        "integer",
				"description": "action=output 时只返回最后多少行，默认 0（全部）",
			},
			"timeout_seconds": map[string]any{
				"type":        "integer",
				"description": fmt.Sprintf("action=wait 最多等几秒，默认 %d，最长 %d", int(defaultTaskWait.Seconds()), int(maxTaskWait.Seconds())),
			},
		},
		"required": []string{"action"},
	}
}

type TaskArgs struct {
	Action         string `json:"action"`
	TaskId         string `json:"task_id"`
	TailLines      int    `json:"tail_lines"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

func (t TaskTool) Execute(ctx context.Context, rawArgs json.RawMessage) (string, error) {
	if len(rawArgs) == 0 || string(rawArgs) == "null" {
		return "", fmt.Errorf("缺少参数: action")
	}
	var args TaskArgs
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return "", fmt.Errorf("参数格式错误: %w", err)
	}
	action := strings.ToLower(strings.TrimSpace(args.Action))
	if action == "" {
		return "", fmt.Errorf("action 不能为空（list / output / wait / stop）")
	}

	switch action {
	case "list":
		return listTasks(), nil
	case "output", "wait", "stop":
	default:
		return "", fmt.Errorf("不认识的 action: %s（只能是 list / output / wait / stop）", args.Action)
	}

	task, ok := backgroundTasks.get(strings.TrimSpace(args.TaskId))
	if !ok {
		return "", fmt.Errorf("没有这个任务: %q；先用 Task action=list 看看有哪些", args.TaskId)
	}

	// 模型主动来看过这个任务，就别再往系统提示词里补"它跑完了"。
	// 要等这次操作做完再标：wait 期间任务才结束的，标记得落在结束之后才算数
	var (
		out string
		err error
	)
	switch action {
	case "stop":
		out, err = stopTask(task)
	case "wait":
		out, err = waitTask(ctxOrBackground(ctx), task, args.TimeoutSeconds)
	default:
		out, err = taskOutput(task, args.TailLines)
	}
	task.markAnnounced()
	return out, err
}

// listTasks 列出所有任务，没有任务时说清楚怎么起一个
func listTasks() string {
	tasks := backgroundTasks.list()
	if len(tasks) == 0 {
		return "目前没有后台任务（用 Bash 的 background=true 起一个）"
	}
	var sb strings.Builder
	fmt.Fprintf(&sb, "%d 个后台任务:\n", len(tasks))
	for _, t := range tasks {
		sb.WriteString(t.summary() + "\n")
	}
	return strings.TrimRight(sb.String(), "\n")
}

// stopTask 杀掉任务（连它的子进程），等它真的结束再回话
func stopTask(task *bgTask) (string, error) {
	if task.state0() != taskRunning {
		return fmt.Sprintf("%s 已经结束（%s），不用再停\n最后输出:\n%s",
			task.ID, task.state0().Label(), taskOutputTail(task, 20)), nil
	}
	task.cancel()
	<-task.done
	status, exitCode, _, _ := task.state()
	return fmt.Sprintf("%s %s（exit_code: %d，耗时 %s）\n最后输出:\n%s",
		task.ID, status.Label(), exitCode, shortDuration(task.elapsed()), taskOutputTail(task, 20)), nil
}

// waitTask 等任务跑完，最多等 timeoutSeconds；到点还没完就如实说还在跑
func waitTask(ctx context.Context, task *bgTask, timeoutSeconds int) (string, error) {
	wait := defaultTaskWait
	if timeoutSeconds > 0 {
		wait = time.Duration(timeoutSeconds) * time.Second
		if wait > maxTaskWait {
			wait = maxTaskWait
		}
	}
	timer := time.NewTimer(wait)
	defer timer.Stop()

	select {
	case <-task.done:
	case <-timer.C:
		return fmt.Sprintf("%s 还在跑（已跑 %s，最多等了 %s）；可以继续用 action=wait，或者用 action=output 先看看进度",
			task.ID, shortDuration(task.elapsed()), shortDuration(wait)), nil
	case <-doneChan(ctx):
		return "", fmt.Errorf("等待被中断")
	}

	status, exitCode, reason, _ := task.state()
	head := fmt.Sprintf("%s %s（exit_code: %d，耗时 %s）", task.ID, status.Label(), exitCode, shortDuration(task.elapsed()))
	if reason != "" {
		head += "：" + reason
	}
	return head + "\n" + orNoOutput(task.out.String()), nil
}

// taskOutput 返回任务的当前状态和输出，tailLines > 0 时只留最后几行
func taskOutput(task *bgTask, tailLines int) (string, error) {
	status, exitCode, reason, _ := task.state()
	head := fmt.Sprintf("%s · %s · 已跑 %s · %s\n命令: %s",
		task.ID, status.Label(), shortDuration(task.elapsed()),
		describeWorkdir(task.Workdir), firstLine(task.Command))
	if status != taskRunning {
		head += fmt.Sprintf("\nexit_code: %d", exitCode)
	}
	if reason != "" {
		head += "\n原因: " + reason
	}
	if status == taskRunning {
		head += "\n任务还在跑，下面是到目前为止的输出："
	}
	out := task.out.String()
	if tailLines > 0 {
		out = lastLines(out, tailLines)
	}
	return head + "\n" + orNoOutput(out), nil
}

// taskOutputTail 取任务输出的最后几行，用在 stop 这类摘要里
func taskOutputTail(task *bgTask, n int) string {
	return orNoOutput(lastLines(task.out.String(), n))
}

// lastLines 取文本最后 n 行，n <= 0 时原样返回
func lastLines(s string, n int) string {
	if n <= 0 || s == "" {
		return s
	}
	lines := strings.Split(strings.TrimRight(s, "\n"), "\n")
	if len(lines) <= n {
		return s
	}
	return fmt.Sprintf("…（前面还有 %d 行没显示）\n%s", len(lines)-n, strings.Join(lines[len(lines)-n:], "\n"))
}

// doneChan 在没传 Context 时返回 nil，select 会一直等着任务结束
func doneChan(ctx context.Context) <-chan struct{} {
	if ctx == nil {
		return nil
	}
	return ctx.Done()
}
