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
	"unicode/utf8"
)

const (
	defaultBashTimeout = 30 * time.Second
	maxBashTimeout     = 120 * time.Second
	// maxBashOutput 是 stdout+stderr 合并后保留的字节数，超出时开头和结尾各留一半
	maxBashOutput = 64 * 1024
)

// shellSpec 描述命令要怎么交给 shell：程序路径 + 固定参数，命令本体作为最后一个参数
type shellSpec struct {
	Name string
	Path string
	Args []string
	Hint string // 给模型看的语法提示
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
			Name: "pwsh",
			Path: p,
			Args: []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command"},
			Hint: "（PowerShell 7：ls/cat/pwd/rm 等别名可用，支持 && 和管道）",
		}
	}
	if p, err := exec.LookPath("powershell"); err == nil {
		return shellSpec{
			Name: "powershell",
			Path: p,
			Args: []string{"-NoLogo", "-NoProfile", "-NonInteractive", "-Command"},
			Hint: "（Windows PowerShell 5.1：不支持 &&，多条命令请用 ; 分隔）",
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

func (t BashTool) Description() string {
	s := activeShell()
	return fmt.Sprintf("在指定工作目录执行 shell 命令，返回 exit_code 和 stdout/stderr 合并输出。"+
		"当前平台 %s/%s，shell 是 %s%s。"+
		"调用时必须用 intent 写一句这次要做什么，界面上会连完整命令一起展示给用户。"+
		"适合构建、测试、git、查看目录等；文件的读写和内容搜索请优先用 Read/Write/Edit/Grep。"+
		"不要执行交互式命令（vim、more、read）或长期运行的服务（npm start），它们会超时。",
		runtime.GOOS, runtime.GOARCH, s.Name, s.Hint)
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
				"description": "超时秒数，默认30，最大120",
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
}

func (t BashTool) Execute(rawArgs json.RawMessage) (string, error) {
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

	timeout := defaultBashTimeout
	if args.TimeoutSeconds > 0 {
		timeout = time.Duration(args.TimeoutSeconds) * time.Second
		if timeout > maxBashTimeout {
			timeout = maxBashTimeout
		}
	}

	// 先把工作目录的问题报清楚，别让 shell 启动失败的信息糊成一团
	workdir := strings.TrimSpace(args.Workdir)
	if workdir != "" {
		info, err := os.Stat(workdir)
		if err != nil {
			return "", fmt.Errorf("工作目录不可用 %s: %w", workdir, err)
		}
		if !info.IsDir() {
			return "", fmt.Errorf("工作目录 %s 不是目录", workdir)
		}
	}

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	// 命令作为独立参数传给 shell，避免 cmd 再做一次引号解析
	sh := activeShell()
	shArgs := append(append([]string{}, sh.Args...), args.Command)
	cmd := exec.CommandContext(ctx, sh.Path, shArgs...)
	if workdir != "" {
		cmd.Dir = workdir
	}
	// 不接 stdin，防止交互式命令一直等输入
	cmd.Stdin = nil
	// 关掉分页器、强制 git 非交互
	cmd.Env = append(os.Environ(),
		"GIT_PAGER=cat", "PAGER=cat", "TERM=dumb", "GIT_TERMINAL_PROMPT=0", "NO_COLOR=1",
	)

	out := newOutputBuffer(maxBashOutput)
	cmd.Stdout = out
	cmd.Stderr = out // 同一个 Writer，exec 内部会串行写，安全

	err := cmd.Run()

	if ctx.Err() == context.DeadlineExceeded {
		killTree(cmd.Process)
		return fmt.Sprintf("命令超时（%s），已终止\nshell: %s\nexit_code: -1\n%s", timeout, sh.Name, orNoOutput(out.String())), nil
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

// killTree 把子进程一起干掉，否则 Windows 上 npm / go test 这类会留下孤儿进程
func killTree(p *os.Process) {
	if p == nil {
		return
	}
	_ = p.Kill()
	if runtime.GOOS == "windows" {
		_ = exec.Command("taskkill", "/F", "/T", "/PID", strconv.Itoa(p.Pid)).Run()
	}
}

func orNoOutput(s string) string {
	if strings.TrimSpace(s) == "" {
		return "(无输出)"
	}
	return s
}

// outputBuffer 限制累计输出，超过上限时保留开头和结尾，中间只计数。
// 这样既不会把内存打爆（yes、大日志），又不会丢掉最有用的末尾报错。
type outputBuffer struct {
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
	if o.total <= o.headMax+o.tailMax {
		return string(o.head) + string(o.tail)
	}
	head := trimIncompleteRuneRight(o.head)
	tail := trimIncompleteRuneLeft(o.tail)
	var sb strings.Builder
	sb.Write(head)
	fmt.Fprintf(&sb, "\n...[输出过长，中间省略 %d 字节，只保留开头和结尾]\n", o.total-len(o.head)-len(o.tail))
	sb.Write(tail)
	return sb.String()
}

// trimIncompleteRuneRight 去掉末尾被截断的半个多字节字符
func trimIncompleteRuneRight(b []byte) []byte {
	for len(b) > 0 && !utf8.Valid(b) {
		b = b[:len(b)-1]
	}
	return b
}

// trimIncompleteRuneLeft 去掉开头被截断的半个多字节字符
func trimIncompleteRuneLeft(b []byte) []byte {
	for len(b) > 0 {
		r, size := utf8.DecodeRune(b)
		if r != utf8.RuneError || size > 1 {
			break
		}
		b = b[1:]
	}
	return b
}
