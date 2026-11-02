package tool

import (
	"context"

	"hamusuta/agent-dev/src/domain"
)

// ctxOrBackground 兜住没传 ctx 的调用方（测试、控制台直调），
// 免得每个工具都得先判一次 nil 才能用 ctx
func ctxOrBackground(ctx context.Context) context.Context {
	if ctx == nil {
		return context.Background()
	}
	return ctx
}

var readTool ReadTool
var bashTool BashTool
var editTool EditTool
var writeTool WriteTool
var grepTool GrepTool
var globTool GlobTool
var taskTool TaskTool

var Tools map[string]domain.Tool = map[string]domain.Tool{
	readTool.Name():  readTool,
	writeTool.Name(): writeTool,
	editTool.Name():  editTool,
	grepTool.Name():  grepTool,
	globTool.Name():  globTool,
	bashTool.Name():  bashTool,
	taskTool.Name():  taskTool,
}

// ForMode 返回当前工作模式下该交给模型的工具：只读模式把会改动磁盘的
// （Write / Edit / Bash）整个摘掉，模型拿不到就调不了，比事后拦更干净。
// 其它模式全给，"编辑需授权"由 core 在执行前逐个确认。
func ForMode(mode domain.Mode) map[string]domain.Tool {
	if mode != domain.ModeReadOnly {
		return Tools
	}
	out := make(map[string]domain.Tool, len(Tools))
	for name, t := range Tools {
		if t.Mutating() {
			continue
		}
		out[name] = t
	}
	return out
}
