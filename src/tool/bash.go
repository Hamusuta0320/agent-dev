package tool

import (
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strconv"
	"strings"
	"sync"
	"time"

	"hamusuta/agent-dev/src/domain"
)

const (
	defaultBashTimeout = 30 * time.Second
	// maxBashTimeout 允许模型给慢命令（构建、装依赖）多留点时间，再长就该用后台任务了
	maxBashTimeout = 10 * time.Minute
	// maxBashOutput 是 stdout+stderr 合并后保留的字节数，超出时开头和结尾各留一半
	maxBashOutput = 64 * 1024
	// psUTF8Prefix 让 PowerShell 按 UTF-8 解码原生命令的输出、也按 UTF-8 写出去。
	// 中文 Windows 的默认代码页是 GBK，不改的话 go test、ls 这类输出里的中文会全变成乱码
	psUTF8Prefix = "[Console]::OutputEncoding=[Text.Encoding]::UTF8; "
)

// shellSpec 描述命令要怎么交给 shell：程序路径 + 固定参数，命令本体作为最后一个参数
type shellSpec struct {
	Name string
	Path string
	Args []string
	Hint string // 给模型看的语法提示
	// UTF8Prefix 是拼在命令最前面的固定语句，用来让 shell 按 UTF-8 解码子进程输出。
	// PowerShell 在中文 Windows 上默认按本地代码页（GBK）解码原生命令的输出，
	// Go 程序打的 UTF-8 中文到这儿就成了乱码，所以先把它掰回 UTF-8。
	UTF8Prefix string
}

var (
	shellOnce   sync.Once
	shellCached shellSpec
)

// ShellName 返回当前使用的 shell 名字，供系统提示词告知模型该用哪种语法
func ShellName() string {
	return activeShell().Name
}

func activeShell() shellSpec {
	shellOnce.Do(func() {
		shellCached = detectShell()
	})
	return shellCached
}

func detectShell() shellSpec {
	// 支持用 AGENT_SHELL 覆盖，例如 AGENT_SHELL="bash -c"
	if custom := strings.TrimSpace(os.Getenv("AGENT_SHELL")); custom != "" {
		parts := strings.Fields(custom)
		if len(parts) > 0 {
			return shellSpec{Name: parts[0], Path: parts[0], Args: parts[1:], Hint: "（由环境变量 AGENT_SHELL 指定）"}
		}
	}

	if runtime.GOOS != "windows" {
		if p, err := exec.LookPath("bash"); err == nil {
			return shellSpec{Name: "bash", Path: p, Args: []string{"-c"}}
		}
		return shellSpec{Name: "sh", Path: "sh", Args: []string{"-c"}}
	}

	// Windows 上优先 PowerShell 7（支持 &&、别名齐全、文本处理方便），
	// 其次 Windows PowerShell 5.1，最后才退回 cmd
	if p, err := exec.LookPath("pwsh"); err == nil {
		return shellSpec{
			Name:       "pwsh",
			Path:       p,
			Args:       []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command"},
			Hint:       "（PowerShell 7：ls/cat/pwd/rm 等别名可用，支持 && 和管道）",
			UTF8Prefix: psUTF8Prefix,
		}
	}
	if p, err := exec.LookPath("powershell"); err == nil {
		return shellSpec{
			Name:       "powershell",
			Path:       p,
			Args:       []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command"},
			Hint:       "（Windows PowerShell 5.1：不支持 &&，多条命令请用 ; 分隔）",
			UTF8Prefix: psUTF8Prefix,
		}
	}
	return shellSpec{
		Name: "cmd",
		Path: "cmd",
		Args: []string{"/D", "/S", "/C"},
		Hint: "（cmd 语法：用 dir / type / where / findstr / del，不要用 ls / cat / grep / pwd）",
	}
}

type BashTool struct{}

func (t BashTool) Name() string {
	return "Bash"
}

// Mutating 是 true：shell 里什么都能干，一律当成会改动磁盘
func (t BashTool) Mutating() bool { return true }

