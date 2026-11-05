package tool

import (
	"strings"
	"testing"
)

// 整份替换：状态收成三种，留空算 pending，工具结果里带上清单
func TestTodoToolExecute(t *testing.T) {
	t.Cleanup(ClearTodos)

	out, err := (TodoTool{}).Execute(nil, []byte(`{"intent":"把计划列出来","todos":[
		{"content":"读代码","status":"completed"},
		{"content":"改配置","status":"in_progress"},
		{"content":"跑测试"}
	]}`))
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	for _, want := range []string{"1/3 完成", "进行中 改配置", "[x] 1. 读代码", "[~] 2. 改配置", "[ ] 3. 跑测试"} {
		if !strings.Contains(out, want) {
			t.Fatalf("结果里少了 %q:\n%s", want, out)
		}
	}

	items := CurrentTodos()
	if len(items) != 3 {
		t.Fatalf("清单应该有 3 条，实际 %+v", items)
	}
	if items[2].Status != TodoPending {
		t.Fatalf("没写 status 的应该算 pending，实际 %q", items[2].Status)
	}

	// 传空数组就是清空
	out, err = (TodoTool{}).Execute(nil, []byte(`{"intent":"收工","todos":[]}`))
	if err != nil {
		t.Fatalf("清空失败: %v", err)
	}
	if !strings.Contains(out, "清空") || len(CurrentTodos()) != 0 {
		t.Fatalf("空数组该清空清单: %q %+v", out, CurrentTodos())
	}
}

// 参数不对就报错，别默默存一份看不懂的清单
func TestTodoToolValidation(t *testing.T) {
	t.Cleanup(ClearTodos)

	cases := map[string]string{
		"没有参数":     ``,
		"少了 todos": `{"intent":"x"}`,
		"内容空":      `{"todos":[{"status":"pending"}]}`,
		"状态认不出":    `{"todos":[{"content":"a","status":"weird"}]}`,
		"内容带换行":    `{"todos":[{"content":"a\nb","status":"pending"}]}`,
	}
	for name, raw := range cases {
		//nolint:staticcheck // 故意传 nil：待办工具本来就不看 ctx
		if _, err := (TodoTool{}).Execute(nil, []byte(raw)); err == nil {
			t.Fatalf("%s 应该报错", name)
		}
	}
	if len(CurrentTodos()) != 0 {
		t.Fatalf("报错的调用不该留下清单: %+v", CurrentTodos())
	}
}

// 进度和清单的文案：有进行中的说进行中，全做完了不再提手里这项
func TestTodoProgressAndList(t *testing.T) {
	if got := TodoProgress(nil); got != "" {
		t.Fatalf("空清单不该有进度: %q", got)
	}

	items := []TodoItem{
		{Content: "读代码", Status: TodoCompleted},
		{Content: "改配置", Status: TodoInProgress},
		{Content: "跑测试", Status: TodoPending},
	}
	if got := TodoProgress(items); got != "1/3 完成 · 进行中 改配置" {
		t.Fatalf("进度文案不对: %q", got)
	}
	if got := TodoCurrent(items); got != "改配置" {
		t.Fatalf("手上这项应该是进行中的那条，实际 %q", got)
	}

	// 没有进行中就退回第一条还没做的
	items[1].Status = TodoCompleted
	if got := TodoProgress(items); got != "2/3 完成 · 待做 跑测试" {
		t.Fatalf("没有进行中时该说待做: %q", got)
	}
	// 全做完了就只报进度
	items[2].Status = TodoCompleted
	if got := TodoProgress(items); got != "3/3 完成" {
		t.Fatalf("全做完时不该再提手里这项: %q", got)
	}
	if got := TodoCurrent(items); got != "" {
		t.Fatalf("全做完时手上没有活: %q", got)
	}

	if got := TodoList(items); got != "[x] 1. 读代码\n[x] 2. 改配置\n[x] 3. 跑测试" {
		t.Fatalf("清单渲染不对:\n%s", got)
	}
}
