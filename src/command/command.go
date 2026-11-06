// Package command 是本地命令的那张表：命令叫什么、一句话说明是什么。
//
// 终端界面（src/cli）和浏览器界面（src/webui）都从这儿取：两边的补全候选、
// 帮助正文、"是不是打错了一条命令"的判断必须是同一份，不然同一个名字在两个界面上
// 说法不一样，用户得记两套。
//
// 这个包只管"有哪些命令、名字长什么样"，不管命令怎么执行——执行是各个界面自己的事
// （终端里改成打历史区，浏览器里回一段 JSON 给前端）。
package command

import (
	"strings"
	"unicode"
)

// Spec 是一条本地命令：名字（带前导斜杠）和一句话说明。
// 说明要写得中性——它同时出现在终端和浏览器的候选列表里，别写某个界面专有的按键
type Spec struct {
	Name string `json:"name"`
	Desc string `json:"desc"`
}

// Specs 是全部内置命令，顺序就是补全候选里排的顺序（终端那边还按这个顺序生成帮助）。
// 加一条命令只改这里；两边的界面都会跟着变
var Specs = []Spec{
	{"/help", "看这个帮助"},
	{"/new", "开一个新会话"},
	{"/resume", "恢复以前的会话：不带参数列出存过的会话，也可以直接跟序号或 id"},
	{"/session", "看当前会话 id 和消息条数"},
	{"/config", "配置当前这套模型：接口地址（留空用内置的）、密钥、模型名、思考强度"},
	{"/mode", "看或切换工作模式：只读 / 编辑需授权 / 完全授权"},
	{"/model", "看配置好的几套模型，或切到其中一套；/model add 新增一套，/model del 名字删掉一套"},
	{"/effort", "看或调整思考强度（档位按类型不同，DeepSeek 有 none/xhigh/ultra/max；会写回配置文件）"},
	{"/skills", "看装了哪些技能，直接 /技能名 就能调用"},
	{"/todo", "看这次任务的待办清单"},
	{"/quiet", "切换精简显示：只打正文，不显示思考过程和工具输出（会写回配置）"},
	{"/exit", "退出（终端界面用；浏览器界面直接关标签页）"},
}

// Spec 按名字找一条命令，名字要带前导斜杠。找不到时 ok 为 false
func Find(name string) (Spec, bool) {
	for _, spec := range Specs {
		if spec.Name == name {
			return spec, true
		}
	}
	return Spec{}, false
}

// IsKnown 说明这个名字是不是一条内置命令
func IsKnown(name string) bool {
	_, ok := Find(name)
	return ok
}

// Names 是所有命令名，测试和界面按需取
func Names() []string {
	out := make([]string, 0, len(Specs))
	for _, spec := range Specs {
		out = append(out, spec.Name)
	}
	return out
}

// Suggest 返回能把当前输入补全的命令：只在第一段（还没打空格）时补全，
// 输入已经是某个完整命令时不再提示。技能名的补全由调用方接在后面——
// 技能是用户自己装的，两个界面各有各的技能清单
func Suggest(input string) []Spec {
	s := Prefix(input)
	if s == "" {
		return nil
	}
	var out []Spec
	for _, spec := range Specs {
		if strings.HasPrefix(spec.Name, s) {
			out = append(out, spec)
		}
	}
	if len(out) == 1 && out[0].Name == s {
		return nil
	}
	return out
}

// Prefix 是"该拿什么去比前缀"：输入以斜杠开头、而且还没打空格时返回那截名字，
// 否则返回空串（说明这会儿不该补全）
func Prefix(input string) string {
	s := strings.TrimLeft(input, " \t")
	if !strings.HasPrefix(s, "/") || strings.ContainsAny(s, " \t\n") {
		return ""
	}
	return s
}

// LooksLike 判断输入的第一段是不是"想打一条命令"：/ 开头，后面全是名字里
// 该有的字符（字母、数字、连字符、下划线）。认出来却没有对应的命令或技能，
// 说一句比闷头发给模型强；/tmp/foo.log 这种带路径分隔符的照旧当普通提问。
// 中日韩文字也算名字里该有的——技能名可以是中文
func LooksLike(field string) bool {
	if !strings.HasPrefix(field, "/") || len(field) == 1 {
		return false
	}
	for _, r := range field[1:] {
		switch {
		case r == '-' || r == '_' || unicode.IsLetter(r) || unicode.IsDigit(r):
		default:
			return false
		}
	}
	return true
}

// FirstField 是输入的第一段，空输入时返回空串
func FirstField(input string) string {
	if fields := strings.Fields(input); len(fields) > 0 {
		return fields[0]
	}
	return ""
}

// Args 是命令后面那截参数，没参数时返回空串
func Args(input string) string {
	fields := strings.Fields(input)
	if len(fields) < 2 {
		return ""
	}
	return strings.Join(fields[1:], " ")
}

// Near 是名字里含这段字的命令和技能，用来在"没有这条命令"时给个提示。
// 列一屏沾边的等于没提示，所以最多 maxNear 个
func Near(name string, skillNames []string) []string {
	lower := strings.ToLower(strings.TrimPrefix(name, "/"))
	var near []string
	for _, spec := range Specs {
		candidate := strings.TrimPrefix(spec.Name, "/")
		if strings.Contains(strings.ToLower(candidate), lower) {
			near = append(near, spec.Name)
		}
	}
	for _, skill := range skillNames {
		if strings.Contains(strings.ToLower(skill), lower) {
			near = append(near, "/"+skill)
		}
	}
	if len(near) > maxNear {
		near = near[:maxNear]
	}
	return near
}

// maxNear 是"相近的命令"最多列几个
const maxNear = 8