func (t BashTool) Description() string {
	s := activeShell()
	return fmt.Sprintf("在指定工作目录执行 shell 命令，返回 exit_code 和 stdout/stderr 合并输出。"+
		"当前平台 %s/%s，shell 是 %s%s。"+
		"调用时必须用 intent 写一句这次要做什么，界面上会连完整命令一起展示给用户。"+
		"timeout_seconds 指定超时时间（默认 30 秒，最长 %s），构建、装依赖这类慢命令记得调大。"+
		"background=true 让命令转到后台运行，立刻拿到任务 id，之后用 Task 工具看输出、等它结束或停掉它"+
		"（跑完会自动提醒你，不用轮询），适合 dev server、长时间构建这类不该卡住这一轮的命令。"+
		"前台适合构建、测试、git、查看目录等；文件的读写和内容搜索请优先用 Read/Write/Edit/Grep。"+
		"不要执行交互式命令（vim、more、read），它们会一直等输入。",
		runtime.GOOS, runtime.GOARCH, s.Name, s.Hint, shortDuration(maxBashTimeout))
}

func (t BashTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"intent": map[string]any{
				"type":        "string",
				"description": "这次调用的意图，一句话说明要做什么，比如\"查看项目目录结构\"，只用于展示给用户",
			},
			"command": map[string]any{
				"type":        "string",
				"description": "要执行的命令，支持管道、重定向和 &&",
			},
			"workdir": map[string]any{
				"type":        "string",
				"description": "工作目录，不传则使用当前目录",
			},
			"timeout_seconds": map[string]any{
				"type":        "integer",
				"description": fmt.Sprintf("超时秒数，默认 %d，最长 %d；后台任务到点也会被停掉", int(defaultBashTimeout.Seconds()), int(maxBashTimeout.Seconds())),
			},
			"background": map[string]any{
				"type":        "boolean",
				"description": "是否放到后台运行，默认 false。为 true 时立刻返回任务 id，用 Task 工具跟进（看输出/等结束/停掉）",
			},
		},
		"required": []string{"intent", "command"},
	}
}

type BashArgs struct {
	// Intent 是这次调用的意图，只用来在界面上展示，不影响执行
	Intent         string `json:"intent"`
	Command        string `json:"command"`
	Workdir        string `json:"workdir"`
	TimeoutSeconds int    `json:"timeout_seconds"`
	Background     bool   `json:"background"`
}

// Preview 是"编辑需授权"模式下给用户看的一句话摘要：要跑的命令原样摊开，
// 目录和意图放在补充说明里
func (t BashTool) Preview(rawArgs json.RawMessage) domain.ApprovalRequest {
	req := domain.ApprovalRequest{Kind: domain.ApprovalRunCommand, Target: "（命令参数不完整）"}
	var args BashArgs
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return req
	}
	command := strings.TrimRight(args.Command, "\r\n")
	if strings.TrimSpace(command) == "" {
		return req
	}
	req.Target = command
	var detail []string
	if intent := strings.TrimSpace(args.Intent); intent != "" {
		detail = append(detail, intent)
	}
	if wd := strings.TrimSpace(args.Workdir); wd != "" {
		detail = append(detail, "目录 "+wd)
	}
	if args.Background {
		detail = append(detail, "后台运行")
	}
	if args.TimeoutSeconds > 0 {
		detail = append(detail, "超时 "+shortDuration(bashTimeout(args.TimeoutSeconds)))
	}
	req.Detail = strings.Join(detail, " · ")
	return req
}

