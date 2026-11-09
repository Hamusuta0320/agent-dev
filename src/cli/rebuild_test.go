package main

import (
	"encoding/json"
	"fmt"
	"strings"
	"testing"
	"time"

	"hamusuta/agent-dev/src/config"
	"hamusuta/agent-dev/src/domain"
	"hamusuta/agent-dev/src/tool"

	tea "charm.land/bubbletea/v2"
)

// setTodos 把一份清单装进界面读的那份待办里，走工具那条正经路径（清单只由
// TodoWrite 维护，测试也别绕过它自己塞）
func setTodos(t *testing.T, items []tool.TodoItem) {
	t.Helper()
	args, err := json.Marshal(tool.TodoArgs{Intent: "测试用", Todos: items})
	if err != nil {
		t.Fatalf("准备清单失败: %v", err)
	}
	if _, err := (tool.TodoTool{}).Execute(t.Context(), args); err != nil {
		t.Fatalf("准备清单失败: %v", err)
	}
}

// 屏幕上叠出好几份界面时（终端矮、历史又长的时候会发生），按 Ctrl+L 应该能
// 把旧内容整体顶出去，只剩干净的一帧。
func TestRebuildClearsResidue(t *testing.T) {
	for _, size := range []struct{ w, h int }{{50, 18}, {60, 24}, {80, 30}} {
		t.Run(itoa(size.w)+"x"+itoa(size.h), func(t *testing.T) {
			hs := newHarness(t, size.w, size.h)

			// 攒够历史：几轮回答 + 工具结果，把屏幕填满
			hs.send(eventMsg(domain.Event{Kind: domain.EventUsage, Usage: probeUsage()}))
			for i := range 8 {
				hs.send(eventMsg(domain.Event{Kind: domain.EventContent, Text: "回答片段"}))
				hs.send(eventMsg(domain.Event{
					Kind:   domain.EventToolResult,
					Text:   "结果行 " + itoa(i),
					Call:   domain.ToolCall{Id: itoa(i), Name: "Bash"},
					Status: domain.ToolStatusSuccess,
				}))
			}

			// 重建
			hs.p.Send(tea.KeyPressMsg{Code: 'l', Mod: tea.ModCtrl})
			time.Sleep(500 * time.Millisecond)

			lines := hs.snapshot("按 Ctrl+L 之后")
			joined := strings.Join(lines, "\n")

			// 关键元素各一次，说明界面干净了
			for _, want := range []string{"上下文 ", "Ctrl+C"} {
				if n := strings.Count(joined, want); n != 1 {
					t.Errorf("重建后 %q 出现 %d 次，应该只有 1 次:\n%s", want, n, joined)
				}
			}
			// 帧得在屏幕上：输入框那圈圆角框的顶边
			if !strings.Contains(joined, "╭") {
				t.Errorf("重建后输入框没了:\n%s", joined)
			}
		})
	}
}

// 帧高必须随终端收缩：终端矮的时候，帧要是占满整屏，每次打印往上插行都会算错
// 滚动量，屏幕上就叠出好几份界面（状态行、输入框、上下文占用各出现好几遍）。
func TestFrameFitsTerminal(t *testing.T) {
	for _, h := range []int{12, 14, 16, 18, 20, 24, 30, 40} {
		m := newModel(safeConfig(), nil, ".", domain.NewSession("."))
		m.width, m.height = 60, h

		frame := compose(m.frameLines(), m.width)
		rows := len(strings.Split(frame, "\n"))

		// 帧绝不能比终端还高，否则 print 往上插行的前提彻底不成立
		if rows > h-1 {
			t.Errorf("高 %d：帧占了 %d 行，比终端还高", h, rows)
		}
		// 终端不太矮的时候，帧必须给历史区留出足够空间
		if h >= 18 && rows > h-reserveHistoryRows {
			t.Errorf("高 %d：帧占了 %d 行，只给历史留了 %d 行（至少要 %d 行）",
				h, rows, h-rows, reserveHistoryRows)
		}
	}
}

// reserveHistoryRows 在多矮的终端上都不该被突破
func TestActiveRowsShrinksWithTerminal(t *testing.T) {
	m := newModel(safeConfig(), nil, ".", domain.NewSession("."))
	m.width = 60

	tall := m
	tall.height = 40
	if got := tall.activeRows(); got != maxActiveRows {
		t.Errorf("终端够高时应该用满 %d 行，实际 %d 行", maxActiveRows, got)
	}

	short := m
	short.height = 16
	if got := short.activeRows(); got >= maxActiveRows {
		t.Errorf("终端矮的时候这块要收窄，实际还是 %d 行", got)
	}

	tiny := m
	tiny.height = 10
	if got := tiny.activeRows(); got != minActiveRows {
		t.Errorf("终端特别矮时也要留 %d 行，实际 %d 行", minActiveRows, got)
	}
}

