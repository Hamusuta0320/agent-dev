package webui

import (
	"encoding/json"
	"strings"
	"testing"

	"hamusuta/agent-dev/src/domain"
)

// 带图的提问发出去之后，聊天记录里那条要能看到缩略图。
//
// 界面画"用户说了什么"用的就是这个 started 事件（发出去当场就画，不等刷新）。
// 以前它只带文本，图发出去就从界面上消失了：输入区里那份预览已经被清掉，
// 新的缩略图又没画出来，要刷新页面走 snapshot 才重新出现。
func TestStartedEventCarriesImages(t *testing.T) {
	img := domain.Image{MediaType: "image/png", Data: "iVBORw0KGgoAAAANSUhEUg"}
	ev := startedEvent(1, "看看这张图", []domain.Image{img})

	if ev.Kind != kindStarted || ev.Text != "看看这张图" {
		t.Fatalf("started 事件的基本字段不对: %+v", ev)
	}
	if len(ev.Images) != 1 {
		t.Fatalf("应该带上 1 张图，实际 %d", len(ev.Images))
	}
	// 前端直接塞进 <img src>，所以必须是 data: 地址
	if want := "data:image/png;base64," + img.Data; ev.Images[0] != want {
		t.Fatalf("图片地址不对:\n实际 %q\n应该 %q", ev.Images[0], want)
	}

	// 序列化之后前端还得能读到（json 标签别写错）
	raw, err := json.Marshal(ev)
	if err != nil {
		t.Fatalf("序列化失败: %v", err)
	}
	if !strings.Contains(string(raw), `"images":["data:image/png;base64,`) {
		t.Fatalf("JSON 里没有 images 字段: %s", raw)
	}

	// 没带图时不该留一个空数组：前端判 ev.images && ev.images.length，空数组也不会出错，
	// 但少发一点是一点（base64 动辄几十 KB，这里省的是整条事件）
	plain := startedEvent(1, "纯文字", nil)
	if plain.Images != nil {
		t.Fatalf("没图时不该有 images: %+v", plain.Images)
	}
	if raw, _ := json.Marshal(plain); strings.Contains(string(raw), "images") {
		t.Fatalf("没图时 JSON 里不该出现 images: %s", raw)
	}
}

// 只发图不打字的情况：提问是空的，但图在，这条消息照样要能画出来
func TestStartedEventImageOnly(t *testing.T) {
	img := domain.Image{MediaType: "image/jpeg", Data: "AAAA"}
	ev := startedEvent(1, "", []domain.Image{img})
	if ev.Text != "" || len(ev.Images) != 1 {
		t.Fatalf("只有图时该是空文本 + 一张图: %+v", ev)
	}
}

// 坏数据（缺 media_type 或者缺 data）不该变成 src="data:;base64," 这种废地址，
// 直接丢掉：宁可少一张缩略图，也别在页面上留一个加载失败的破图标
func TestStartedEventDropsBrokenImages(t *testing.T) {
	ev := startedEvent(1, "带着半张图", []domain.Image{
		{MediaType: "image/png"}, // 没数据
		{Data: "AAAA"},           // 没类型
		{MediaType: "image/png", Data: "AAAA"},
	})
	if len(ev.Images) != 1 {
		t.Fatalf("只该留下那唯一一张完整的，实际 %d 张: %+v", len(ev.Images), ev.Images)
	}
}

// 历史（刷新 / 切会话走 snapshot）那条路也得带图：两条路都要有，缺一个就是"刷新前看不见"或"刷新后看不见"
func TestHistoryCarriesImages(t *testing.T) {
	s := domain.NewSession("/tmp/demo")
	s.AppendUser("这张图里是什么", domain.Image{MediaType: "image/png", Data: "iVBOR"})

	msgs := historyFromSession(s)
	if len(msgs) != 1 || msgs[0].Role != "user" {
		t.Fatalf("应该只有一条用户消息: %+v", msgs)
	}
	if len(msgs[0].Images) != 1 || !strings.HasPrefix(msgs[0].Images[0], "data:image/png;base64,") {
		t.Fatalf("历史里没带上图: %+v", msgs[0].Images)
	}
}
