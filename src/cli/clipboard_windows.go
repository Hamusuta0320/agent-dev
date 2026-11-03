//go:build windows

package main

import (
	"errors"
	"fmt"
	"os"
	"os/exec"
	"strings"
)

// clipboardScript 用 PowerShell 把剪贴板里的图片存成临时 PNG。
// 剪贴板给的原始格式是 DIB，交给 System.Drawing 直接 Save 成 PNG 最省事；
// 退出码 2 是"剪贴板里没有图片"，和真正的出错区分开
const clipboardScript = `
Add-Type -AssemblyName System.Windows.Forms
Add-Type -AssemblyName System.Drawing
$img = [System.Windows.Forms.Clipboard]::GetImage()
if ($null -eq $img) { exit 2 }
$img.Save('%s', [System.Drawing.Imaging.ImageFormat]::Png)
`

// clipboardImage 从 Windows 剪贴板里捞一张图片，返回 PNG 字节。
// 剪贴板里的图没有"文件名"这回事，先落到临时文件再读回来。
func clipboardImage() ([]byte, error) {
	f, err := os.CreateTemp("", "agent-dev-clip-*.png")
	if err != nil {
		return nil, err
	}
	path := f.Name()
	f.Close()
	defer os.Remove(path)

	// script 里的路径用单引号包着，路径自带的单引号要写两遍才不会被当成结束符
	script := fmt.Sprintf(clipboardScript, strings.ReplaceAll(path, "'", "''"))
	cmd := exec.Command(powerShell(), "-NoProfile", "-NonInteractive", "-STA", "-Command", script)
	var stderr strings.Builder
	cmd.Stderr = &stderr
	if err := cmd.Run(); err != nil {
		var exitErr *exec.ExitError
		if errors.As(err, &exitErr) && exitErr.ExitCode() == 2 {
			return nil, errNoClipboardImage
		}
		return nil, fmt.Errorf("读剪贴板失败: %v %s", err, strings.TrimSpace(stderr.String()))
	}
	raw, err := os.ReadFile(path)
	if err != nil || len(raw) == 0 {
		return nil, errNoClipboardImage
	}
	return raw, nil
}

// powerShell 找一个能用的 PowerShell：老 Windows 只有 powershell，
// 装了 PowerShell 7 的机器上还有 pwsh
func powerShell() string {
	for _, name := range []string{"powershell", "pwsh"} {
		if _, err := exec.LookPath(name); err == nil {
			return name
		}
	}
	return "powershell"
}
