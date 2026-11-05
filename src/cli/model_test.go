package main

import (
	"fmt"
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hamusuta/agent-dev/src/config"
	"hamusuta/agent-dev/src/domain"
	"hamusuta/agent-dev/src/tool"

	"charm.land/lipgloss/v2"
)

// 两条新命令要认得出来，也能补全
func TestModelAndTodoCommands(t *testing.T) {
	if got := parseCommand("/model"); got != cmdModel {
		t.Fatalf("/model 没认出来: %v", got)
	}
	if got := parseCommand("/model 稳"); got != cmdModel {
		t.Fatalf("/model 带参数也该认出来: %v", got)
	}
	if got := parseCommand("/todo"); got != cmdTodo {
		t.Fatalf("/todo 没认出来: %v", got)
	}
}

// 测试用的两套模型配置
func testModels() []config.ModelConfig {
	return []config.ModelConfig{
		{Name: "快", Type: "DeepSeek", ApiUrl: "https://a", ApiKey: "sk-a", Model: "model-a", ContextLimit: 64000},
		{Name: "稳", Type: "OpenAI", ApiUrl: "https://b", ApiKey: "sk-b", Model: "model-b", ContextLimit: 256000},
	}
}

// /model 不带参数时把配置好的几套列出来，当前那套标出来
func TestModelText(t *testing.T) {
	text := stripANSI(modelText(testModels(), "快"))
	for _, want := range []string{"共 2 套", "当前用 快", "▸ 快", "model-b", "OpenAI", "https://b"} {
		if !strings.Contains(text, want) {
			t.Fatalf("/model 列表里少了 %q:\n%s", want, text)
		}
	}

	// 一套都没配时给句提示，告诉人往哪儿填
	empty := stripANSI(modelText(nil, ""))
	if !strings.Contains(empty, "/config") || !strings.Contains(empty, "Models") {
		t.Fatalf("没配模型时的提示不对:\n%s", empty)
	}
}

// /model 名字切过去：内存里、界面上、配置文件三处都得跟上
func TestSwitchModel(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	m := newModel(config.GlobalConfig{Models: testModels(), Current: "快"}, nil, ".", domain.NewSession("."))
	m.cfgPath = path

	if got := m.contextLimit; got != 64000 {
		t.Fatalf("上下文窗口该跟着当前模型走，实际 %d", got)
	}
	if got := m.modelLabel(); got != "快" {
		t.Fatalf("当前模型显示名不对: %q", got)
	}

	got, _ := m.switchModel("/model 稳")
	after := got.(model)
	if after.cfg.Current != "稳" {
		t.Fatalf("没切过去: %q", after.cfg.Current)
	}
	if after.contextLimit != 256000 {
		t.Fatalf("上下文窗口没跟着换: %d", after.contextLimit)
	}
	if after.modelLabel() != "稳" {
		t.Fatalf("界面上的模型名没换: %q", after.modelLabel())
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("配置没写回: %v", err)
	}
	for _, want := range []string{"Current: 稳", "model-b", "model-a"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("写回的配置里少了 %q:\n%s", want, data)
		}
	}

	// 认不出的名字不切，当前还是稳
	got, _ = after.switchModel("/model 不存在")
	if got.(model).cfg.Current != "稳" {
		t.Fatalf("认不出的模型不该切: %q", got.(model).cfg.Current)
	}
}

// 没配模型时界面照样能开，显示成"未配置"
func TestModelLabelWithoutConfig(t *testing.T) {
	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	if got := m.modelLabel(); got != "未配置" {
		t.Fatalf("没配模型时该显示未配置，实际 %q", got)
	}
	if got := m.contextLimit; got != config.DefaultContextLimit {
		t.Fatalf("没配模型时该用默认窗口，实际 %d", got)
	}
}

// 待办清单：没有待办就一行不占；有的逐条列出来，每行都不超过限宽（折行会把界面顶乱）
func TestTodoLines(t *testing.T) {
	if lines := todoLines(nil, 0, maxTodoRows); lines != nil {
		t.Fatalf("没有待办时不该有这一块: %v", lines)
	}

	items := []tool.TodoItem{
		{Content: "读代码", Status: tool.TodoCompleted},
		{Content: "改配置", Status: tool.TodoInProgress},
	}
	lines := stripANSI(strings.Join(todoLines(items, 0, maxTodoRows), "\n"))
	for _, want := range []string{"待办", "1/2", "1 项进行中", "[x] 1. 读代码", "[~] 2. 改配置"} {
		if !strings.Contains(lines, want) {
			t.Fatalf("待办清单里少了 %q:\n%s", want, lines)
		}
	}
	// 装得下时不提"第几条到第几条"，那只会让人以为还有更多
	if strings.Contains(lines, "项 ·") || strings.Contains(lines, "第 1-2 项") {
		t.Fatalf("全部列出来了还报窗口范围:\n%s", lines)
	}

	// 窄终端里只能截断：折行会把界面高度顶乱
	for limit := 10; limit <= 60; limit += 10 {
		for _, line := range todoLines(items, limit, maxTodoRows) {
			if w := lipgloss.Width(line); w > limit {
				t.Fatalf("限宽 %d 时有 %d 格宽的行，超了: %q", limit, w, stripANSI(line))
			}
		}
	}
}

