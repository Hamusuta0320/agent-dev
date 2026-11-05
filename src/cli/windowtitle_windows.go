//go:build windows

package main

import (
	"syscall"
	"unsafe"
)

// setWindowTitle 把控制台窗口的标题换成当前会话的名字（见 windowTitle）。
// 拿不到控制台时（输出被重定向、在无控制台的进程里跑）setWindowTitleW 会失败，忽略即可
func setWindowTitle(sessionTitle string) {
	title, err := syscall.UTF16PtrFromString(windowTitle(sessionTitle))
	if err != nil {
		return
	}
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	_, _, _ = kernel32.NewProc("SetConsoleTitleW").Call(uintptr(unsafe.Pointer(title)))
}
