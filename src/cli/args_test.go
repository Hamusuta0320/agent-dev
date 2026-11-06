package main

import "testing"

// 不带参数进终端界面；--web 可以带地址也可以不带
func TestParseArgs(t *testing.T) {
	cases := []struct {
		name    string
		args    []string
		wantWeb bool
		wantAdr string
	}{
		{"默认进 TUI", nil, false, ""},
		{"空参数跳过", []string{""}, false, ""},
		{"--web 不带地址", []string{"--web"}, true, ""},
		{"--web 带地址", []string{"--web", "127.0.0.1:9000"}, true, "127.0.0.1:9000"},
		{"--web=地址", []string{"--web=:8080"}, true, ":8080"},
		{"-web 也认", []string{"-web", "0.0.0.0:8080"}, true, "0.0.0.0:8080"},
		{"后面跟的是另一个参数时不当地址", []string{"--web", "-web"}, true, ""},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			addr, web, err := parseArgs(c.args)
			if err != nil {
				t.Fatalf("不该报错: %v", err)
			}
			if web != c.wantWeb {
				t.Fatalf("wantWeb 应该是 %v，实际 %v", c.wantWeb, web)
			}
			if addr != c.wantAdr {
				t.Fatalf("地址应该是 %q，实际 %q", c.wantAdr, addr)
			}
		})
	}
}

// 不认识的参数要报错：这个工具能读写磁盘，猜错代价比打一行用法大
func TestParseArgsUnknown(t *testing.T) {
	for _, args := range [][]string{{"--webb"}, {"--web", "-x"}, {"serve"}} {
		if _, _, err := parseArgs(args); err == nil {
			t.Fatalf("%v 应该报错", args)
		}
	}
}
