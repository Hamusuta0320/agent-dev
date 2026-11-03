package main

import (
	"fmt"
	"strings"

	"hamusuta/agent-dev/src/domain"
	"hamusuta/agent-dev/src/session"
)

// command 是本地命令，不发给模型
type command int

const (
	cmdNone command = iota
	cmdHelp
	cmdNew
	cmdResume
	cmdSession
	cmdConfig
	cmdMode
	cmdExit
)

// commandSpec 是一条本地命令的说明。帮助文本和输入补全都从这张表生成，
// 加命令只改这里一处。
type commandSpec struct {
	cmd  command
	name string
	desc string
}

var commandSpecs = []commandSpec{
	{cmdHelp, "/help", "看这个帮助"},
	{cmdNew, "/new", "开一个新会话"},
	{cmdResume, "/resume", "恢复以前的会话：不带参数列出来，带序号或 id 切过去"},
	{cmdSession, "/session", "看当前会话 id 和消息条数"},
	{cmdConfig, "/config", "配置模型：接口地址、密钥、模型名"},
	{cmdMode, "/mode", "看或切换工作模式：只读 / 编辑需授权 / 完全授权"},
	{cmdExit, "/exit", "退出（也可以 Ctrl+D）"},
}

// parseCommand 认出输入里的本地命令，认不出就当成普通提问
func parseCommand(input string) command {
	fields := strings.Fields(input)
	if len(fields) == 0 {
		return cmdNone
	}
	switch fields[0] {
	case "/help":
		return cmdHelp
	case "/new":
		return cmdNew
	case "/resume":
		return cmdResume
	case "/session":
		return cmdSession
	case "/config":
		return cmdConfig
	case "/mode":
		return cmdMode
	case "/exit", "/quit", "exit", "quit":
		return cmdExit
	}
	return cmdNone
}

// suggestCommands 返回能把当前输入补全的命令。
// 只在第一段（还没打空格）时补全；输入已经是某个完整命令时不再提示。
func suggestCommands(input string) []commandSpec {
	s := strings.TrimLeft(input, " \t")
	if !strings.HasPrefix(s, "/") || strings.ContainsAny(s, " \t\n") {
		return nil
	}
	var out []commandSpec
	for _, spec := range commandSpecs {
		if strings.HasPrefix(spec.name, s) {
			out = append(out, spec)
		}
	}
	if len(out) == 1 && out[0].name == s {
		return nil
	}
	return out
}

func helpText() string {
	lines := []string{bannerStyle.Render("命令")}
	for _, spec := range commandSpecs {
		lines = append(lines, fmt.Sprintf("  %-9s %s", spec.name, spec.desc))
	}
	lines = append(lines,
		"",
		bannerStyle.Render("授权"),
		"  编辑需授权模式下，写文件、改文件、执行命令前会停下来问你：",
		"  ↑↓ 或 j k 选选项，Enter 确认（默认就是允许一次）",
		"  y 允许一次 · a 本回合都允许这个工具 · n / Esc 拒绝",
		"  在确认框里直接打字，就当成\"拒绝并说明\"：写完回车，这句话会转给模型",
		"  只读模式不给写类工具；完全授权模式不再问",
		"",
		bannerStyle.Render("输入"),
		"  Enter 发送，Shift+Enter 换行（Ctrl+J 也行），粘贴多行文本会当成一次输入",
		"  输入以 / 开头时会列出可补全的命令，↑↓ 选择、Tab 或 Enter 补全",
		"  Tab 被终端接走时（比如 VS Code 集成终端），改用 Ctrl+Space 或 → 补全",
		"",
		bannerStyle.Render("图片"),
		"  截图后按 Ctrl+V（终端把 Ctrl+V 接走时用 Alt+V）贴剪贴板里的图",
		"  剪贴板里是文字时，这个键就是普通粘贴；终端自己接管 Ctrl+V 时走它自带的粘贴",
		"  也可以把图片文件直接拖进终端，或者粘贴图片的路径",
		"  附上的图会随下一条提问一起发给模型，输入框上方能看到带了哪几张",
		"  只贴图不写字也能发；Esc 清空附图；一条提问最多 "+fmt.Sprint(maxAttachments)+
			" 张，单张最大 "+fmt.Sprint(domain.MaxImageBytes>>20)+"MB",
	)
	return strings.Join(lines, "\n")
}

func sessionText(cur *domain.Session, workdir string, store *session.Store) string {
	dir := ""
	if store != nil {
		dir = store.Dir
	}
	out := fmt.Sprintf("工作区 %s\n会话 %s，共 %d 条消息", workdir, cur.Id, len(cur.Messages))
	if dir != "" {
		out += "\n存到 " + dir
	}
	return out
}

// modeText 是 /mode 不带参数时的说明：当前模式标出来，顺便列出怎么切
func modeText(cur domain.Mode) string {
	lines := []string{bannerStyle.Render("工作模式") +
		hintStyle.Render("  当前 ") + statusStyle.Render(cur.Label())}
	for _, mode := range []domain.Mode{domain.ModeReadOnly, domain.ModeAsk, domain.ModeFull} {
		mark := "  "
		style := hintStyle
		if mode == cur {
			mark, style = "▸ ", promptStyle
		}
		lines = append(lines, style.Render(fmt.Sprintf("%s%s  %-5s %s",
			mark, mode.Label(), mode.String(), modeHint(mode))))
	}
	lines = append(lines,
		"",
		hintStyle.Render("  切换：/mode read · /mode ask · /mode full，会写回配置文件"))
	return strings.Join(lines, "\n")
}

// modeHint 是一句话说明某个模式放行到什么程度
func modeHint(mode domain.Mode) string {
	switch mode {
	case domain.ModeReadOnly:
		return "只看不改：不给写文件、改文件、执行命令的工具"
	case domain.ModeFull:
		return "写操作直接执行，不再确认"
	default:
		return "写操作逐个确认：改哪个文件、跑什么命令都先摊开给你看"
	}
}
