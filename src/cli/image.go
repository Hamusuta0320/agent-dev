package main

import (
	"errors"
	"fmt"
	"io"
	"net/url"
	"os"
	"path/filepath"
	"strings"

	"hamusuta/agent-dev/src/domain"

	tea "charm.land/bubbletea/v2"
	"github.com/atotto/clipboard"
)

// errNoClipboardImage 表示剪贴板里没有图片。这不算出错——用户可能只是想粘一段文字，
// 拿到它的那一方安静地说一句"没有图片"就行，别搞得像出故障了
var errNoClipboardImage = errors.New("剪贴板里没有图片")

// maxAttachments 是一条提问最多带几张图。图片会跟着会话存下来、每一轮都发给模型，
// 带太多既贵又慢，到上限就提示一句，别默默攒着
const maxAttachments = 8

// imageAttachment 是准备随下一条提问发出去的一张图。label 只在提示里用来指认是谁，
// 比如文件名或者"剪贴板"
type imageAttachment struct {
	label string
	img   domain.Image
}

// clipboardPasteMsg 是按下 Ctrl+V / Alt+V 之后从剪贴板取回来的东西：
// 有文字就是文字，没文字再看有没有图
type clipboardPasteMsg struct {
	text string
	img  domain.Image
	err  error
}

// clipboardText 读剪贴板里的文字，读不到就算没有。这里不看错误：
// 剪贴板里只有图的时候本来就没有文字，那不是故障，接着去看图就是了
func clipboardText() string {
	text, err := clipboard.ReadAll()
	if err != nil {
		return ""
	}
	return text
}

// pasteFromClipboard 起一个 Cmd 去剪贴板取一次。先看文字：终端肯把 Ctrl+V 放过来之后，
// 用户按它多半还是想贴文字，这时候别去动图片；真的只有图才当成附图。
// 读剪贴板得跑外部进程（Windows 上读图是一把 PowerShell），所以放进 tea.Cmd 里做，
// 界面不会跟着卡一下
func pasteFromClipboard() tea.Cmd {
	return func() tea.Msg {
		if text := clipboardText(); strings.TrimSpace(text) != "" {
			return clipboardPasteMsg{text: text}
		}
		raw, err := clipboardImage()
		if err != nil {
			return clipboardPasteMsg{err: err}
		}
		img, err := domain.NewImage(raw)
		return clipboardPasteMsg{img: img, err: err}
	}
}

// loadImageFile 读一个本地图片文件：类型按文件头认，大小有上限。
// 不是图片、太大、读不出来都返回错误，让调用方决定是提示还是当成普通文字
func loadImageFile(path string) (domain.Image, error) {
	info, err := os.Stat(path)
	if err != nil {
		return domain.Image{}, err
	}
	if info.IsDir() {
		return domain.Image{}, fmt.Errorf("%s 是目录", path)
	}
	if info.Size() > domain.MaxImageBytes {
		return domain.Image{}, fmt.Errorf("图片太大（%d KB），单张最多 %d MB",
			(info.Size()+1023)/1024, domain.MaxImageBytes>>20)
	}
	raw, err := os.ReadFile(path)
	if err != nil {
		return domain.Image{}, err
	}
	return domain.NewImage(raw)
}

// imagesFromPaste 从粘贴的内容里认出图片文件，读进来挂到下一条提问上。
// 拖一张图进终端时，终端贴进来的是它的路径（路径带空格会加引号，有的终端给 file:// 地址），
// 所以先按路径认一遍。
// 认不出图片就返回 nil——那这内容就是普通文字，照常贴进输入框；认得出但读不了的
// （太大、没权限）把原因带回来，界面得说一句，否则用户只看到路径被当成文字贴进来了。
func imagesFromPaste(content string) (imgs []imageAttachment, problem string) {
	lines := strings.Split(strings.TrimSpace(content), "\n")
	out := make([]imageAttachment, 0, len(lines))
	for _, line := range lines {
		path := pastedPath(line)
		if path == "" || !looksLikeImage(path) {
			return nil, ""
		}
		img, err := loadImageFile(path)
		if err != nil {
			return nil, fmt.Sprintf("%s 读不了：%v", filepath.Base(path), err)
		}
		out = append(out, imageAttachment{label: filepath.Base(path), img: img})
	}
	return out, ""
}

// pastedPath 把一行粘贴内容收拾成一个路径：去掉两头的引号和空白，解开 file:// 地址
func pastedPath(line string) string {
	s := strings.TrimSpace(line)
	if len(s) >= 2 {
		if q := s[0]; (q == '"' || q == '\'' || q == '`') && s[len(s)-1] == q {
			s = s[1 : len(s)-1]
		}
	}
	if strings.HasPrefix(strings.ToLower(s), "file://") {
		s = localPath(s)
	}
	return s
}

// localPath 把 file:// 地址还原成路径。不走 url.Parse：它会把 file://C:/x 里的
// 盘符当成主机名扔掉，Windows 上这么一解析路径就没了
func localPath(s string) string {
	rest := s[len("file://"):]
	if unescaped, err := url.PathUnescape(rest); err == nil {
		rest = unescaped
	}
	// file://localhost/x 里的主机名是"本机"，不是路径的一部分
	rest = strings.TrimPrefix(rest, "localhost/")
	// file:///C:/x.png 比 file://C:/x.png 多一个斜杠，盘符前面那个要摘掉；
	// unix 的 file:///home/x.png 摘了就错了，所以只在后面跟着盘符时才摘
	if trimmed := strings.TrimPrefix(rest, "/"); len(trimmed) > 1 && trimmed[1] == ':' {
		return trimmed
	}
	return rest
}

// looksLikeImage 只看文件头判断 path 是不是一张接口认得的图片。
// 为了认一张图把整个文件读一遍不值当，所以先读开头十几个字节
func looksLikeImage(path string) bool {
	f, err := os.Open(path)
	if err != nil {
		return false
	}
	defer f.Close()
	if info, err := f.Stat(); err != nil || !info.Mode().IsRegular() {
		return false
	}
	head := make([]byte, 12)
	n, _ := io.ReadFull(f, head)
	return domain.SniffImageType(head[:n]) != ""
}
