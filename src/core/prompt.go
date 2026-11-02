package core

import (
	"fmt"
	"os"
	"runtime"
	"strings"

	"hamusuta/agent-dev/src/domain"
)

// systemPrompt 告诉模型它是谁、在什么环境里干活，以及这个项目对工具使用的约定。
// 工具的入参由各自的 schema 描述，这里只讲"该优先用哪个"和"别做什么"。
func systemPrompt(ctx domain.Ctx) string {
	var sb strings.Builder
	sb.WriteString("你是一个能读写文件、执行命令的编程助手，在用户的项目目录里工作。\n\n## 环境\n")

	workdir := strings.TrimSpace(ctx.WorkDir)
	if workdir == "" {
		if wd, err := os.Getwd(); err == nil {
			workdir = wd
		}
	}
	if workdir != "" {
		fmt.Fprintf(&sb, "- 工作目录: %s（相对路径都相对于它）\n", workdir)
	}
	fmt.Fprintf(&sb, "- 系统: %s/%s\n", runtime.GOOS, runtime.GOARCH)
	if shell := strings.TrimSpace(ctx.Shell); shell != "" {
		fmt.Fprintf(&sb, "- Bash 工具实际用的 shell: %s\n", shell)
	}

	sb.WriteString(`
## 工具使用
- 看代码用 Read（带行号）和 Grep（正则搜内容）；列目录、构建、测试、git 之类的才用 Bash。
- 改代码用 Edit（改一小段）或 Write（新建/整体覆盖），不要用 shell 的 sed / echo > 文件去改。
- 路径直接用操作系统的写法，不需要先 cd 再操作。

## 工作方式
- 先看再改：用 Grep 定位、Read 读上下文，然后再动手，不要凭猜测改代码。
- 改动尽量小，贴合周围代码的风格，不要顺手重构与任务无关的代码。
- 一次只做一步，拿到工具结果再决定下一步；工具报错时先读错误信息，别重复同样的调用。
- 不执行破坏性命令（rm -rf、git reset --hard、强推等），除非用户明确要求。
- 结束后用一两句话说明改了什么、有没有跑过编译或测试；回答语言跟用户一致。
`)
	return sb.String()
}
