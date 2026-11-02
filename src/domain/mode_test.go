package domain

import "testing"

// 配置和 /mode 里的写法都要认，写错了要报错而不是悄悄变成别的模式
func TestParseMode(t *testing.T) {
	cases := map[string]Mode{
		"":         DefaultMode,
		"read":     ModeReadOnly,
		" READ ":   ModeReadOnly,
		"readonly": ModeReadOnly,
		"只读":       ModeReadOnly,
		"ask":      ModeAsk,
		"edit":     ModeAsk,
		"full":     ModeFull,
		"auto":     ModeFull,
		"完全授权":     ModeFull,
	}
	for input, want := range cases {
		got, err := ParseMode(input)
		if err != nil {
			t.Fatalf("%q 应该能解析: %v", input, err)
		}
		if got != want {
			t.Fatalf("%q 解析成了 %v，应该是 %v", input, got, want)
		}
	}

	if _, err := ParseMode("半自动"); err == nil {
		t.Fatal("认不出的模式应该报错")
	}
}

// String 和 ParseMode 要能对上，写回配置再用它读回来还是同一个模式
func TestModeStringRoundTrip(t *testing.T) {
	for _, mode := range []Mode{ModeReadOnly, ModeAsk, ModeFull} {
		got, err := ParseMode(mode.String())
		if err != nil {
			t.Fatalf("%v 的写法 %q 解析失败: %v", mode, mode.String(), err)
		}
		if got != mode {
			t.Fatalf("%q 解析成了 %v，应该是 %v", mode.String(), got, mode)
		}
		if mode.Label() == "" {
			t.Fatalf("%v 没有中文名字", mode)
		}
	}
}
