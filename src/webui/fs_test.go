package webui

import (
	"encoding/json"
	"net/http"
	"net/url"
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// testTree 造一棵选择器要看的小目录树：大小写混排的子目录、一个隐藏目录、
// 一个普通文件（文件不该出现在列表里）
func testTree(t *testing.T) string {
	t.Helper()
	dir := t.TempDir()
	for _, name := range []string{"beta", "Alpha", ".hidden"} {
		if err := os.Mkdir(filepath.Join(dir, name), 0o755); err != nil {
			t.Fatalf("建目录 %s 失败: %v", name, err)
		}
	}
	if err := os.WriteFile(filepath.Join(dir, "note.txt"), []byte("x"), 0o644); err != nil {
		t.Fatalf("建文件失败: %v", err)
	}
	return dir
}

func entryNames(entries []dirEntry) []string {
	out := make([]string, 0, len(entries))
	for _, e := range entries {
		out = append(out, e.Name)
	}
	return out
}

// 列子目录：只列目录、文件排除、大小写不敏感按名字排、隐藏目录照常返回
func TestListDirs(t *testing.T) {
	dir := testTree(t)
	m := newTestManager(t)

	list, err := m.listDirs(dir)
	if err != nil {
		t.Fatalf("列目录失败: %v", err)
	}
	if list.Roots {
		t.Fatal("普通目录不该标成根列表")
	}
	if list.Path != dir {
		t.Fatalf("当前路径不对: %q", list.Path)
	}
	if list.Parent != filepath.Dir(dir) {
		t.Fatalf("上一级不对: %q", list.Parent)
	}
	got := strings.Join(entryNames(list.Entries), ",")
	if want := ".hidden,Alpha,beta"; got != want {
		t.Fatalf("子目录列表不对: %q，想要 %q", got, want)
	}
	if list.Entries[0].Path != filepath.Join(dir, ".hidden") {
		t.Fatalf("条目路径不对: %+v", list.Entries[0])
	}
	if list.WorkDir != m.workdir || list.Home == "" {
		t.Fatalf("快捷入口没带上: %+v", list)
	}
}

// 不带 path 是根列表：Windows 是盘符，Unix 是 /。根那一层没有当前目录和上一级
func TestListDirsRoots(t *testing.T) {
	m := newTestManager(t)
	list, err := m.listDirs("")
	if err != nil {
		t.Fatalf("列根失败: %v", err)
	}
	if !list.Roots || list.Path != "" || list.Parent != "" {
		t.Fatalf("根列表的形状不对: %+v", list)
	}
	if len(list.Entries) == 0 {
		t.Fatal("根列表一项都没有")
	}
}

// 路径不存在、指向文件都报错；新建文件夹的名字不能借机穿越
func TestListDirErrors(t *testing.T) {
	dir := testTree(t)
	m := newTestManager(t)

	if _, err := m.listDirs(filepath.Join(dir, "没有这个目录")); err == nil {
		t.Fatal("不存在的目录该报错")
	}
	if _, err := m.listDirs(filepath.Join(dir, "note.txt")); err == nil ||
		!strings.Contains(err.Error(), "不是目录") {
		t.Fatalf("指向文件该说不是目录: %v", err)
	}

	for _, name := range []string{"", "  ", ".", "..", "../x", `..\x`, "a/b", `a\b`} {
		if _, err := createDir(dir, name); err == nil {
			t.Fatalf("新建文件夹名字 %q 不该通过", name)
		}
	}
}

// 新建文件夹：建出来能立刻在列表里看到；重名报错
func TestCreateDir(t *testing.T) {
	dir := testTree(t)
	m := newTestManager(t)

	created, err := createDir(dir, "新项目")
	if err != nil {
		t.Fatalf("新建失败: %v", err)
	}
	if want := filepath.Join(dir, "新项目"); created != want {
		t.Fatalf("新建路径不对: %q，想要 %q", created, want)
	}
	if fi, err := os.Stat(created); err != nil || !fi.IsDir() {
		t.Fatalf("目录没建出来: %v", err)
	}

	list, err := m.listDirs(dir)
	if err != nil {
		t.Fatalf("列目录失败: %v", err)
	}
	found := false
	for _, e := range list.Entries {
		if e.Name == "新项目" {
			found = true
		}
	}
	if !found {
		t.Fatalf("新建的目录没出现在列表里: %v", entryNames(list.Entries))
	}

	if _, err := createDir(dir, "新项目"); err == nil {
		t.Fatal("重名该报错")
	}
}

// 走 HTTP：列表接口和新建接口各自回对形状，失败回 400 和中文原因
func TestHTTPFSAPI(t *testing.T) {
	m := newTestManager(t)
	srv := newTestServer(t, m)
	dir := testTree(t)

	code, body := sendJSON(t, http.MethodGet, srv.URL+"/api/fs/list?path="+url.QueryEscape(dir), "")
	if code != http.StatusOK {
		t.Fatalf("列目录该 200，实际 %d: %s", code, body)
	}
	var list dirList
	if err := json.Unmarshal([]byte(body), &list); err != nil {
		t.Fatalf("响应不是 JSON: %v: %s", err, body)
	}
	if got := strings.Join(entryNames(list.Entries), ","); got != ".hidden,Alpha,beta" {
		t.Fatalf("列表不对: %q", got)
	}

	// 根列表
	code, body = sendJSON(t, http.MethodGet, srv.URL+"/api/fs/list", "")
	if code != http.StatusOK {
		t.Fatalf("列根该 200，实际 %d: %s", code, body)
	}
	if err := json.Unmarshal([]byte(body), &list); err != nil || !list.Roots {
		t.Fatalf("根列表响应不对: %v: %s", err, body)
	}

	// 坏路径 400
	if code, _ := sendJSON(t, http.MethodGet,
		srv.URL+"/api/fs/list?path="+url.QueryEscape(filepath.Join(dir, "nope")), ""); code != http.StatusBadRequest {
		t.Fatalf("不存在的目录该 400，实际 %d", code)
	}

	// 新建成功，重名和穿越 400
	code, body = sendJSON(t, http.MethodPost, srv.URL+"/api/fs/mkdir",
		`{"path":`+strconvQuote(dir)+`,"name":"web-项目"}`)
	if code != http.StatusOK {
		t.Fatalf("新建该 200，实际 %d: %s", code, body)
	}
	if fi, err := os.Stat(filepath.Join(dir, "web-项目")); err != nil || !fi.IsDir() {
		t.Fatalf("HTTP 建出来的目录不存在: %v", err)
	}
	if code, _ := sendJSON(t, http.MethodPost, srv.URL+"/api/fs/mkdir",
		`{"path":`+strconvQuote(dir)+`,"name":"web-项目"}`); code != http.StatusBadRequest {
		t.Fatalf("重名该 400，实际 %d", code)
	}
	if code, _ := sendJSON(t, http.MethodPost, srv.URL+"/api/fs/mkdir",
		`{"path":`+strconvQuote(dir)+`,"name":"../escape"}`); code != http.StatusBadRequest {
		t.Fatalf("带 .. 的名字该 400，实际 %d", code)
	}
}

// strconvQuote 把路径包成 JSON 字符串，省得手拼转义
func strconvQuote(s string) string {
	b, _ := json.Marshal(s)
	return string(b)
}
