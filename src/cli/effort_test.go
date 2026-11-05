package main

import (
	"os"
	"path/filepath"
	"strings"
	"testing"

	"hamusuta/agent-dev/src/config"
	"hamusuta/agent-dev/src/domain"
)

// 界面上要看得到当前档位：开场那两行和底部状态栏都带上，没配档位时显示"默认"，
// /effort 改过之后界面立刻跟着变
func TestEffortShownInUI(t *testing.T) {
	models := testModels()
	models[0].ReasoningEffort = "high"
	m := newModel(config.GlobalConfig{Models: models, Current: "快"}, nil, ".", domain.NewSession("."))
	m.width = 200

	if got := m.effortLabel(); got != "high" {
		t.Fatalf("当前档位不对: %q", got)
	}
	banner := stripANSI(strings.Join(m.bannerLines(), "\n"))
	if !strings.Contains(banner, "思考 high") {
		t.Fatalf("开场那几行里该有思考强度:\n%s", banner)
	}
	footer := stripANSI(m.footerLine())
	if !strings.Contains(footer, "思考 high") {
		t.Fatalf("底部状态栏里该有思考强度:\n%s", footer)
	}

	// 跑起来的时候也看得见：那会儿开场那几行早滚上去了
	m.state = stateRunning
	if running := stripANSI(m.footerLine()); !strings.Contains(running, "思考 high") {
		t.Fatalf("运行中的底栏也该有思考强度:\n%s", running)
	}

	// 没配档位的那套显示"默认"
	m.state = stateIdle
	moved, _ := m.switchModel("/model 稳")
	if got := moved.(model).effortLabel(); got != "默认" {
		t.Fatalf("没配档位该显示默认: %q", got)
	}

	// /effort 改完界面立刻跟上，不用重启
	changed, _ := moved.(model).adjustEffort("/effort low")
	if got := changed.(model).effortLabel(); got != "low" {
		t.Fatalf("改完档位界面没跟上: %q", got)
	}
	if footer := stripANSI(changed.(model).footerLine()); !strings.Contains(footer, "思考 low") {
		t.Fatalf("改完底栏还显示旧档位:\n%s", footer)
	}
}

// /effort 要认得出来（带参数也认），并且出现在补全候选里
func TestEffortCommandParsed(t *testing.T) {
	if got := parseCommand("/effort"); got != cmdEffort {
		t.Fatalf("/effort 没认出来: %v", got)
	}
	if got := parseCommand("/effort high"); got != cmdEffort {
		t.Fatalf("/effort 带参数也该认出来: %v", got)
	}

	var found bool
	for _, spec := range suggestCommands("/eff") {
		if spec.name == "/effort" {
			found = true
		}
	}
	if !found {
		t.Fatal("/eff 的补全候选里该有 /effort")
	}
}

// effortArg 认档位名，也认"默认 / auto / off"这些回到默认档的说法；别的都算认不出
func TestEffortArg(t *testing.T) {
	for _, c := range []struct {
		in   string
		want string
	}{
		{"high", "high"},
		{"  Medium ", "medium"},
		{"minimal", "minimal"},
		{"默认", ""},
		{"default", ""},
		{"off", ""},
		{"none", ""},
	} {
		got, ok := effortArg(c.in)
		if !ok {
			t.Fatalf("%q 该认下来", c.in)
		}
		if got != c.want {
			t.Fatalf("%q 认成 %q，想要 %q", c.in, got, c.want)
		}
	}

	for _, bad := range []string{"很努力", "ludicrous", "1"} {
		if _, ok := effortArg(bad); ok {
			t.Fatalf("%q 不该认下来", bad)
		}
	}
}

// /effort 不带参数时把当前档位和可选项列出来，当前那档标出来
func TestEffortText(t *testing.T) {
	text := stripANSI(effortText(config.ModelConfig{Name: "快", Model: "model-a", ReasoningEffort: "high"}))
	for _, want := range []string{"当前 high", "▸ high", "minimal", "默认", "/effort low"} {
		if !strings.Contains(text, want) {
			t.Fatalf("/effort 列表里少了 %q:\n%s", want, text)
		}
	}

	// 没配过的人看到的是"默认"档：这时候不发 reasoning_effort
	def := stripANSI(effortText(config.ModelConfig{Name: "稳"}))
	for _, want := range []string{"当前 默认", "▸ 默认", "不发这个参数"} {
		if !strings.Contains(def, want) {
			t.Fatalf("默认档没标出来（%q）:\n%s", want, def)
		}
	}
}

