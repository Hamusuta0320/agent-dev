package domain

import (
	"encoding/base64"
	"fmt"
	"strings"
)

// 图片类型只认接口吃得下的这几种，别的（bmp、tiff、svg……）当成不认识，
// 免得发过去被接口整条请求打回来
const (
	ImagePNG  = "image/png"
	ImageJPEG = "image/jpeg"
	ImageGIF  = "image/gif"
	ImageWebP = "image/webp"
)

// MaxImageBytes 是单张图片的上限。图片要跟着会话一起存进 JSON（base64 还要再胖三分之一），
// 十几兆的图会让会话文件、每一轮请求都变得很难看，超过就请用户自己先压一下。
const MaxImageBytes = 5 << 20

// Image 是一张随提问发给模型的图片。Data 是原始字节的 base64，
// 会话文件里也原样存着，恢复会话时不用再去磁盘上找原图。
type Image struct {
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}

// SniffImageType 按文件头认图片类型，认不出返回空串。
// 不看扩展名：截图工具存出来的名字五花八门，文件头才是真的
func SniffImageType(raw []byte) string {
	switch {
	case len(raw) >= 8 && string(raw[:8]) == "\x89PNG\r\n\x1a\n":
		return ImagePNG
	case len(raw) >= 3 && raw[0] == 0xFF && raw[1] == 0xD8 && raw[2] == 0xFF:
		return ImageJPEG
	case len(raw) >= 6 && (string(raw[:6]) == "GIF87a" || string(raw[:6]) == "GIF89a"):
		return ImageGIF
	case len(raw) >= 12 && string(raw[:4]) == "RIFF" && string(raw[8:12]) == "WEBP":
		return ImageWebP
	}
	return ""
}

// NewImage 把一张图片的原始字节包成能发给模型的样子：类型按文件头认，内容转成 base64，
// 之后原始字节就可以扔了。认不出类型、或者太大都直接报错，让调用方去提示用户。
func NewImage(raw []byte) (Image, error) {
	if len(raw) == 0 {
		return Image{}, fmt.Errorf("图片是空的")
	}
	mediaType := SniffImageType(raw)
	if mediaType == "" {
		return Image{}, fmt.Errorf("认不出这是什么图片（支持 png / jpeg / gif / webp）")
	}
	if len(raw) > MaxImageBytes {
		return Image{}, fmt.Errorf("图片太大（%d KB），单张最多 %d MB", (len(raw)+1023)/1024, MaxImageBytes>>20)
	}
	return Image{
		MediaType: mediaType,
		Data:      base64.StdEncoding.EncodeToString(raw),
	}, nil
}

// Empty 说明这张图是空的（零值或者数据丢了）
func (i Image) Empty() bool { return i.Data == "" || i.MediaType == "" }

// Kind 是给人看的类型名：png / jpeg / gif / webp
func (i Image) Kind() string { return strings.TrimPrefix(i.MediaType, "image/") }

// Size 是原始字节数。base64 长度能直接换算出来，为了显示一句"1.2M"
// 把整张图解码一遍不值当
func (i Image) Size() int {
	n := len(i.Data)
	if n == 0 {
		return 0
	}
	size := n / 4 * 3
	switch {
	case strings.HasSuffix(i.Data, "=="):
		size -= 2
	case strings.HasSuffix(i.Data, "="):
		size--
	}
	return size
}

// DataURL 是发给接口的那个 data: 地址（openai 那套多模态写法）
func (i Image) DataURL() string {
	return "data:" + i.MediaType + ";base64," + i.Data
}
