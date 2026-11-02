//go:build windows

package main

import "syscall"

// utf8CodePage 是 UTF-8 的代码页
const utf8CodePage = 65001

// useUTF8Console 把控制台的输入/输出代码页切到 UTF-8。
// 中文 Windows 默认是 936（GBK），而程序写出去的都是 UTF-8 字节，
// 不切的话界面里的中文会一坨乱码（"涓婁笅鏂囧" 那种）。
// 拿不到控制台（比如输出被重定向、在无控制台的进程里跑）时调用会失败，忽略即可。
func useUTF8Console() {
	kernel32 := syscall.NewLazyDLL("kernel32.dll")
	_, _, _ = kernel32.NewProc("SetConsoleOutputCP").Call(utf8CodePage)
	_, _, _ = kernel32.NewProc("SetConsoleCP").Call(utf8CodePage)
}
