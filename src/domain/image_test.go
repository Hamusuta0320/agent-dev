package domain

import (
	"encoding/base64"
	"strings"
	"testing"
)

// pngBytes 是一段带正确 PNG 文件头的假数据：认识图片只看文件头，内容不必是真图
func pngBytes(n int) []byte {
	head := []byte("\x89PNG\r\n\x1a\n")
	if n < len(head) {
		return head[:n]
	}
	return append(head, make([]byte, n-len(head))...)
}

func TestSniffImageType(t *testing.T) {
	cases := []struct {
		name string
		raw  []byte
		want string
	}{
		{"png", pngBytes(10), ImagePNG},
		{"jpeg", []byte{0xFF, 0xD8, 0xFF, 0xE0, 0x00}, ImageJPEG},
		{"gif", []byte("GIF89a...."), ImageGIF},
		{"webp", []byte("RIFF\x00\x00\x00\x00WEBPVP8 "), ImageWebP},
		{"纯文本", []byte("package main"), ""},
		{"太短", []byte{0x89}, ""},
		{"空", nil, ""},
	}
	for _, c := range cases {
		if got := SniffImageType(c.raw); got != c.want {
			t.Fatalf("%s: 认出的是 %q，应该是 %q", c.name, got, c.want)
		}
	}
}

// 图片包成能发给模型的样子：类型按文件头认，内容是 base64，data: 地址拼得对
func TestNewImage(t *testing.T) {
	raw := pngBytes(1000)
	img, err := NewImage(raw)
	if err != nil {
		t.Fatalf("应该认得出来: %v", err)
	}
	if img.MediaType != ImagePNG || img.Kind() != "png" {
		t.Fatalf("类型不对: %s / %s", img.MediaType, img.Kind())
	}
	if img.Size() != len(raw) {
		t.Fatalf("大小应该按 base64 长度换算成 %d，实际 %d", len(raw), img.Size())
	}
	if want := "data:image/png;base64," + base64.StdEncoding.EncodeToString(raw); img.DataURL() != want {
		t.Fatalf("data: 地址不对: %s", img.DataURL())
	}
	if img.Empty() {
		t.Fatal("有内容的图片不该算空")
	}

	// 认不出的格式、空内容、太大的都直接拒掉，别等接口那边整条请求失败
	if _, err := NewImage([]byte("这不是图片")); err == nil {
		t.Fatal("认不出的格式应该报错")
	}
	if _, err := NewImage(nil); err == nil {
		t.Fatal("空图片应该报错")
	}
	big := pngBytes(MaxImageBytes + 1)
	if _, err := NewImage(big); err == nil || !strings.Contains(err.Error(), "太大") {
		t.Fatalf("超过上限应该报错并说清楚: %v", err)
	}
}

// 只有图片没有文字的提问也要发给模型：那也是一次正经提问
func TestSessionHistoryKeepsImages(t *testing.T) {
	s := NewSession("/tmp/demo")
	img, err := NewImage(pngBytes(64))
	if err != nil {
		t.Fatalf("造图片失败: %v", err)
	}
	s.AppendUser("", img)     // 只贴了图
	s.AppendUser("有字有图", img) // 图和字都有
	s.AppendUser("")          // 什么都没有：这种才该跳过

	history := s.History()
	if len(history) != 2 {
		t.Fatalf("应该留下两条，实际 %d 条: %+v", len(history), history)
	}
	if len(history[0].Images) != 1 || history[0].Images[0].Data != img.Data {
		t.Fatalf("图片没跟着历史走: %+v", history[0])
	}
	if history[1].Content != "有字有图" || len(history[1].Images) != 1 {
		t.Fatalf("文字和图片都该留着: %+v", history[1])
	}
}
