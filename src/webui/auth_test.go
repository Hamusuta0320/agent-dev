package webui

import (
	"net/http"
	"net/http/httptest"
	"testing"

	"hamusuta/agent-dev/src/config"
)

// withAuth 在 newTestManager 的基础上配一组登录账号
func withAuth(t *testing.T, user, pass string) *Manager {
	t.Helper()
	m := newTestManager(t)
	m.cfg.WebAuth = &config.WebAuthConfig{Username: user, Password: pass}
	return m
}

// do 发一个请求，返回状态码；带凭证就带上
func do(t *testing.T, h http.Handler, path, user, pass string) *httptest.ResponseRecorder {
	t.Helper()
	req := httptest.NewRequest("GET", path, nil)
	if user != "" || pass != "" {
		req.SetBasicAuth(user, pass)
	}
	w := httptest.NewRecorder()
	h.ServeHTTP(w, req)
	return w
}

// 没配 WebAuth 时不要求登录：保持原来的行为，别把只想听 127.0.0.1 的人挡在外面
func TestRoutesNoAuthWhenUnconfigured(t *testing.T) {
	m := newTestManager(t)
	h := m.routes()
	if got := do(t, h, "/api/meta", "", "").Code; got != http.StatusOK {
		t.Fatalf("没配 WebAuth 时应该直接放行，实际 %d", got)
	}
}

// 只填了一半（比如忘了密码）不能当成"配了"：静默放行比报错更危险
func TestRoutesHalfConfiguredAuthIsIgnored(t *testing.T) {
	for _, c := range []struct{ user, pass string }{
		{"admin", ""},
		{"", "secret"},
		{"   ", "secret"},
	} {
		m := withAuth(t, c.user, c.pass)
		if got := do(t, m.routes(), "/api/meta", "", "").Code; got != http.StatusOK {
			t.Fatalf("WebAuth{%q,%q} 只配一半时不该拦人，实际 %d", c.user, c.pass, got)
		}
	}
}

// 配好了就拦：无凭证、错用户名、错密码都是 401，且带 WWW-Authenticate 让浏览器弹框
func TestRoutesRequiresAuth(t *testing.T) {
	m := withAuth(t, "admin", "s3cret")
	h := m.routes()

	for _, c := range []struct{ name, user, pass string }{
		{"无凭证", "", ""},
		{"用户名错", "root", "s3cret"},
		{"密码错", "admin", "wrong"},
		{"空密码", "admin", ""},
	} {
		w := do(t, h, "/api/meta", c.user, c.pass)
		if w.Code != http.StatusUnauthorized {
			t.Fatalf("%s: 应该 401，实际 %d", c.name, w.Code)
		}
		if got := w.Header().Get("WWW-Authenticate"); got == "" {
			t.Fatalf("%s: 401 必须带 WWW-Authenticate，否则浏览器不弹登录框", c.name)
		}
	}

	if got := do(t, h, "/api/meta", "admin", "s3cret").Code; got != http.StatusOK {
		t.Fatalf("凭证对了应该 200，实际 %d", got)
	}
}

// 用户名前后的空格不该影响登录（配置里手多打一个空格太常见了）
func TestAuthTrimsUsernameSpaces(t *testing.T) {
	m := withAuth(t, "  admin  ", "s3cret")
	if got := do(t, m.routes(), "/api/meta", "admin", "s3cret").Code; got != http.StatusOK {
		t.Fatalf("用户名两侧空格应该被忽略，实际 %d", got)
	}
}

// 认证盖住所有路径，不只是 /api：静态页、SSE 事件流、目录列表一个都不能漏。
// 这里只验状态码，真正的 SSE 行为在 events_test.go 里测
func TestAuthCoversEveryPath(t *testing.T) {
	m := withAuth(t, "admin", "s3cret")
	h := m.routes()
	for _, path := range []string{
		"/",
		"/index.html",
		"/style.css",
		"/api/meta",
		"/api/sessions",
		"/api/workspaces",
		"/api/fs/list?path=/",
		"/api/commands",
		"/api/config",
	} {
		if got := do(t, h, path, "", "").Code; got != http.StatusUnauthorized {
			t.Fatalf("%s: 未登录应该 401，实际 %d", path, got)
		}
	}
}

// 密码里有非 ASCII 字符时也要能登录（Basic 认证是字节比对，不该出编码问题）
func TestAuthNonASCIIPassword(t *testing.T) {
	m := withAuth(t, "管理员", "密码123")
	if got := do(t, m.routes(), "/api/meta", "管理员", "密码123").Code; got != http.StatusOK {
		t.Fatalf("中文账号密码应该能登录，实际 %d", got)
	}
}
