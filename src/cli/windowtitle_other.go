//go:build !windows

package main

import (
	"fmt"
	"os"
)

// setWindowTitle 用 OSC 0 序列把窗口/标签页的标题换成当前会话的名字（见 windowTitle）。
// 终端不认这个序列时会直接忽略，屏幕上不会多出东西来
func setWindowTitle(sessionTitle string) {
	fmt.Fprintf(os.Stdout, "\x1b]0;%s\x07", windowTitle(sessionTitle))
}