// 待办一多就滚动：窗口以正在做的那一项开头，最多 maxTodoRows 条，前后放不下的折掉。
// 15 条做到第 6 条，列的就是 6-10。
func TestTodoWindowScrolls(t *testing.T) {
	build := func(n, cur int) []tool.TodoItem {
		items := make([]tool.TodoItem, n)
		for i := range items {
			status := tool.TodoPending
			switch {
			case cur < 0 || i < cur:
				status = tool.TodoCompleted
			case i == cur:
				status = tool.TodoInProgress
			}
			items[i] = tool.TodoItem{Content: fmt.Sprintf("第 %d 项", i+1), Status: status}
		}
		return items
	}

	for _, c := range []struct {
		name      string
		items     []tool.TodoItem
		wantFirst string
		wantLast  string
		wantRange string
	}{
		{"做到第 6 条", build(15, 5), "[~] 6. 第 6 项", "[ ] 10. 第 10 项", "第 6-10 项"},
		{"做到第 1 条", build(15, 0), "[~] 1. 第 1 项", "[ ] 5. 第 5 项", "第 1-5 项"},
		{"做到第 13 条", build(15, 12), "[x] 11. 第 11 项", "[ ] 15. 第 15 项", "第 11-15 项"},
		{"全做完了", build(15, -1), "[x] 11. 第 11 项", "[x] 15. 第 15 项", "第 11-15 项"},
	} {
		t.Run(c.name, func(t *testing.T) {
			lines := todoLines(c.items, 0, maxTodoRows)
			// 标题 + 5 条
			if len(lines) != 1+maxTodoRows {
				t.Fatalf("应该列出标题 + %d 条，实际 %d 行:\n%s",
					maxTodoRows, len(lines), stripANSI(strings.Join(lines, "\n")))
			}
			body := stripANSI(strings.Join(lines, "\n"))
			for _, want := range []string{c.wantFirst, c.wantLast, c.wantRange} {
				if !strings.Contains(body, want) {
					t.Fatalf("少了 %q:\n%s", want, body)
				}
			}
			// 只有窗口里那几条露头，窗口外的一条都不能出现
			start, end := todoWindow(c.items, maxTodoRows)
			for i, it := range c.items {
				entry := fmt.Sprintf("%d. %s", i+1, it.Content)
				if got, want := strings.Contains(body, entry), i >= start && i < end; got != want {
					t.Fatalf("第 %d 项（窗口 %d-%d）该不该显示弄反了:\n%s",
						i+1, start+1, end, body)
				}
			}
		})
	}

	// 终端矮到列不下一行时只留标题，进度总量还看得见
	if lines := todoLines(build(15, 5), 0, 0); len(lines) != 1 {
		t.Fatalf("一行都匀不出来时该只留标题，实际 %d 行", len(lines))
	}
}

// /todo 的输出：清单逐条列出来，空的时候说明怎么让它有
func TestTodoText(t *testing.T) {
	empty := stripANSI(todoText(nil))
	if !strings.Contains(empty, "空的") || !strings.Contains(empty, "TodoWrite") {
		t.Fatalf("空清单的说明不对:\n%s", empty)
	}

	text := stripANSI(todoText([]tool.TodoItem{
		{Content: "读代码", Status: tool.TodoCompleted},
		{Content: "改配置", Status: tool.TodoInProgress},
		{Content: "跑测试", Status: tool.TodoPending},
	}))
	for _, want := range []string{"1/3 完成", "1 项进行中", "[x] 1. 读代码", "[~] 2. 改配置", "[ ] 3. 跑测试"} {
		if !strings.Contains(text, want) {
			t.Fatalf("/todo 输出里少了 %q:\n%s", want, text)
		}
	}
}

// 待办清单只在本进程里，换会话时该清掉：它是上一个任务的进度表
func TestNewSessionClearsTodos(t *testing.T) {
	t.Cleanup(tool.ClearTodos)
	if _, err := (tool.TodoTool{}).Execute(nil, []byte(`{"todos":[{"content":"读代码","status":"pending"}]}`)); err != nil {
		t.Fatalf("准备清单失败: %v", err)
	}

	m := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	m.input.SetValue("/new")
	if got, _ := m.submit(); len(tool.CurrentTodos()) != 0 {
		t.Fatalf("新会话该清空待办，实际 %+v", tool.CurrentTodos())
	} else if got.(model).cur.Id == "" {
		t.Fatal("没开成新会话")
	}
}
