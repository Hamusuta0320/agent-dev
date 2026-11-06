package tool

import (
	"context"
	"encoding/json"
	"strings"

	"hamusuta/agent-dev/src/domain"
)

// intentDesc 是所有工具共用的 intent 参数说明：让模型一句话交代"这次要干什么"，
// 界面把它显示在工具名后面，用户扫一眼就知道这一串调用在忙什么
const intentDesc = "这次调用的意图，一句话说明要做什么（比如\"读一下登录相关的代码\"），只用于展示给用户"

// intentProp 是各工具 schema 里通用的 intent 字段
func intentProp() map[string]any {
	return map[string]any{
		"type":        "string",
		"description": intentDesc,
	}
}

// ToolIntent 是所有工具参数里共有的 intent 字段。
// 界面不认各工具自己的参数结构，只从这个形状里取意图
type ToolIntent struct {
	Intent string `json:"intent"`
}

// CallIntent 从一次调用的原始参数里取出 intent，取不到（参数坏了或没传）就返回空串
func CallIntent(rawArgs string) string {
	var v ToolIntent
	if err := json.Unmarshal([]byte(rawArgs), &v); err != nil {
		return ""
	}
	return strings.TrimSpace(v.Intent)
}

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
var todoTool TodoTool
var webSearchTool WebSearchTool
var webFetchTool WebFetchTool

var Tools map[string]domain.Tool = map[string]domain.Tool{
	readTool.Name():  readTool,
	writeTool.Name(): writeTool,
	editTool.Name():  editTool,
	grepTool.Name():  grepTool,
	globTool.Name():  globTool,
	bashTool.Name():  bashTool,
	taskTool.Name():  taskTool,
	todoTool.Name():  todoTool,
	// 联网：搜索和抓取，都只读
	webSearchTool.Name(): webSearchTool,
	webFetchTool.Name():  webFetchTool,
}

// ForMode 返回当前工作模式下该交给模型的工具：只读模式把会改动磁盘的
// （Write / Edit / Bash）整个摘掉，模型拿不到就调不了，比事后拦更干净。
// 其它模式全给，"编辑需授权"由 core 在执行前逐个确认。
func ForMode(mode domain.Mode) map[string]domain.Tool {
	return FilterByMode(Tools, mode)
}

// FilterByMode 按模式筛一套工具，口径和 ForMode 一样。
// core 在跑的每一步都拿它把 ctx.Tools 重新筛一遍——浏览器那边能在这轮跑着的时候
// 切到只读，光在开跑时筛一次的话，切了也得等下一轮才收得回去
func FilterByMode(tools map[string]domain.Tool, mode domain.Mode) map[string]domain.Tool {
	if mode != domain.ModeReadOnly {
		return tools
	}
	out := make(map[string]domain.Tool, len(tools))
	for name, t := range tools {
		if t.Mutating() {
			continue
		}
		out[name] = t
	}
	return out
}

// All 返回全套工具，不分模式。浏览器界面那一侧递的就是它：core 会按"此刻"的模式
// 现筛，所以从只读切到完全授权时，写类工具也在手里，下一步就能发出去
func All() map[string]domain.Tool {
	return Tools
}
