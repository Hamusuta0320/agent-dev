package core

import (
	"fmt"
	"strings"

	"hamusuta/agent-dev/src/domain"
	"hamusuta/agent-dev/src/tool"
)

// todoToolName 是待办工具的名字。core 不认工具实例，只认会话历史里的调用名：
// 只读模式下工具表里没有它，界面上仍然要能看见那份清单
const todoToolName = "TodoWrite"

// lastTodos 从会话历史里找最后一份待办清单。模型每次 TodoWrite 传的都是整份，
// 所以最后一条就是当前那份；没调过、或者最后一次的参数写坏了都返回 nil。
//
// 特意不读 tool.CurrentTodos()：那是进程级的一份，浏览器界面里多开几个会话会串。
// 从这条会话自己的历史里读，天然按会话隔离，跨回合也还在
func lastTodos(session *domain.Session) []tool.TodoItem {
	if session == nil {
		return nil
	}
	for i := len(session.Messages) - 1; i >= 0; i-- {
		calls := session.Messages[i].ToolCalls
		for j := len(calls) - 1; j >= 0; j-- {
			if calls[j].Name != todoToolName {
				continue
			}
			items, ok := tool.ParseTodoArgs(calls[j].Arguments)
			if !ok {
				// 参数坏了就当没列过清单：拿它判断"还有活没干完"会误催
				return nil
			}
			return items
		}
	}
	return nil
}

// maxTodoEdits 是一轮里允许改几次待办清单。留得很宽：一份五条的清单，
// 正常"开工列一次、逐条核销三四次、收尾一次"也就五六次，十次足够。
// 它只拦"拿清单打转"那种——实测有会话一轮改了 16 次
const maxTodoEdits = 10

// todoNotice 在模型准备收尾（这一轮不再调用工具）时检查待办清单，
// 还有没做完的条目就拼一条提醒让它回去核销。返回 nil 表示可以收尾。
//
// 为什么要这个自检：光靠系统提示词里那句"做完一项立刻改成 completed"不管用。
// 模型干长活时注意力全在代码上，收尾时忘了回头核销很常见——实测多条会话里
// 最后一次更新都停在"完成数 = 总条数 - 1"，最后一项永远挂着 in_progress，
// 或者干脆传个空数组把清单抹掉。
func todoNotice(session *domain.Session) []domain.AIChatMessage {
	var sb strings.Builder
	for i, it := range lastTodos(session) {
		if it.Status == tool.TodoCompleted {
			continue
		}
		if sb.Len() == 0 {
			sb.WriteString("## 待办清单\n这一轮准备收尾了，但你自己列的清单里还有没做完的：\n")
		}
		mark := "[ ]"
		if it.Status == tool.TodoInProgress {
			mark = "[~]"
		}
		// 序号用清单里的原始位置，别重新编：对不上会让模型以为说的是别的条目
		fmt.Fprintf(&sb, "%s %d. %s\n", mark, i+1, it.Content)
	}
	if sb.Len() == 0 {
		return nil
	}
	sb.WriteString("两条路选一条，别反复改同一条的措辞：\n")
	sb.WriteString("  1. 现在就能做完的，立刻用 TodoWrite 标成 completed；\n")
	sb.WriteString("  2. 这一轮做不了的，按实际情况办——要等用户拿主意、已经不需要了，\n")
	sb.WriteString("     或者必须等这一轮结束才谈得上验证（比如\"重启后生效\"，而重启正是在等这一轮跑完），\n")
	sb.WriteString("     就把它从清单里删掉，并在回答里说清楚为什么还欠着。\n")
	sb.WriteString("把\"重启生效\"改写成\"重启\"让它看起来能收尾，不算做完；" +
		"一轮里反复重写同一份清单也解决不了问题。")
	return []domain.AIChatMessage{{Role: domain.RoleUser, Content: sb.String()}}
}
