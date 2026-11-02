package tool

import (
	"os"
	"path/filepath"
	"runtime"
	"strings"
	"testing"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
)

// localBytes 返回 s 的本地编码（GB18030）字节。
// 取样时要注意：有些词的 GBK 字节恰好也是合法 UTF-8（例如"目录"是 C4BF C2BC），
// 那种情况靠编码探测无法区分，测试里直接拒掉，避免样例本身没意义。
func localBytes(t *testing.T, s string) []byte {
	t.Helper()
	b, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(s))
	if err != nil {
		t.Fatalf("%q 无法编码成 GB18030: %v", s, err)
	}
	if utf8.Valid(b) {
		t.Fatalf("%q 的本地编码字节恰好是合法 UTF-8，不能当样例: %v", s, b)
	}
	return b
}

// gbkOnlyChars 是 GBK 装不下、GB18030 才能表示的四字节字符
func gbkOnlyChars(t *testing.T, s string) bool {
	t.Helper()
	if _, err := simplifiedchinese.GBK.NewEncoder().Bytes([]byte(s)); err == nil {
		return false
	}
	return true
}

func TestDecodeOutputKeepsUTF8(t *testing.T) {
	in := "中文输出\nhello\n"
	if got := decodeOutput([]byte(in)); got != in {
		t.Fatalf("合法 UTF-8 不该被改动: %q", got)
	}
}

func TestDecodeOutputLocal(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("本地编码兜底只在 windows 上生效")
	}
	for _, s := range []string{"中文", "配置项", "丢失"} {
		if got := decodeOutput(localBytes(t, s)); got != s {
			t.Fatalf("本地编码内容没解出来: %q != %q", got, s)
		}
	}
}

// UTF-8 和本地编码混在同一个输出里时，只转换坏掉的行
func TestDecodeOutputMixedLines(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("本地编码兜底只在 windows 上生效")
	}
	raw := append([]byte("中文\n"), localBytes(t, "配置项")...)
	raw = append(raw, '\n')
	if got, want := decodeOutput(raw), "中文\n配置项\n"; got != want {
		t.Fatalf("混合编码输出不对: %q != %q", got, want)
	}
}

// 截断卡在多字节字符中间时，去掉半个字符就行，不该整段当本地编码转码
func TestDecodeOutputTruncatedUTF8(t *testing.T) {
	in := []byte("中文")[:4] // "中" + "文" 的前 1 个字节
	if got := decodeOutput(in); got != "中" {
		t.Fatalf("半截字符应被丢掉: %q", got)
	}
}

// 本地编码的行不能被当成"UTF-8 截断"整行裁掉
func TestDecodeOutputKeepsLocalLineWhole(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("本地编码兜底只在 windows 上生效")
	}
	raw := append([]byte("    "), localBytes(t, "配置项")...)
	raw = append(raw, []byte(" = 1\r")...)
	if got, want := decodeOutput(raw), "    配置项 = 1\r"; got != want {
		t.Fatalf("本地编码行被裁掉了: %q != %q", got, want)
	}
}

func TestOutputBufferDecodesLocal(t *testing.T) {
	if runtime.GOOS != "windows" {
		t.Skip("本地编码兜底只在 windows 上生效")
	}
	buf := newOutputBuffer(64)
	if _, err := buf.Write(localBytes(t, "配置项")); err != nil {
		t.Fatalf("写入失败: %v", err)
	}
	if out := buf.String(); out != "配置项" {
		t.Fatalf("输出没转成 UTF-8: %q", out)
	}
}

// 真实跑一次带中文输出的命令，确保整条链路（shell → 缓冲区 → 工具结果）不乱码
func TestBashChineseOutput(t *testing.T) {
	out, err := runBash(t, map[string]any{"command": "echo 中文输出"})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !utf8.ValidString(out) {
		t.Fatalf("工具输出必须是合法 UTF-8: %q", out)
	}
	if !strings.Contains(out, "中文输出") {
		t.Fatalf("中文输出乱码了: %q", out)
	}
}

func TestDecodeLocalLossless(t *testing.T) {
	if got, ok := decodeLocalLossless(localBytes(t, "配置项")); !ok || got != "配置项" {
		t.Fatalf("本地编码文本应能无损解出: %q %v", got, ok)
	}
	// \xff 不是合法的 GB18030 首字节，解不动
	if _, ok := decodeLocalLossless([]byte{0xff, 0xfe}); ok {
		t.Fatal("非法字节不该被当成本地编码文本")
	}
	// 本来就是 UTF-8 的内容，要么解不动，要么往返回来还是原样，不能转坏
	if got, ok := decodeLocalLossless([]byte("纯 UTF-8 中文")); ok && got != "纯 UTF-8 中文" {
		t.Fatalf("UTF-8 内容被转坏了: %q", got)
	}
}

// GB18030 的四字节字符（GBK 装不下）也要能无损往返
func TestLocalEncodingRoundTripFourByte(t *testing.T) {
	for _, s := range []string{"😀", "𠀀"} {
		if !gbkOnlyChars(t, s) {
			t.Fatalf("%q 应该不是 GBK 能表示的字符", s)
		}
		enc, ok := encodeLocalLossless(s)
		if !ok {
			t.Fatalf("%q 应该能编码成 GB18030", s)
		}
		if len(enc) != 4 {
			t.Fatalf("%q 的 GB18030 编码应该是 4 字节，实际 %x", s, enc)
		}
		if got, ok := decodeLocalLossless(enc); !ok || got != s {
			t.Fatalf("%q 往返失败: %q %v", s, got, ok)
		}
	}
}

