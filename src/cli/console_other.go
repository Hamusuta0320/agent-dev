//go:build !windows

package main

// useUTF8Console 在非 Windows 平台上不用做什么：终端本来就按 UTF-8 解码
func useUTF8Console() {}
