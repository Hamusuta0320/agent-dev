package core

import (
	"bufio"
	"fmt"
	"io"
	"os"
	"strings"

	"hamusuta/agent-dev/src/domain"
)

// maxApprovalTries 是同一个确认最多追问几遍。敲错了就再问一次，
// 连着敲错几遍按拒绝处理，别让 Run 一直停在这儿。
const maxApprovalTries = 3

// consoleApprover 在控制台里问一句"要不要执行这次写操作"，等用户敲一行。
// 交互手感照着 Claude Code 来：默认选中"允许"，直接回车就放行。
type consoleApprover struct {
	in  *bufio.Reader
	out io.Writer
	// interactive 为 false 时不问：stdin 被重定向到文件或管道，问了也没人答
	interactive bool
}

func newConsoleApprover(in io.Reader, out io.Writer) *consoleApprover {
	return &consoleApprover{in: bufio.NewReader(in), out: out, interactive: isTerminal(in)}
}

// isTerminal 判断这个 Reader 是不是连着终端。只有 *os.File 才可能知道，
// 别的实现（测试里的 buffer）一律当成非交互，免得卡住等输入。
func isTerminal(in io.Reader) bool {
	f, ok := in.(*os.File)
	if !ok {
		return false
	}
	info, err := f.Stat()
	if err != nil {
		return false
	}
	return info.Mode()&os.ModeCharDevice != 0
}

// ask 把要确认的内容摊开，然后读一行回答
func (a *consoleApprover) ask(req domain.ApprovalRequest) domain.ApprovalAnswer {
	a.printRequest(req)
	if !a.interactive {
		fmt.Fprintln(a.out, "  当前不是交互式终端，没法确认，已按拒绝处理（要直接执行请 /mode full）")
		return domain.ApprovalAnswer{}
	}

	for range maxApprovalTries {
		fmt.Fprint(a.out, "> ")
		line, err := a.in.ReadString('\n')
		if strings.TrimSpace(line) == "" && err != nil {
			// EOF：stdin 关了（比如被重定向到这儿读完），只能按拒绝处理
			fmt.Fprintln(a.out)
			fmt.Fprintln(a.out, "  没读到回答，已按拒绝处理")
			return domain.ApprovalAnswer{}
		}
		if ans, ok := parseApprovalInput(line); ok {
			return ans
		}
		fmt.Fprintln(a.out, "  没看懂，回 y 允许 / a 本回合都允许 / n 拒绝（直接回车算允许）")
	}

	fmt.Fprintln(a.out, "  连着几遍都没看懂，已按拒绝处理")
	return domain.ApprovalAnswer{}
}

// printRequest 是控制台版的确认框：要改哪个文件、要跑什么命令，一屏说清
func (a *consoleApprover) printRequest(req domain.ApprovalRequest) {
	fmt.Fprintf(a.out, "\n[确认] %s 需要授权%s\n", req.Tool, approvalKindLabel(req.Kind))
	switch req.Kind {
	case domain.ApprovalRunCommand:
		for i, line := range strings.Split(req.Target, "\n") {
			line = strings.TrimRight(line, "\r")
			if i == 0 {
				fmt.Fprintf(a.out, "  $ %s\n", line)
				continue
			}
			fmt.Fprintf(a.out, "    %s\n", line)
		}
	default:
		fmt.Fprintf(a.out, "  %s\n", req.Target)
	}
	for _, line := range strings.Split(strings.TrimRight(req.Detail, "\n"), "\n") {
		if strings.TrimSpace(line) == "" {
			continue
		}
		fmt.Fprintf(a.out, "  %s\n", strings.TrimRight(line, "\r"))
	}
	fmt.Fprintf(a.out, "  允许吗？[y] 允许一次 · [a] 本回合都允许 %s · [n] 拒绝（直接回车算允许）\n", req.Tool)
}

// approvalKindLabel 是一句话说明这类操作意味着什么
func approvalKindLabel(kind domain.ApprovalKind) string {
	if kind == domain.ApprovalRunCommand {
		return "：执行命令"
	}
	return "：改动文件"
}

// parseApprovalInput 把一行回答解析成结论。
// 空输入（直接回车）算允许，跟 Claude Code 里默认选中 Yes 一个手感；
// 第二个返回值表示这行能不能看懂，看不懂时调用方会再问一次。
func parseApprovalInput(line string) (domain.ApprovalAnswer, bool) {
	switch strings.ToLower(strings.TrimSpace(line)) {
	case "", "y", "yes", "是", "允许", "1":
		return domain.ApprovalAnswer{Allowed: true}, true
	case "a", "always", "总是", "都允许", "2":
		return domain.ApprovalAnswer{Allowed: true, Always: true}, true
	case "n", "no", "否", "拒绝", "3":
		return domain.ApprovalAnswer{}, true
	}
	return domain.ApprovalAnswer{}, false
}

// askOnConsole 是给 consoleEmit 用的：每个 Run 一个确认器，
// 这样连续几次确认共用同一个缓冲，粘贴多行输入时不会漏掉后面的回答
func askOnConsole(a *consoleApprover, req domain.ApprovalRequest) domain.ApprovalAnswer {
	ans := a.ask(req)
	switch {
	case ans.Always:
		fmt.Fprintf(a.out, "  已允许，本回合不再问 %s\n", req.Tool)
	case ans.Allowed:
		fmt.Fprintln(a.out, "  已允许")
	default:
		fmt.Fprintln(a.out, "  已拒绝")
	}
	return ans
}