// 一次 print 最多能带几行，必须真的按帧高算：帧高要是数成 frameLines 的元素个数，
// 就会少算好几行（输入框上方那块是一个整块多行字符串），一次想打的行数超过终端
// 剩下的行数，插行落点就错了，屏幕上叠出旧帧（两份输入框、两行上下文占用）。
//
// 帧高还要取"屏幕上那一帧"和"即将画上去的新帧"里高的那个：渲染器插行时是按屏幕上
// 那一帧的位置往上数的，清单刚收起时屏幕上那帧还高着，只按新帧算就会多打一大截。
func TestPrintRowsLeavesRoomAboveFrame(t *testing.T) {
	// 屏幕上那一帧比即将画的新帧高时按高的算：比如确认框顶掉输入框之后又回主界面，
	// 新帧矮下去，可屏幕上那一帧还在原位，插行的落点是按它数的
	const h = 30
	m := newModel(safeConfig(), nil, ".", domain.NewSession("."))
	m.width, m.height = 100, h
	m.screenRows = m.frameRows() + 3 // 屏幕上那帧比新帧高 3 行
	if got, want := m.printRows(), h-m.screenRows-1; got != want {
		t.Errorf("屏幕上那一帧更高时，一次该只带 %d 行，实际 %d 行", want, got)
	}
	// 两帧一样高时就是"终端高 - 帧高 - 1"
	m.screenRows = m.frameRows()
	if got, want := m.printRows(), h-m.frameRows()-1; got != want {
		t.Errorf("两帧一样高时该带 %d 行，实际 %d 行", want, got)
	}

	for _, height := range []int{12, 14, 16, 18, 20, 24, 30, 40} {
		m := newModel(safeConfig(), nil, ".", domain.NewSession("."))
		m.width, m.height = 80, height

		rows := m.frameRows()
		// 帧高 = 固定那几行 + 输入框上方那块的实际行数，多一行少一行都不对
		// （那块现在按内容占行，没内容就 0 行）
		if want := chromeRows + m.activeRows(); rows > want {
			t.Errorf("高 %d：帧高算出来 %d 行，比 chrome(%d)+槽位上限(%d) 还高",
				height, rows, chromeRows, m.activeRows())
		}
		// 帧高 + 一次能带的行数 + 一行余量，不能超过终端高，否则插行会顶出屏幕。
		// 终端矮到帧本身就快占满时（槽位已经收到 1 行）没法再让，只要求至少能打一行
		room := m.printRows()
		if room < 1 {
			t.Errorf("高 %d：一次打印连一行都带不了", height)
		}
		if rows+room+1 > height && room > 1 {
			t.Errorf("高 %d：帧 %d 行、一次想打 %d 行，加起来超出终端", height, rows, room)
		}
	}
}

// 清单会占几行，但不能把帧撑到顶出屏幕：帧高有上限，超了就是渲染错位的源头。
//
// 帧高本身允许变（输入框上方那块按内容占行），所以这里不比对"有清单前后等高"，
// 只守两条：清单真的排出来了，且整帧仍然给历史区留下 reserveHistoryRows 行。
func TestFrameHeightCappedWithTodos(t *testing.T) {
	t.Cleanup(tool.ClearTodos)
	for _, h := range []int{14, 16, 18, 20, 24, 30} {
		for _, state := range []uiState{stateIdle, stateRunning} {
			tool.ClearTodos()
			m := newModel(safeConfig(), nil, ".", domain.NewSession("."))
			m.width, m.height, m.state = 80, h, state

			items := make([]tool.TodoItem, 15)
			for i := range items {
				items[i] = tool.TodoItem{Content: fmt.Sprintf("第 %d 件事", i+1), Status: tool.TodoPending}
			}
			items[5].Status = tool.TodoInProgress
			setTodos(t, items)

			rows := m.frameRows()
			// 清单列出来了，但没把别的内容挤没。终端太矮时只列得下标题，
			// 那时至少要看到进度总量（"待办 x/y"）
			view := stripANSI(m.activeView())
			if m.activeTodoRows() > 1 {
				if !strings.Contains(view, "第 6 件事") {
					t.Errorf("高 %d 状态 %d：清单没显示出来:\n%s", h, state, view)
				}
			} else if !strings.Contains(view, "待办 ") {
				t.Errorf("高 %d 状态 %d：清单连标题都没有:\n%s", h, state, view)
			}
			// 整帧仍然塞得进终端，且给历史区留出了行
			if rows > h {
				t.Errorf("高 %d：帧占了 %d 行，比终端还高", h, rows)
			}
			if h >= 18 && rows > h-reserveHistoryRows {
				t.Errorf("高 %d：帧占了 %d 行，只给历史留了 %d 行（至少要 %d 行）",
					h, rows, h-rows, reserveHistoryRows)
			}
		}
	}
}

func safeConfig() config.GlobalConfig { return config.GlobalConfig{Mode: "full"} }

func itoa(n int) string {
	if n == 0 {
		return "0"
	}
	var b []byte
	for n > 0 {
		b = append([]byte{byte('0' + n%10)}, b...)
		n /= 10
	}
	return string(b)
}
