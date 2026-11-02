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
	sb.WriteString(modeSection(ctx.Mode))

	sb.WriteString(`
## 工具使用
- 看代码用 Read（带行号）和 Grep（正则搜内容）；列目录、找文件用 Glob（pattern 默认 * 就是列一层，**/*.go 递归）。
- 互不依赖的只读操作（Read / Glob / Grep）可以在同一条消息里一起发出来，它们会并行执行，快一些；会改磁盘的工具仍然一个一个跑。
- 改代码用 Edit（改一小段）或 Write（新建/整体覆盖），不要用 shell 的 sed / echo > 文件去改。
- 路径直接用操作系统的写法，不需要先 cd 再操作。

## 工作方式
- 先看再改：用 Glob 摸清目录、Grep 定位、Read 读上下文，然后再动手，不要凭猜测改代码。
- 改动尽量小，贴合周围代码的风格，不要顺手重构与任务无关的代码。
- 一次只做一步，拿到工具结果再决定下一步；工具报错时先读错误信息，别重复同样的调用。
- 不执行破坏性命令（rm -rf、git reset --hard、强推等），除非用户明确要求。
- 结束后用一两句话说明改了什么、有没有跑过编译或测试；回答语言跟用户一致。
`)
	return sb.String()
}

// modeSection 把当前工作模式的约束写进系统提示词。
// 只读模式下这些工具根本没发给模型，提前说明能省掉一次"A 工具不存在"的试错；
// 需授权模式下让模型知道被拒绝不是出错，而是用户的选择。
func modeSection(mode domain.Mode) string {
	switch mode {
	case domain.ModeReadOnly:
		return "- 工作模式: 只读（本次对话没有写文件、改文件、执行命令的工具）\n" +
			"  只能看和分析；需要改动时给出方案和要改的地方，让用户切到\"编辑需授权\"或\"完全授权\"再做。\n"
	case domain.ModeFull:
		return "- 工作模式: 完全授权（写操作直接执行，无需用户确认）\n" +
			"  没有确认这一关，所以更要保守：只改和当前任务相关的文件，破坏性操作仍然要先问。\n"
	default:
		return "- 工作模式: 编辑需授权（写文件、改文件、执行命令前会请用户确认）\n" +
			"  被拒绝时不要原样重试，换个不改文件的思路，或者先问用户打算怎么改。\n"
	}
}
