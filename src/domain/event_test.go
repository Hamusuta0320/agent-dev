package domain

import (
	"testing"
	"time"
)

// 状态文案要能直接给人看
func TestToolStatusLabel(t *testing.T) {
	cases := map[ToolStatus]string{
		ToolStatusRunning: "调用中",
		ToolStatusSuccess: "调用成功",
		ToolStatusFailed:  "调用失败",
	}
	for status, want := range cases {
		if got := status.Label(); got != want {
			t.Fatalf("状态 %d 的文案是 %q，应该是 %q", status, got, want)
		}
	}
}

// 耗时按量级换单位：毫秒以内、毫秒、秒
func TestElapsedLabel(t *testing.T) {
	cases := map[time.Duration]string{
		0:                       "<1ms",
		300 * time.Microsecond:  "<1ms",
		3 * time.Millisecond:    "3ms",
		999 * time.Millisecond:  "999ms",
		time.Second:             "1.00s",
		1234 * time.Millisecond: "1.23s",
	}
	for elapsed, want := range cases {
		if got := (Event{Elapsed: elapsed}).ElapsedLabel(); got != want {
			t.Fatalf("%v 显示成了 %q，应该是 %q", elapsed, got, want)
		}
	}
}