func (t BashTool) Execute(ctx context.Context, rawArgs json.RawMessage) (string, error) {
	if len(rawArgs) == 0 || string(rawArgs) == "null" {
		return "", fmt.Errorf("缺少参数: command")
	}

	var args BashArgs
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return "", fmt.Errorf("参数格式错误: %w", err)
	}
	if strings.TrimSpace(args.Command) == "" {
		return "", fmt.Errorf("command 不能为空")
	}
	if err := checkWorkdir(args.Workdir); err != nil {
		return "", err
	}
	timeout := bashTimeout(args.TimeoutSeconds)

	// 后台任务不跟着这一轮走：这一轮结束或用户按 Ctrl+C 都不该把它带走，
	// 要停就明确用 Task action=stop
	if args.Background {
		task, err := startBackgroundTask(args, timeout)
		if err != nil {
			return "", err
		}
		return fmt.Sprintf("已在后台启动 %s（%s，超时 %s）\n命令: %s\n用 Task action=output/wait/stop 跟进，task_id=%s",
			task.ID, describeWorkdir(task.Workdir), shortDuration(timeout), firstLine(task.Command), task.ID), nil
	}

	// 已经中断了就别启动子进程；没中断时把 ctx 当成父级，
	// 这样用户按 Ctrl+C 能立刻把命令（连它的子进程）停下来，不用等超时
	parent := ctxOrBackground(ctx)
	if err := parent.Err(); err != nil {
		return "", fmt.Errorf("命令没有执行: %w", err)
	}
	runCtx, cancel := context.WithTimeout(parent, timeout)
	defer cancel()

	sh := activeShell()
	cmd, err := shellCommand(runCtx, args)
	if err != nil {
		return "", err
	}

	out := newOutputBuffer(maxBashOutput)
	cmd.Stdout = out
	cmd.Stderr = out // 同一个 Writer，exec 内部会串行写，安全

	err = cmd.Run()

	switch {
	case errors.Is(runCtx.Err(), context.DeadlineExceeded):
		return fmt.Sprintf("命令超时（%s），已终止；慢命令可以调大 timeout_seconds（最长 %s），或者改用 background=true\nshell: %s\nexit_code: -1\n%s",
			shortDuration(timeout), shortDuration(maxBashTimeout), sh.Name, orNoOutput(out.String())), nil
	case runCtx.Err() != nil:
		// 父 Context 被取消：用户按了 Ctrl+C，进程树已经在 Cancel 里杀掉了
		return fmt.Sprintf("命令被中断，已终止\nshell: %s\nexit_code: -1\n%s", sh.Name, orNoOutput(out.String())), nil
	}

	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			return "", fmt.Errorf("启动命令失败（shell=%s）: %w", sh.Name, err)
		}
	}
	return fmt.Sprintf("exit_code: %d\n%s", exitCode, orNoOutput(out.String())), nil
}

// bashTimeout 把模型给的秒数换成实际超时：没给或给的不对用默认值，超上限的夹到上限
func bashTimeout(seconds int) time.Duration {
	if seconds <= 0 {
		return defaultBashTimeout
	}
	timeout := time.Duration(seconds) * time.Second
	if timeout > maxBashTimeout {
		timeout = maxBashTimeout
	}
	return timeout
}

// checkWorkdir 先把工作目录的问题报清楚，别让 shell 启动失败的信息糊成一团
func checkWorkdir(workdir string) error {
	workdir = strings.TrimSpace(workdir)
	if workdir == "" {
		return nil
	}
	info, err := os.Stat(workdir)
	if err != nil {
		return fmt.Errorf("工作目录不可用 %s: %w", workdir, err)
	}
	if !info.IsDir() {
		return fmt.Errorf("工作目录 %s 不是目录", workdir)
	}
	return nil
}

// shellCommand 组装一次 shell 调用：命令、环境、工作目录都在这儿统一处理。
// 超时或用户中断时除了子进程本身，还要把它拉起来的进程一起收拾，
// 否则 Windows 上 npm / go test 这类会留下孤儿进程继续跑。
func shellCommand(ctx context.Context, args BashArgs) (*exec.Cmd, error) {
	// 命令作为独立参数传给 shell，避免 cmd 再做一次引号解析
	sh := activeShell()
	command := args.Command
	if sh.UTF8Prefix != "" {
		command = sh.UTF8Prefix + command
	}
	shArgs := append(append([]string{}, sh.Args...), command)
	cmd := exec.CommandContext(ctx, sh.Path, shArgs...)
	cmd.Cancel = func() error {
		killTree(cmd.Process)
		return nil
	}
	if workdir := strings.TrimSpace(args.Workdir); workdir != "" {
		cmd.Dir = workdir
	}
	// 不接 stdin，防止交互式命令一直等输入
	cmd.Stdin = nil
	// 关掉分页器、强制 git 非交互
	cmd.Env = append(os.Environ(),
		"GIT_PAGER=cat", "PAGER=cat", "TERM=dumb", "GIT_TERMINAL_PROMPT=0", "NO_COLOR=1",
	)
	return cmd, nil
}

