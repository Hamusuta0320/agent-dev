package command

import (
	"strings"
	"testing"
)

// 命令表本身要立得住：名字唯一、都以斜杠开头、说明不为空。
// 这张表是两个界面共用的，加命令时漏了说明或者名字打错都在这里拦住
func TestSpecs(t *testing.T) {
	seen := make(map[string]bool, len(Specs))
	for _, spec := range Specs {
		if !strings.HasPrefix(spec.Name, "/") {
			t.Fatalf("命令名该带前导斜杠: %q", spec.Name)
		}
		if strings.TrimSpace(spec.Desc) == "" {
			t.Fatalf("%s 缺说明", spec.Name)
		}
		if seen[spec.Name] {
			t.Fatalf("%s 重复了", spec.Name)
		}
		seen[spec.Name] = true
	}

	// Find 要认得出表里的每一条，IsKnown 跟着走
	for _, spec := range Specs {
		if got, ok := Find(spec.Name); !ok || got.Name != spec.Name {
			t.Fatalf("Find(%q) 没认出来", spec.Name)
		}
		if !IsKnown(spec.Name) {
			t.Fatalf("IsKnown(%q) 该是 true", spec.Name)
		}
	}
	if IsKnown("/zzz") {
		t.Fatal("/zzz 不该认成命令")
	}
	if len(Names()) != len(Specs) {
		t.Fatalf("Names 数量对不上: %d vs %d", len(Names()), len(Specs))
	}
}

// 前缀匹配：只在第一段、还没打空格时补全；打全了就不再提示
func TestSuggest(t *testing.T) {
	names := func(specs []Spec) []string {
		out := make([]string, 0, len(specs))
		for _, spec := range specs {
			out = append(out, spec.Name)
		}
		return out
	}

	if got := names(Suggest("/con")); len(got) != 1 || got[0] != "/config" {
		t.Fatalf("/con 该补成 /config，实际 %v", got)
	}
	if got := Suggest("/config"); got != nil {
		t.Fatalf("打全了不该再提示: %v", names(got))
	}
	if got := Suggest("/config "); got != nil {
		t.Fatalf("开始写参数了不该补全: %v", names(got))
	}
	if got := Suggest("你好"); got != nil {
		t.Fatalf("普通输入不该补全: %v", names(got))
	}
	if got := Suggest(""); got != nil {
		t.Fatalf("空输入不该补全: %v", names(got))
	}
	// 打一个斜杠：全部命令都列出来，第一条还是 /help
	all := Suggest("/")
	if len(all) != len(Specs) || all[0].Name != "/help" {
		t.Fatalf("打一个斜杠该列出全部 %d 条，第一条是 /help，实际 %v", len(Specs), names(all))
	}
	// 前面有空格也算在打命令。顺序按表来：/mode 在 /model 前面
	if got := names(Suggest("  /mo")); len(got) != 2 || got[0] != "/mode" || got[1] != "/model" {
		t.Fatalf("缩进过的输入也该补全，实际 %v", got)
	}
}

// Prefix 只在"正在打第一段命令名"时给出要比的那截
func TestPrefix(t *testing.T) {
	cases := map[string]string{
		"/co":   "/co",
		"  /co": "/co",
		"/co x": "",
		"你好":    "",
		"":      "",
		"/":     "/",
		"/a\tb": "",
		"x/co":  "",
	}
	for input, want := range cases {
		if got := Prefix(input); got != want {
			t.Fatalf("Prefix(%q) = %q，应该是 %q", input, got, want)
		}
	}
}

// 形状像命令的判定：/ 开头、后面全是名字里该有的字符
func TestLooksLike(t *testing.T) {
	cases := map[string]bool{
		"/zzz":         true,
		"/add-disease": true,
		"/a_b":         true,
		"/技能名":         true,
		"/":            false,
		"":             false,
		"/tmp/foo.log": false,
		"/a.b":         false,
		"hello":        false,
	}
	for input, want := range cases {
		if got := LooksLike(input); got != want {
			t.Fatalf("LooksLike(%q) = %v，应该是 %v", input, got, want)
		}
	}
}

// FirstField / Args 把一行输入拆成命令名和参数
func TestFirstFieldAndArgs(t *testing.T) {
	if got := FirstField("  /model 稳  "); got != "/model" {
		t.Fatalf("第一段取错了: %q", got)
	}
	if got := Args("  /model 稳  "); got != "稳" {
		t.Fatalf("参数取错了: %q", got)
	}
	if got := Args("/model"); got != "" {
		t.Fatalf("没参数时该是空串: %q", got)
	}
	if got := FirstField("   "); got != "" {
		t.Fatalf("空输入该给空串: %q", got)
	}
}

// 打错一条命令时列相近的：命令和技能一起找，最多列几个
func TestNear(t *testing.T) {
	got := Near("/disease", []string{"add-disease", "add-scheduled-task"})
	if len(got) != 1 || got[0] != "/add-disease" {
		t.Fatalf("该只挑出 /add-disease，实际 %v", got)
	}

	// 名字里含 model 的只有 /model 一条
	if got := Near("/model", nil); len(got) != 1 || got[0] != "/model" {
		t.Fatalf("该挑出 /model，实际 %v", got)
	}

	// 太宽的关键词也不该把一屏都列出来
	many := make([]string, 0, maxNear+5)
	for i := 0; i < maxNear+5; i++ {
		many = append(many, "a")
	}
	if got := Near("/a", many); len(got) > maxNear {
		t.Fatalf("最多列 %d 个，实际 %d 个", maxNear, len(got))
	}
}
