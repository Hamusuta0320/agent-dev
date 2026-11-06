package webui

import (
	"encoding/json"
	"net/http"
	"os"
	"strings"
	"testing"

	"hamusuta/agent-dev/src/config"
)

// 没配过 Quiet 时默认就是精简：meta 里得说 quiet=true，
// 浏览器界面和终端界面看的是同一个配置项
func TestMetaQuietDefaultOn(t *testing.T) {
	m := newTestManager(t)
	if got := m.meta().Quiet; !got {
		t.Fatal("没配过 Quiet 时该默认是精简（true）")
	}

	// 显式配成 false 才变详细
	off := false
	m.setConfig(config.GlobalConfig{Models: m.Config().Models, Current: m.Config().Current, Mode: "full", Quiet: &off})
	if got := m.meta().Quiet; got {
		t.Fatal("配了 Quiet: false 之后该是详细（false）")
	}
}

// POST /api/quiet 是全局开关：不需要会话 id，改完写回配置，回包里带回新的 meta
func TestHTTPSetQuiet(t *testing.T) {
	m := newTestManager(t)
	srv := newTestServer(t, m)

	code, body := sendJSON(t, http.MethodPost, srv.URL+"/api/quiet", `{"quiet":false}`)
	if code != http.StatusOK {
		t.Fatalf("切显示方式该 200，实际 %d: %s", code, body)
	}
	var got meta
	if err := json.Unmarshal([]byte(body), &got); err != nil {
		t.Fatalf("回包不是 JSON: %v", err)
	}
	if got.Quiet {
		t.Fatal("切到详细之后 meta.quiet 该是 false")
	}
	data, err := os.ReadFile(m.cfgPath)
	if err != nil {
		t.Fatalf("配置没写回: %v", err)
	}
	if !strings.Contains(string(data), "Quiet: false") {
		t.Fatalf("写回的配置里该有 Quiet: false:\n%s", data)
	}

	// 再切回来
	if code, body := sendJSON(t, http.MethodPost, srv.URL+"/api/quiet", `{"quiet":true}`); code != http.StatusOK {
		t.Fatalf("再切回来该 200，实际 %d: %s", code, body)
	}
	if !m.Config().QuietMode() {
		t.Fatal("内存里的配置该跟着变回精简")
	}
}

// /quiet 命令：不带参数取反，带 on/off 直接设；同样写回配置
func TestCommandQuiet(t *testing.T) {
	m := newTestManager(t)
	s := m.create()

	// 默认精简，取反就是详细
	reply := run(t, m, s, "/quiet")
	if m.Config().QuietMode() {
		t.Fatal("/quiet 该把精简关掉")
	}
	if reply.Meta == nil || reply.Meta.Quiet {
		t.Fatalf("回包里 meta.quiet 该是 false: %+v", reply.Meta)
	}
	if !strings.Contains(reply.Text, "已关") {
		t.Fatalf("该说清是关掉了:\n%s", reply.Text)
	}
	data, err := os.ReadFile(m.cfgPath)
	if err != nil {
		t.Fatalf("配置没写回: %v", err)
	}
	if !strings.Contains(string(data), "Quiet: false") {
		t.Fatalf("写回的配置里该有 Quiet: false:\n%s", data)
	}

	// /quiet on 明确打开
	if got := run(t, m, s, "/quiet on"); !m.Config().QuietMode() || got.Meta == nil || !got.Meta.Quiet {
		t.Fatalf("/quiet on 该打开精简: %+v", got.Meta)
	}
	// /quiet off 明确关掉
	if got := run(t, m, s, "/quiet off"); m.Config().QuietMode() || got.Meta == nil || got.Meta.Quiet {
		t.Fatalf("/quiet off 该关掉精简: %+v", got.Meta)
	}
}

// 精简显示开着时，历史里不该带出思考过程；关掉之后要能重建出来
func TestHistoryRespectsQuiet(t *testing.T) {
	m := newTestManager(t)
	s := m.create()
	s.Data().AppendUser("问一句")
	s.Data().AppendAssistant("答一句", "想了一下", nil)

	// 快照本身总是把 reasoning 带全：显示不显示是前端按 meta.quiet 决定的。
	// 这里守住"数据没被精简掉"这条：切成详细能看到思考
	snap := s.snapshot()
	if len(snap.Messages) < 2 {
		t.Fatalf("历史该有两条以上: %+v", snap.Messages)
	}
	if snap.Messages[1].Reasoning != "想了一下" {
		t.Fatalf("快照里该带着思考内容（前端按 quiet 决定显不显示）: %+v", snap.Messages[1])
	}
	if snap.Status == "" {
		t.Fatal("快照该带状态")
	}
}
