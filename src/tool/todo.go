package tool

import (
	"context"
	"encoding/json"
	"fmt"
	"strings"
	"sync"
)

// 待办清单里一条的状态。只有这三个值，界面上也按它们上色
const (
	// TodoPending 是还没开始做的
	TodoPending = "pending"
	// TodoInProgress 是手上正在做的那一条，同一时刻最多一条
	TodoInProgress = "in_progress"
	// TodoCompleted 是做完了的
	TodoCompleted = "completed"
)

// TodoItem 是待办清单里的一条：content 写"要做什么"，status 是它走到哪一步了
type TodoItem struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

// todoStore 保存当前这份待办清单。工具实例本身是无状态的，
// 清单得挂在这儿才留得住；界面和 /todo 都从这儿读
type todoStore struct {
	mu    sync.Mutex
	items []TodoItem
}

var currentTodos = &todoStore{}

// set 整份换掉现有的清单
func (s *todoStore) set(items []TodoItem) {
	s.mu.Lock()
	defer s.mu.Unlock()
	s.items = items
}

// list 取一份快照，调用方随便改都不影响存着的那份
func (s *todoStore) list() []TodoItem {
	s.mu.Lock()
	defer s.mu.Unlock()
	return append([]TodoItem{}, s.items...)
}

// CurrentTodos 返回当前待办清单的快照，界面靠它显示进度
func CurrentTodos() []TodoItem {
	return currentTodos.list()
}

// ClearTodos 清空清单：换会话，或者模型把活干完了
func ClearTodos() {
	currentTodos.set(nil)
}

// TodoCounts 数一下完成了几条、几条在做
func TodoCounts(items []TodoItem) (done, running int) {
	for _, it := range items {
		switch it.Status {
		case TodoCompleted:
			done++
		case TodoInProgress:
			running++
		}
	}
	return done, running
}

// TodoCurrent 是"手上这条"的内容：有进行中的就是它，没有就退回第一条还没做的。
// 全都做完了返回空串
func TodoCurrent(items []TodoItem) string {
	for _, it := range items {
		if it.Status == TodoInProgress {
			return it.Content
		}
	}
	for _, it := range items {
		if it.Status != TodoCompleted {
			return it.Content
		}
	}
	return ""
}

// TodoProgress 是一句话的进度，例如"2/5 完成 · 进行中 改配置"
func TodoProgress(items []TodoItem) string {
	if len(items) == 0 {
		return ""
	}
	done, running := TodoCounts(items)
	out := fmt.Sprintf("%d/%d 完成", done, len(items))
	if cur := TodoCurrent(items); cur != "" {
		label := "待做 "
		if running > 0 {
			label = "进行中 "
		}
		out += " · " + label + cur
	}
	return out
}

// TodoList 把清单渲染成几行：做完的 [x]、在做的 [~]、还没动的 [ ]，
// 前面带序号，模型和用户都能一眼扫完
func TodoList(items []TodoItem) string {
	var sb strings.Builder
	for i, it := range items {
		mark := "[ ]"
		switch it.Status {
		case TodoCompleted:
			mark = "[x]"
		case TodoInProgress:
			mark = "[~]"
		}
		fmt.Fprintf(&sb, "%s %d. %s\n", mark, i+1, it.Content)
	}
	return strings.TrimRight(sb.String(), "\n")
}

// parseTodoStatus 把模型给的状态收成三种。留空算 pending；
// 认不出的直接报错——静默当成 pending 会让清单看起来莫名其妙
func parseTodoStatus(s string) (string, error) {
	switch strings.ToLower(strings.TrimSpace(s)) {
	case "", TodoPending:
		return TodoPending, nil
	case TodoInProgress, "in-progress", "doing":
		return TodoInProgress, nil
	case TodoCompleted, "done":
		return TodoCompleted, nil
	}
	return "", fmt.Errorf("不认识的 status: %q（只能是 pending / in_progress / completed）", s)
}

type TodoTool struct{}

func (t TodoTool) Name() string {
	return "TodoWrite"
}

// Mutating 是 false：清单只存在内存里，不碰磁盘。只读模式下也留着，
// 看代码的时候照样可以列一份清单
func (t TodoTool) Mutating() bool { return false }

func (t TodoTool) Description() string {
	return "维护这次任务的待办清单：每次把整份清单传上来，整份替换。" +
		"intent 写一句这次在做什么（比如\"把计划列出来\"），会显示在界面上。" +
		"多步任务（三步以上、要改好几个文件、要反复试错）先列出来再动手，做完一项立刻改成 completed，" +
		"任何时候最多一条 in_progress。只问一句、看一眼就能答的事别开清单。" +
		"传空数组就是清空。清单是给你自己盯进度用的，不是给用户的汇报。" +
		"要看后台命令用 Task，TodoWrite 只管清单。"
}

func (t TodoTool) Schema() map[string]any {
	return map[string]any{
		"type": "object",
		"properties": map[string]any{
			"intent": intentProp(),
			"todos": map[string]any{
				"type":        "array",
				"description": "整份清单，按执行顺序排；空数组表示清空",
				"items": map[string]any{
					"type": "object",
					"properties": map[string]any{
						"content": map[string]any{
							"type":        "string",
							"description": "要做什么，一句话一条（比如\"改 config.go 的 Provider 结构\"）",
						},
						"status": map[string]any{
							"type":        "string",
							"enum":        []string{TodoPending, TodoInProgress, TodoCompleted},
							"description": "pending 还没开始 / in_progress 正在做（同时只能一条）/ completed 做完了",
						},
					},
					"required": []string{"content", "status"},
				},
			},
		},
		"required": []string{"intent", "todos"},
	}
}

type TodoArgs struct {
	Intent string     `json:"intent"`
	Todos  []TodoItem `json:"todos"`
}

func (t TodoTool) Execute(ctx context.Context, rawArgs json.RawMessage) (string, error) {
	if len(rawArgs) == 0 || string(rawArgs) == "null" {
		return "", fmt.Errorf("缺少参数: todos（传空数组表示清空）")
	}
	var args TodoArgs
	if err := json.Unmarshal(rawArgs, &args); err != nil {
		return "", fmt.Errorf("参数格式错误: %w", err)
	}
	// 没传 todos 和传了空数组是两回事：前者是模型漏了参数，后者才是清空
	var probe struct {
		Todos *[]TodoItem `json:"todos"`
	}
	if err := json.Unmarshal(rawArgs, &probe); err != nil || probe.Todos == nil {
		return "", fmt.Errorf("缺少参数: todos（传空数组表示清空）")
	}

	items := make([]TodoItem, 0, len(args.Todos))
	for i, it := range args.Todos {
		content := strings.TrimSpace(it.Content)
		if content == "" {
			return "", fmt.Errorf("第 %d 条的 content 是空的：每一条都得写清楚要做什么", i+1)
		}
		if strings.ContainsAny(content, "\r\n") {
			return "", fmt.Errorf("第 %d 条的 content 里有换行：一条只写一件事，拆成两条", i+1)
		}
		status, err := parseTodoStatus(it.Status)
		if err != nil {
			return "", fmt.Errorf("第 %d 条（%s）: %w", i+1, content, err)
		}
		items = append(items, TodoItem{Content: content, Status: status})
	}

	currentTodos.set(items)
	if len(items) == 0 {
		return "待办已清空", nil
	}
	return fmt.Sprintf("待办已更新（%s）:\n%s", TodoProgress(items), TodoList(items)), nil
}