// /effort high 就地改当前这套的档位：内存、配置文件都得跟上，另一套不受影响
func TestAdjustEffortWritesConfig(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	m := newModel(config.GlobalConfig{Models: testModels(), Current: "快"}, nil, ".", domain.NewSession("."))
	m.cfgPath = path

	got, _ := m.adjustEffort("/effort high")
	after := got.(model)
	cur, err := after.cfg.CurrentModel()
	if err != nil {
		t.Fatalf("改完读当前模型失败: %v", err)
	}
	if cur.ReasoningEffort != "high" {
		t.Fatalf("内存里的档位没改: %+v", cur)
	}
	if after.cfg.Current != "快" {
		t.Fatalf("当前模型不该被换掉: %q", after.cfg.Current)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("配置没写回: %v", err)
	}
	for _, want := range []string{"ReasoningEffort: high", "Current: 快", "Name: 稳"} {
		if !strings.Contains(string(data), want) {
			t.Fatalf("写回的配置里少了 %q:\n%s", want, data)
		}
	}

	// 只改当前这套，另一套得原样留着
	for _, mc := range after.cfg.AvailableModels() {
		if mc.Label() == "稳" && mc.ReasoningEffort != "" {
			t.Fatalf("另一套的档位不该被改: %+v", mc)
		}
	}

	// 再切到另一套去改，改的是那一套
	moved, _ := after.switchModel("/model 稳")
	then, _ := moved.(model).adjustEffort("/effort low")
	for _, mc := range then.(model).cfg.AvailableModels() {
		switch mc.Label() {
		case "快":
			if mc.ReasoningEffort != "high" {
				t.Fatalf("前面那套该还是 high: %+v", mc)
			}
		case "稳":
			if mc.ReasoningEffort != "low" {
				t.Fatalf("刚切的这套该是 low: %+v", mc)
			}
		}
	}
}

// /effort 默认 回到"不发这个参数"，配置文件里那一行也该跟着消失
func TestAdjustEffortBackToDefault(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	m := newModel(config.GlobalConfig{
		Models: []config.ModelConfig{{
			Name: "快", Type: "DeepSeek", ApiUrl: "https://a", ApiKey: "sk-a", Model: "model-a",
			ReasoningEffort: "high",
		}},
		Current: "快",
	}, nil, ".", domain.NewSession("."))
	m.cfgPath = path

	got, _ := m.adjustEffort("/effort 默认")
	after := got.(model)
	cur, err := after.cfg.CurrentModel()
	if err != nil {
		t.Fatalf("读当前模型失败: %v", err)
	}
	if cur.ReasoningEffort != "" {
		t.Fatalf("该回到不发这个参数的默认档: %+v", cur)
	}

	data, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("配置没写回: %v", err)
	}
	if strings.Contains(string(data), "ReasoningEffort") {
		t.Fatalf("回到默认档后配置里不该留着这一行:\n%s", data)
	}
}

// 认不出的档位不写配置、不改内存；模型一套都没配时也不该崩
func TestAdjustEffortUnknown(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	m := newModel(config.GlobalConfig{Models: testModels(), Current: "快"}, nil, ".", domain.NewSession("."))
	m.cfgPath = path

	got, _ := m.adjustEffort("/effort 很努力")
	after := got.(model)
	if cur, err := after.cfg.CurrentModel(); err != nil {
		t.Fatalf("读当前模型失败: %v", err)
	} else if cur.ReasoningEffort != "" {
		t.Fatalf("认不出的档位不该改内存: %+v", cur)
	}
	if _, err := os.Stat(path); !os.IsNotExist(err) {
		t.Fatalf("认不出的档位不该写配置: %v", err)
	}

	// 一套模型都没配时只报一句配置问题，不 panic
	empty := newModel(config.GlobalConfig{}, nil, ".", domain.NewSession("."))
	empty.cfgPath = filepath.Join(t.TempDir(), "config.yaml")
	out, _ := empty.adjustEffort("/effort high")
	if len(out.(model).cfg.AvailableModels()) != 0 {
		t.Fatal("没配模型时不该凭空造出一套")
	}
}

// 从输入框走一遍真路径：/effort medium 确实接到了命令上
func TestEffortViaSubmit(t *testing.T) {
	path := filepath.Join(t.TempDir(), "config.yaml")
	m := newModel(config.GlobalConfig{Models: testModels(), Current: "快"}, nil, ".", domain.NewSession("."))
	m.cfgPath = path

	m.input.SetValue("/effort medium")
	got, _ := m.submit()
	after := got.(model)
	if after.input.Value() != "" {
		t.Fatalf("命令发出去之后输入框该清空: %q", after.input.Value())
	}
	cur, err := after.cfg.CurrentModel()
	if err != nil {
		t.Fatalf("读当前模型失败: %v", err)
	}
	if cur.ReasoningEffort != "medium" {
		t.Fatalf("/effort medium 没生效: %+v", cur)
	}
}
