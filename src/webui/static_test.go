package webui

import (
	"os"
	"path/filepath"
	"strings"
	"testing"
)

// 前端脚本里不能出现 innerHTML：模型输出（以及它读到的仓库文件）一律走
// createElement / textContent，这是这个界面防 XSS 的那道线。
// markdown.js 是唯一有可能破这条规矩的地方（排版最容易想着拼 HTML 字符串），
// 所以在这儿钉一根桩：谁哪天图省事写了 innerHTML / outerHTML / insertAdjacentHTML，
// 测试直接红。
func TestStaticScriptsNeverUseInnerHTML(t *testing.T) {
	dir := filepath.Join("static")
	entries, err := os.ReadDir(dir)
	if err != nil {
		t.Fatalf("读 static 目录失败: %v", err)
	}
	banned := []string{"innerHTML", "outerHTML", "insertAdjacentHTML", "document.write"}
	checked := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".js") {
			continue
		}
		data, err := os.ReadFile(filepath.Join(dir, name))
		if err != nil {
			t.Fatalf("读 %s 失败: %v", name, err)
		}
		// 注释里提到这些词是允许的（比如"绝不用 innerHTML"这种说明），
		// 所以按行判断：去掉行注释后再找
		for i, line := range strings.Split(string(data), "\n") {
			code := line
			if at := strings.Index(code, "//"); at >= 0 {
				code = code[:at]
			}
			for _, bad := range banned {
				if strings.Contains(code, bad) {
					t.Fatalf("%s 第 %d 行用了 %s，前端脚本一律不许拼 HTML：\n%s",
						name, i+1, bad, strings.TrimSpace(line))
				}
			}
		}
		checked++
	}
	if checked == 0 {
		t.Fatal("一个前端脚本都没检查到，路径不对？")
	}
}

// 页面要按顺序引 markdown.js 再引 app.js：app.js 里直接用 Markdown，
// 顺序反了就是 ReferenceError
func TestIndexLoadsMarkdownFirst(t *testing.T) {
	data, err := os.ReadFile(filepath.Join("static", "index.html"))
	if err != nil {
		t.Fatalf("读 index.html 失败: %v", err)
	}
	html := string(data)
	mdAt := strings.Index(html, "markdown.js")
	appAt := strings.Index(html, "app.js")
	if mdAt < 0 {
		t.Fatal("index.html 里没引 markdown.js")
	}
	if appAt < 0 {
		t.Fatal("index.html 里没引 app.js")
	}
	if mdAt > appAt {
		t.Fatal("markdown.js 必须排在 app.js 前面")
	}
}