// Read 遇到 GBK/GB18030 文件要正常显示，不能报"不是文本文件"
func TestReadLocalEncodedFile(t *testing.T) {
	content := append([]byte("package main\n"), localBytes(t, "配置项 = 1\n")...)
	path := filepath.Join(t.TempDir(), "local.go")
	if err := os.WriteFile(path, content, 0o644); err != nil {
		t.Fatalf("准备文件失败: %v", err)
	}

	out, err := runRead(t, map[string]any{"path": path})
	if err != nil {
		t.Fatalf("读本地编码文件不该报错: %v", err)
	}
	if !strings.Contains(out, "配置项 = 1") {
		t.Fatalf("本地编码内容没解出来: %s", out)
	}
	if !strings.Contains(out, "package main") {
		t.Fatalf("UTF-8 行被破坏了: %s", out)
	}
	if !strings.Contains(out, "GB18030") {
		t.Fatalf("应提示文件不是 UTF-8: %s", out)
	}
}

// GBK 装不下的四字节字符在 Read 里也要能正确显示
func TestReadGB18030FourByteChar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "emoji.txt")
	if err := os.WriteFile(path, localBytes(t, "表情 😀 结束\n"), 0o644); err != nil {
		t.Fatalf("准备文件失败: %v", err)
	}
	out, err := runRead(t, map[string]any{"path": path})
	if err != nil {
		t.Fatalf("读 GB18030 文件不该报错: %v", err)
	}
	if !strings.Contains(out, "表情 😀 结束") {
		t.Fatalf("四字节字符没解出来: %s", out)
	}
}

// 真正的二进制仍然要报错，别为了兼容本地编码把非文本当文本
func TestReadStillRejectsBinary(t *testing.T) {
	path := writeTempFile(t, "ok\n\xff\xfe\n")
	if _, err := runRead(t, map[string]any{"path": path}); err == nil {
		t.Fatal("非法字节的文件应该报错")
	}
}

// Grep 的 pattern 是 UTF-8，本地编码文件要先解码再匹配
func TestGrepLocalEncodedFile(t *testing.T) {
	root := t.TempDir()
	path := filepath.Join(root, "local.txt")
	if err := os.WriteFile(path, append(localBytes(t, "配置项"), []byte(" = 1\n")...), 0o644); err != nil {
		t.Fatalf("准备文件失败: %v", err)
	}

	out, err := runGrep(t, map[string]any{"pattern": "配置项", "path": root})
	if err != nil {
		t.Fatalf("执行失败: %v", err)
	}
	if !strings.Contains(out, "local.txt:1:配置项 = 1") {
		t.Fatalf("本地编码文件没搜到: %s", out)
	}
	if !utf8.ValidString(out) {
		t.Fatalf("结果必须是合法 UTF-8: %q", out)
	}
}

// Edit 改本地编码文件时要保持原编码写回，不能悄悄转成 UTF-8
func TestEditLocalEncodedFileKeepsEncoding(t *testing.T) {
	path := filepath.Join(t.TempDir(), "local.txt")
	if err := os.WriteFile(path, append(localBytes(t, "配置项 = 1"), []byte("\n")...), 0o644); err != nil {
		t.Fatalf("准备文件失败: %v", err)
	}

	out, err := runEdit(t, map[string]any{"path": path, "old_string": "配置项 = 1", "new_string": "配置项 = 2"})
	if err != nil {
		t.Fatalf("改本地编码文件不该报错: %v", err)
	}
	if !strings.Contains(out, "1 处") {
		t.Fatalf("替换结果不对: %s", out)
	}

	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回文件失败: %v", err)
	}
	if utf8.Valid(raw) {
		t.Fatalf("文件被转成 UTF-8 了: %v", raw)
	}
	if got, ok := decodeLocalLossless(raw); !ok || strings.TrimRight(got, "\n") != "配置项 = 2" {
		t.Fatalf("写回的内容不对: %q %v", got, ok)
	}
}

// 新增的内容 GB18030 能表示时要正常写入（四字节字符也算，GBK 是不够的）
func TestEditLocalEncodedWritesFourByteChar(t *testing.T) {
	path := filepath.Join(t.TempDir(), "local.txt")
	if err := os.WriteFile(path, append(localBytes(t, "配置项 = 1"), []byte("\n")...), 0o644); err != nil {
		t.Fatalf("准备文件失败: %v", err)
	}

	if _, err := runEdit(t, map[string]any{"path": path, "old_string": "配置项 = 1", "new_string": "配置项 = 😀"}); err != nil {
		t.Fatalf("GB18030 能表示的字符应该写入成功: %v", err)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		t.Fatalf("读回文件失败: %v", err)
	}
	if utf8.Valid(raw) {
		t.Fatalf("文件被转成 UTF-8 了: %v", raw)
	}
	if got, ok := decodeLocalLossless(raw); !ok || strings.TrimRight(got, "\n") != "配置项 = 😀" {
		t.Fatalf("写回的内容不对: %q %v", got, ok)
	}
}