// shortDuration 把时长写成给人看的样子，工具描述和结果里都用它
func shortDuration(d time.Duration) string {
	switch {
	case d < time.Second:
		if d <= 0 {
			return "0 秒"
		}
		if ms := d.Milliseconds(); ms > 0 {
			return fmt.Sprintf("%d ms", ms)
		}
		return "<1 ms"
	case d >= time.Minute && d%time.Minute == 0:
		return fmt.Sprintf("%d 分钟", int(d.Minutes()))
	case d > time.Minute:
		return fmt.Sprintf("%d 分 %d 秒", int(d.Minutes()), int(d.Seconds())%60)
	default:
		if d%time.Second == 0 {
			return fmt.Sprintf("%d 秒", int(d.Seconds()))
		}
		return fmt.Sprintf("%.1f 秒", d.Seconds())
	}
}

// firstLine 取文本的第一行，用在任务摘要里
func firstLine(s string) string {
	s = strings.TrimSpace(s)
	if i := strings.IndexByte(s, '\n'); i >= 0 {
		s = s[:i]
	}
	return truncateGrepLine(s, maxGrepDisplayBytes)
}

// describeWorkdir 在工作目录摘要里区分"用了哪个目录"和"沿用当前目录"
func describeWorkdir(workdir string) string {
	if wd := strings.TrimSpace(workdir); wd != "" {
		return "目录 " + wd
	}
	return "当前目录"
}

// killTree 把子进程一起干掉，否则 Windows 上 npm / go test 这类会留下孤儿进程。
// Windows 上必须先用 taskkill 按 PID 整棵树地杀：父进程一死，/T 就找不到它的子进程了。
func killTree(p *os.Process) {
	if p == nil {
		return
	}
	if runtime.GOOS == "windows" {
		_ = exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(p.Pid)).Run()
	}
	_ = p.Kill()
}

func orNoOutput(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(无输出)"
	}
	return s
}

// outputBuffer 限制累计输出，超过上限时保留开头和结尾，中间只计数。
// 这样既不会把内存打爆（yes、大日志），又不会丢掉最有用的末尾报错。
// 后台任务里子进程写、模型来读是并发的，所以自带一把锁。
type outputBuffer struct {
	mu      sync.Mutex
	head    []byte
	tail    []byte
	headMax int
	tailMax int
	total   int
}

func newOutputBuffer(max int) *outputBuffer {
	if max < 2 {
		max = 2
	}
	return &outputBuffer{headMax: max / 2, tailMax: max - max/2}
}

func (o *outputBuffer) Write(p []byte) (int, error) {
	o.mu.Lock()
	defer o.mu.Unlock()
	n := len(p)
	o.total += n
	if len(o.head) < o.headMax {
		k := o.headMax - len(o.head)
		if k > len(p) {
			k = len(p)
		}
		o.head = append(o.head, p[:k]...)
		p = p[k:]
	}
	if len(p) > 0 {
		o.tail = append(o.tail, p...)
		if len(o.tail) > o.tailMax {
			o.tail = o.tail[len(o.tail)-o.tailMax:]
		}
	}
	return n, nil
}

func (o *outputBuffer) String() string {
	o.mu.Lock()
	defer o.mu.Unlock()
	if o.total <= o.headMax+o.tailMax {
		return decodeOutput(o.head) + decodeOutput(o.tail)
	}
	// 截断位置可能卡在字符中间：UTF-8 靠 rune 裁剪，转码后的内容靠去掉边界上的替换字符
	head := trimReplacementRight(decodeOutput(o.head))
	tail := trimReplacementLeft(decodeOutput(o.tail))
	var sb strings.Builder
	sb.WriteString(head)
	fmt.Fprintf(&sb, "\n...[输出过长，中间省略 %d 字节，只保留开头和结尾]\n", o.total-len(o.head)-len(o.tail))
	sb.WriteString(tail)
	return sb.String()
}

// trimReplacementRight 去掉截断处残留的半个字符（解码成了 U+FFFD）
func trimReplacementRight(s string) string {
	return strings.TrimSuffix(s, "\uFFFD")
}

func trimReplacementLeft(s string) string {
	return strings.TrimPrefix(s, "\uFFFD")
}
