package tool

import (
	"strconv"
	"strings"
	"testing"

	"hamusuta/agent-dev/src/domain"
)

// 只读模式不给会改动磁盘的工具，其它模式全给
func TestForMode(t *testing.T) {
	readOnly := ForMode(domain.ModeReadOnly)
	// Task 只跟进后台任务、自己不碰磁盘，只读模式下也留着
	for _, name := range []string{"Read", "Grep", "Glob", "Task"} {
		if _, ok := readOnly[name]; !ok {
			t.Fatalf("只读模式应该有 %s: %v", name, names(readOnly))
		}
	}
	for _, name := range []string{"Write", "Edit", "Bash"} {
		if _, ok := readOnly[name]; ok {
			t.Fatalf("只读模式不该有 %s: %v", name, names(readOnly))
		}
	}
	if len(readOnly) == 0 {
		t.Fatal("只读模式不该一个工具都没有")
	}

	for _, mode := range []domain.Mode{domain.ModeAsk, domain.ModeFull} {
		got := ForMode(mode)
		if len(got) != len(Tools) {
			t.Fatalf("%v 应该拿到全部工具，实际 %v", mode, names(got))
		}
	}
}

// All 不分模式给全套：浏览器那边把它递给 core，由 core 按"此刻"的模式现筛。
// FilterByMode 是那一层筛子，口径和 ForMode 一致
func TestAllAndFilterByMode(t *testing.T) {
	if got := All(); len(got) != len(Tools) {
		t.Fatalf("All 应该给全套工具，实际 %v", names(got))
	}
	// 非只读模式原样返回、不做拷贝：每一步都筛一遍，不该白白复制一张表
	for _, mode := range []domain.Mode{domain.ModeAsk, domain.ModeFull} {
		if got := FilterByMode(Tools, mode); len(got) != len(Tools) {
			t.Fatalf("%v 下不该筛掉工具，实际 %v", mode, names(got))
		}
	}
	readOnly := FilterByMode(Tools, domain.ModeReadOnly)
	if _, ok := readOnly["Write"]; ok {
		t.Fatalf("只读模式不该有 Write: %v", names(readOnly))
	}
	if _, ok := Tools["Write"]; !ok {
		t.Fatal("筛出来的那份不该动到原来那张表：之后切回完全授权还要用它")
	}
}

// 每个工具都要老实交代自己改不改磁盘，授权流程全靠它
func TestMutatingFlags(t *testing.T) {
	want := map[string]bool{
		"Read": false, "Grep": false, "Glob": false, "Task": false,
		// 待办清单只存在内存里，不碰磁盘
		"TodoWrite": false,
		"Write":     true, "Edit": true, "Bash": true,
	}
	for name, mutating := range want {
		tool, ok := Tools[name]
		if !ok {
			t.Fatalf("工具 %s 没注册", name)
		}
		if got := tool.Mutating(); got != mutating {
			t.Fatalf("%s 的 Mutating 是 %v，应该是 %v", name, got, mutating)
		}
	}
	if len(Tools) != len(want) {
		t.Fatalf("有工具没在表里交代清楚，注册了 %v", names(Tools))
	}
}

// 会改磁盘的工具都要能给出确认摘要：目标说得清，别只甩一串 JSON
func TestPreviewOfMutatingTools(t *testing.T) {
	cases := map[string]struct {
		args   string
		target string
		kind   domain.ApprovalKind
	}{
		"Bash":  {`{"intent":"跑测试","command":"go test ./...","workdir":"src"}`, "go test ./...", domain.ApprovalRunCommand},
		"Edit":  {`{"path":"a.go","old_string":"x","new_string":"y"}`, "a.go", domain.ApprovalWriteFile},
		"Write": {`{"path":"b.go","content":"line1\nline2\n"}`, "b.go", domain.ApprovalWriteFile},
	}
	for name, c := range cases {
		p, ok := Tools[name].(domain.Previewer)
		if !ok {
			t.Fatalf("%s 没实现 Previewer", name)
		}
		req := p.Preview([]byte(c.args))
		if req.Target != c.target {
			t.Fatalf("%s 的 Target 是 %q，应该是 %q", name, req.Target, c.target)
		}
		if req.Kind != c.kind {
			t.Fatalf("%s 的 Kind 是 %v，应该是 %v", name, req.Kind, c.kind)
		}
		if req.Detail == "" {
			t.Fatalf("%s 的 Detail 不该是空的", name)
		}
	}

	// 参数残缺时也要有个能看的摘要，不能崩
	if req := (BashTool{}).Preview([]byte(`{}`)); req.Target == "" {
		t.Fatal("参数不完整时也要说明白")
	}
}

func names(m map[string]domain.Tool) []string {
	out := make([]string, 0, len(m))
	for name := range m {
		out = append(out, name)
	}
	return out
}

// 改文件和写文件的确认里要带上改动预览：- 旧行 / + 新行
func TestPreviewDiff(t *testing.T) {
	edit := EditTool{}
	req := edit.Preview([]byte(`{"path":"a.go","old_string":"旧的一行\n旧的第二行","new_string":"新的一行"}`))
	if req.Diff == nil {
		t.Fatal("Edit 的确认里应该有改动预览")
	}
	if got := strings.Join(req.Diff.Removed, "|"); got != "旧的一行|旧的第二行" {
		t.Fatalf("删掉的行不对: %q", got)
	}
	if got := strings.Join(req.Diff.Added, "|"); got != "新的一行" {
		t.Fatalf("加上的行不对: %q", got)
	}
	if req.Diff.Truncated {
		t.Fatal("行数不多时不该标截断")
	}

	// 从 Read 结果里连行号一起复制过来的内容，预览里要把行号去掉
	req = edit.Preview([]byte(`{"path":"a.go","old_string":"    12→旧行","new_string":"    12→新行"}`))
	if got := strings.Join(req.Diff.Removed, "|"); got != "旧行" {
		t.Fatalf("预览里不该带行号: %q", got)
	}

	// 行数太多时只留开头几行，并标出截断
	many := strings.Repeat("一行\n", approvalDiffLines+5)
	req = edit.Preview([]byte(`{"path":"a.go","old_string":"x","new_string":` + strconv.Quote(many) + `}`))
	if req.Diff == nil || len(req.Diff.Added) != approvalDiffLines || !req.Diff.Truncated {
		t.Fatalf("长改动应该截断到 %d 行: %+v", approvalDiffLines, req.Diff)
	}

	// Write 的预览里只有加上的行
	w := WriteTool{}
	req = w.Preview([]byte(`{"path":"b.go","content":"第一行\n第二行\n"}`))
	if req.Diff == nil || len(req.Diff.Removed) != 0 || len(req.Diff.Added) != 2 {
		t.Fatalf("Write 的预览应该只有新增的行: %+v", req.Diff)
	}

	// 空内容没有预览，别画一个空的出来
	req = w.Preview([]byte(`{"path":"b.go","content":""}`))
	if req.Diff != nil {
		t.Fatalf("空内容不该有预览: %+v", req.Diff)
	}
}
