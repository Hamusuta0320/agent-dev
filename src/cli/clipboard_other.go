//go:build !windows

package main

import (
	"fmt"
	"os"
	"os/exec"
	"runtime"
	"strings"
)

// clipboardImage 从剪贴板里捞一张图片，返回 PNG 字节。
// macOS 先用 pngpaste（装了的话），没有就退回系统自带的 osascript；
// Linux 先试 Wayland 的 wl-paste，再试 X11 的 xclip。
// 都没有就老实说读不到，让用户改用"把图片文件拖进来"那条路。
func clipboardImage() ([]byte, error) {
	if runtime.GOOS == "darwin" {
		return macClipboardImage()
	}
	if raw, err := runCapture("wl-paste", "-n", "-t", "image/png"); err == nil && len(raw) > 0 {
		return raw, nil
	}
	if raw, err := runCapture("xclip", "-selection", "clipboard", "-t", "image/png", "-o"); err == nil && len(raw) > 0 {
		return raw, nil
	}
	return nil, errNoClipboardImage
}

// macClipboardImage 把剪贴板里那块 PNG 写进临时文件再读回来。
// 走 AppleScript 是因为它随系统就有，不用让人先装点什么
func macClipboardImage() ([]byte, error) {
	if raw, err := runCapture("pngpaste", "-"); err == nil && len(raw) > 0 {
		return raw, nil
	}
	f, err := os.CreateTemp("", "agent-dev-clip-*.png")
	if err != nil {
		return nil, err
	}
	path := f.Name()
	f.Close()
	defer os.Remove(path)

	// 剪贴板里没有图片时 AppleScript 会在这儿抛错，走下面的统一处理
	script := fmt.Sprintf(`try
	set theData to (the clipboard as «class PNGf»)
on error
	error "剪贴板里没有图片"
end try
set fd to open for access (POSIX file "%s") with write permission
set eof fd to 0
write theData to fd
close access fd`, path)
	if _, err := runCapture("osascript", "-e", script); err != nil {
		return nil, errNoClipboardImage
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
		return nil, errNoClipboardImage
	}
	return raw, nil
}

// runCapture 跑一个命令，把它写到标准输出的东西收回来；命令不存在时直接报错
func runCapture(name string, args ...string) ([]byte, error) {
	if _, err := exec.LookPath(name); err != nil {
		return nil, err
	}
	out, err := exec.Command(name, args...).Output()
	if err != nil {
		return nil, fmt.Errorf("%s 没读到东西: %w", strings.TrimSpace(name), err)
	}
	return out, nil
}
