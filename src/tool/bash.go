package tool

import (
	"bytes"
	"context"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
	"time"
)

const (
	defaultBashTimeout = 30 * time.Second
	maxBashTimeout     = 120 * time.Second
	maxBashOutput      = 64 * 1024
)

type BashTool struct{}

func (t BashTool) Name() string {
	return "Bash"
}

func (t BashTool) Description() string {
	return "在指定工作目录执行 shell 命令，返回退出码和 stdout/stderr。适合构建、测试、git、查看目录等。不要执行交互式命令（vim、more）或长期运行的服务（npm start），它们会超时。"
}

func (t BashTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
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
		"required": []string{"command"},
	}
}

type BashArgs struct {
	Command        string `json:"command"`
	Workdir        string `json:"workdir"`
	TimeoutSeconds int    `json:"timeout_seconds"`
}

func (t BashTool) Execute(rawArgs json.RawMessage) (string, error) {
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

	ctx, cancel := context.WithTimeout(context.Background(), timeout)
	defer cancel()

	var shell string
	var shellArgs []string
	if runtime.GOOS == "windows" {
		shell, shellArgs = "cmd", []string{"/C", args.Command}
	} else {
		shell, shellArgs = "sh", []string{"-c", args.Command}
	}

	cmd := exec.CommandContext(ctx, shell, shellArgs...)
	if args.Workdir != "" {
		cmd.Dir = args.Workdir
	}
	// 防止交互式命令等待 stdin 挂住
	cmd.Stdin = nil
	// 避免分页器、强制 git 非交互
	cmd.Env = append(os.Environ(),
		"GIT_PAGER=cat", "PAGER=cat", "TERM=dumb", "GIT_TERMINAL_PROMPT=0",
	)

	out := &limitedBuffer{max: maxBashOutput}
	cmd.Stdout = out
	cmd.Stderr = out // 和 Stdout 是同一个 Writer 时 exec 会串行写，安全

	err := cmd.Run()

	if ctx.Err() == context.DeadlineExceeded {
		return fmt.Sprintf("命令超时（%s），已终止\nexit_code: -1\n%s", timeout, out.String()), nil
	}

	exitCode := 0
	if err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) {
			exitCode = exitErr.ExitCode()
		} else {
			return "", fmt.Errorf("启动命令失败: %w", err)
		}
	}

	result := out.String()
	if out.truncated {
		result += fmt.Sprintf("\n...[输出已截断，仅保留前 %d 字节]", maxBashOutput)
	}
	if strings.TrimSpace(result) == "" {
		result = "(无输出)"
	}
	return fmt.Sprintf("exit_code: %d\n%s", exitCode, result), nil
}

// limitedBuffer 限制累计写入的字节数，防止 `yes`、大日志把内存打爆。
type limitedBuffer struct {
	buf       bytes.Buffer
	max       int
	truncated bool
}

func (b *limitedBuffer) Write(p []byte) (int, error) {
	remain := b.max - b.buf.Len()
	if remain <= 0 {
		b.truncated = true
		return len(p), nil
	}
	if len(p) > remain {
		b.buf.Write(p[:remain])
		b.truncated = true
		return len(p), nil
	}
	b.buf.Write(p)
	return len(p), nil
}

func (b *limitedBuffer) String() string {
	return b.buf.String()
}
