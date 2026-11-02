package domain

import "testing"

// 单位按 1024 进：1k = 1024，1M = 1024k = 1048576
func TestParseTokens(t *testing.T) {
	cases := []struct {
		in   string
		want int
	}{
		{"128000", 128000},
		{" 64000 ", 64000},
		{"128k", 128 * 1024},
		{"128K", 128 * 1024},
		{"128kb", 128 * 1024},
		{"128 kb", 128 * 1024},
		{"1M", 1024 * 1024},
		{"1m", 1024 * 1024},
		{"1.5M", 1572864},
		{"1g", 1073741824},
		{"200k", 204800},
		{"1", 1},
	}
	for _, c := range cases {
		got, err := ParseTokens(c.in)
		if err != nil {
			t.Fatalf("%q 应该能解析: %v", c.in, err)
		}
		if got != c.want {
			t.Fatalf("%q 应该是 %d，实际 %d", c.in, c.want, got)
		}
	}

	for _, bad := range []string{"", "  ", "abc", "k", "0", "-5", "-1M", "12x", "999999g"} {
		if got, err := ParseTokens(bad); err == nil {
			t.Fatalf("%q 应该报错，实际解析成 %d", bad, got)
		}
	}
}

// 一万以内保留一位小数，万以上取整；到 M 级别换成 M。单位同样按 1024 进，
// 跟 ParseTokens 对得上：128k 是 131072，1M 是 1048576
func TestFormatTokens(t *testing.T) {
	cases := []struct {
		in   int
		want string
	}{
		{0, "0"},
		{250, "250"},
		{1200, "1.2k"},
		{10000, "9.8k"},
		{128 * 1024, "128k"},
		{128000, "125k"},
		{1 << 20, "1.0M"},
		{2 << 20, "2.0M"},
		{200 << 20, "200M"},
	}
	for _, c := range cases {
		if got := FormatTokens(c.in); got != c.want {
			t.Fatalf("FormatTokens(%d) 应该是 %q，实际 %q", c.in, c.want, got)
		}
	}
}
