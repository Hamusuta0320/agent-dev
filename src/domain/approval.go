package domain

import "encoding/json"

// ApprovalKind 是这次要确认的操作类型，界面靠它决定用哪种措辞和配色
type ApprovalKind int

const (
	// ApprovalWriteFile 表示要写文件（新建、覆盖或改其中一段）
	ApprovalWriteFile ApprovalKind = iota
	// ApprovalRunCommand 表示要在 shell 里执行命令
	ApprovalRunCommand
)

// ApprovalRequest 是一次写操作在执行前提交给用户确认的内容。
// Target 是一条能一眼看懂的对象：文件路径，或者要执行的命令。
type ApprovalRequest struct {
	Tool   string
	Kind   ApprovalKind
	Target string
	// Detail 是补充说明，比如"覆盖已有文件（原 1.2KB）"、"3 行改成 5 行"、
	// 命令的意图和所在目录
	Detail string
	// Diff 是改动预览，只有改文件时才有；没有时确认框只显示 Detail
	Diff *DiffPreview
}

// DiffPreview 是这次改动到底动了什么的预览：去掉几行、加了几行。
// 只带开头几行，行数太多时 Truncated 为 true，免得一个确认框铺满整屏。
type DiffPreview struct {
	Removed   []string
	Added     []string
	Truncated bool
}

// Empty 判断预览里是不是一行都没有，没有就不用画这一段
func (d DiffPreview) Empty() bool { return len(d.Removed) == 0 && len(d.Added) == 0 }

// ApprovalAnswer 是用户对一次确认的回答
type ApprovalAnswer struct {
	Allowed bool
	// Always 为 true 表示本回合里这个工具不再问，直接放行
	Always bool
	// Feedback 是用户拒绝时顺手写下的话，比如"别动这个文件，改 config 就行"。
	// 会原样转给模型，让它知道该往哪个方向改
	Feedback string
}

// Previewer 是工具的可选能力：实现它就能在"编辑需授权"模式下给出更清楚的
// 确认摘要；没实现的工具退化成展示截断后的原始参数。
type Previewer interface {
	Preview(rawArgs json.RawMessage) ApprovalRequest
}
