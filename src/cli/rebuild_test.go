package main

import (
	"strings"
	"testing"
	"time"

	"hamusuta/agent-dev/src/config"
	"hamusuta/agent-dev/src/domain"

	tea "charm.land/bubbletea/v2"
)

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
			// 帧得在屏幕上
			if !strings.Contains(joined, "问点什么") {
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
func TestPrintRowsLeavesRoomAboveFrame(t *testing.T) {
	for _, h := range []int{12, 14, 16, 18, 20, 24, 30, 40} {
		m := newModel(safeConfig(), nil, ".", domain.NewSession("."))
		m.width, m.height = 80, h

		rows := m.frameRows()
		// 帧高 = 固定那几行 + 输入框上方槽位，多一行少一行都不对
		if want := chromeRows + m.activeRows(); rows != want {
			t.Errorf("高 %d：帧高算出来 %d 行，按 chrome(%d)+槽位(%d) 应该是 %d 行",
				h, rows, chromeRows, m.activeRows(), want)
		}
		// 帧高 + 一次能带的行数 + 一行余量，不能超过终端高，否则插行会顶出屏幕。
		// 终端矮到帧本身就快占满时（槽位已经收到 1 行）没法再让，只要求至少能打一行
		room := m.printRows()
		if room < 1 {
			t.Errorf("高 %d：一次打印连一行都带不了", h)
		}
		if rows+room+1 > h && room > 1 {
			t.Errorf("高 %d：帧 %d 行、一次想打 %d 行，加起来超出终端", h, rows, room)
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
