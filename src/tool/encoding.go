package tool

import (
	"bytes"
	"runtime"
	"strings"
	"unicode/utf8"

	"golang.org/x/text/encoding/simplifiedchinese"
	"golang.org/x/text/transform"
)

// localEncoding 说明：本地编码统一用 GB18030，而不是 GBK。
// GBK 是 GB18030 的子集，对 GBK 内容编解码结果完全一致，
// 但 GB18030 还能表示 GBK 装不下的四字节字符（生僻字等），兼容面更广。

// decodeOutput 把子进程吐出来的字节转成合法 UTF-8 字符串。
// Windows 上不少原生命令（ping、ipconfig、cmd 内建命令等）按本地代码页输出，
// 中文系统就是 GBK/GB18030，直接当 UTF-8 用会变成乱码。
// 逐行判断，UTF-8 和本地编码混在一起时也能只转换坏掉的那几行。
func decodeOutput(b []byte) string {
	if len(b) == 0 {
		return ""
	}
	if utf8.Valid(b) {
		return string(b)
	}
	var sb strings.Builder
	sb.Grow(len(b) * 3 / 2)
	for len(b) > 0 {
		var line []byte
		if i := bytes.IndexByte(b, '\n'); i >= 0 {
			line, b = b[:i+1], b[i+1:]
		} else {
			line, b = b, nil
		}
		if utf8.Valid(line) {
			sb.Write(line)
			continue
		}
		// 尾部被拦腰切断的多字节字符，裁掉就行，别当成本地代码页解码
		if t, ok := trimTruncatedRuneTail(line); ok {
			sb.Write(t)
			continue
		}
		sb.WriteString(decodeLocal(line))
	}
	return sb.String()
}

// trimTruncatedRuneTail 处理"输出正好被从字符中间截断"的情况：
// 从头逐字符解码，只有末尾那个多字节字符被拦腰切断时才返回裁剪结果。
// 解不动的位置离结尾还远（或者首字节根本不是 UTF-8 引导字节），说明这行本来就是
// 本地代码页（GBK），不能当成 UTF-8 截断处理，否则整行中文会被裁没。
func trimTruncatedRuneTail(b []byte) ([]byte, bool) {
	for i := 0; i < len(b); {
		r, size := utf8.DecodeRune(b[i:])
		if r != utf8.RuneError || size > 1 {
			i += size
			continue
		}
		rest := b[i:]
		if len(rest) <= 3 && rest[0] >= 0xC2 && rest[0] <= 0xF4 && !utf8.FullRune(rest) {
			return b[:i], true
		}
		return nil, false
	}
	return nil, false
}

// decodeLocal 按 Windows 本地代码页（中文系统是 GBK/GB18030）解码，
// 无法解码的字节替换成 U+FFFD，保证结果一定是合法 UTF-8
func decodeLocal(b []byte) string {
	if runtime.GOOS == "windows" {
		return transformBytes(simplifiedchinese.GB18030.NewDecoder(), b)
	}
	return strings.ToValidUTF8(string(b), "\uFFFD")
}

// transformBytes 做一次编码转换，失败时把已转好的部分和剩下的原始字节拼起来，
// 避免整行内容因为个别坏字节全丢
func transformBytes(t transform.Transformer, b []byte) string {
	out, n, err := transform.Bytes(t, b)
	if err == nil {
		return string(out)
	}
	if n > len(b) {
		n = len(b)
	}
	return string(out) + strings.ToValidUTF8(string(b[n:]), "\uFFFD")
}

// decodeLocalLossless 把整段字节按本地编码（GB18030）解码，只有「解出来再编码回去和原字节
// 一模一样」才算认作本地编码文本。读文件时用它：既能正常显示 GBK/GB18030 文件，
// 又不会把二进制垃圾当文本。
func decodeLocalLossless(b []byte) (string, bool) {
	if len(b) == 0 {
		return "", true
	}
	s, n, err := transform.Bytes(simplifiedchinese.GB18030.NewDecoder(), b)
	if err != nil || n != len(b) {
		return "", false
	}
	// 再编码回去必须和原字节完全一致，才算无损（含 U+FFFD、非法序列等情况都会被挡掉）
	back, ok := encodeLocalLossless(string(s))
	if !ok || !bytes.Equal(back, b) {
		return "", false
	}
	return string(s), true
}

// encodeLocalLossless 把 UTF-8 文本编码回本地编码（GB18030），出现表示不了的字符时返回 false
func encodeLocalLossless(s string) ([]byte, bool) {
	if s == "" {
		return nil, true
	}
	out, err := simplifiedchinese.GB18030.NewEncoder().Bytes([]byte(s))
	if err != nil {
		return nil, false
	}
	return out, true
}
