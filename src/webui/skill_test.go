package webui

import (
	"net/http"
	"os"
	"path/filepath"
	"strings"
	"testing"
	"time"
)

// withSkills 在临时用户目录下摆一个技能，让 core.Skills 读得到。
// 技能清单每次现读磁盘（技能是用户随手装的，缓存了反而看不见新装的），
// 所以这里只要把 HOME 指到临时目录就行
func withSkills(t *testing.T, name, desc, body string) {
	t.Helper()
	home := t.TempDir()
	t.Setenv("HOME", home)
	t.Setenv("USERPROFILE", home) // Windows 上 os.UserHomeDir 读的是这个
	dir := filepath.Join(home, ".agents", "skills", name)
	if err := os.MkdirAll(dir, 0o755); err != nil {
		t.Fatalf("建技能目录失败: %v", err)
	}
	content := "---\nname: " + name + "\ndescription: " + desc + "\n---\n\n" + body + "\n"
	if err := os.WriteFile(filepath.Join(dir, "SKILL.md"), []byte(content), 0o644); err != nil {
		t.Fatalf("写技能失败: %v", err)
	}
}

// "/技能名 补充说明" 要拆成技能和后面那截；认不出就交回去让调用方报错
func TestSkillCall(t *testing.T) {
	withSkills(t, "add-disease", "添加病种", "先看病种表，再改 DiseaseCategory")

	skill, rest, ok := skillCall("/add-disease 加个病种，参考 XX")
	if !ok {
		t.Fatal("该认出技能")
	}
	if skill.Name != "add-disease" || rest != "加个病种，参考 XX" {
		t.Fatalf("拆得不对: %+v %q", skill, rest)
	}
	// 只打名字、没有补充说明也算
	if _, rest, ok := skillCall("/add-disease"); !ok || rest != "" {
		t.Fatalf("该认出技能、补充说明为空: %q %v", rest, ok)
	}
	if _, _, ok := skillCall("/zzz"); ok {
		t.Fatal("不认识的命令不该认成技能")
	}
	if _, _, ok := skillCall("帮我改个 bug"); ok {
		t.Fatal("普通提问不该认成技能")
	}
}

// 装好的技能要出现在补全候选里，名字前面带斜杠
func TestAPICommandsIncludesSkills(t *testing.T) {
	withSkills(t, "add-disease", "添加病种", "步骤")
	m := newTestManager(t)
	srv := newTestServer(t, m)

	code, body := sendJSON(t, http.MethodGet, srv.URL+"/api/commands", "")
	if code != http.StatusOK {
		t.Fatalf("拉命令表该 200，实际 %d: %s", code, body)
	}
	if !strings.Contains(body, "/add-disease") || !strings.Contains(body, "添加病种") {
		t.Fatalf("候选里该有装好的技能:\n%s", body)
	}
}

// 形状像命令却既不是内置命令也不是技能时挡住，别把 /zzz 发给模型
func TestHTTPUnknownCommandNotSent(t *testing.T) {
	m := newTestManager(t)
	withFakeProvider(t, &fakeProvider{steps: []fakeStep{{content: "不该跑到这儿"}}})
	srv := newTestServer(t, m)
	s := m.create()

	code, body := sendJSON(t, http.MethodPost, srv.URL+"/api/sessions/"+s.Data().Id+"/messages",
		`{"prompt":"/zzz 干点什么"}`)
	if code != http.StatusBadRequest {
		t.Fatalf("打错的命令该 400，实际 %d: %s", code, body)
	}
	if !strings.Contains(body, "没有叫 /zzz 的命令") {
		t.Fatalf("该说清是命令打错了:\n%s", body)
	}
	if s.Running() {
		t.Fatal("打错的命令不该起一轮")
	}

	// 带路径分隔符的不算命令，照旧当普通提问发出去
	if code, body := sendJSON(t, http.MethodPost, srv.URL+"/api/sessions/"+s.Data().Id+"/messages",
		`{"prompt":"/tmp/foo.log 这是啥"}`); code != http.StatusNoContent {
		t.Fatalf("/tmp/foo.log 该当普通提问发出去，实际 %d: %s", code, body)
	}

	// 这一轮是真在后台跑的：等它收尾再结束用例。不然 t.TempDir 清理时
	// 文件还在被写，Windows 上会报"目录非空"
	defer func() {
		s.Cancel()
		deadline := time.Now().Add(3 * time.Second)
		for time.Now().Before(deadline) && s.Running() {
			time.Sleep(10 * time.Millisecond)
		}
	}()
}
