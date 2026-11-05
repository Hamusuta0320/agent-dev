package provider

import (
	"context"
	"fmt"
	"net/http"
	"net/http/httptest"
	"strings"
	"testing"
)

// /models 是 OpenAI 兼容协议里的标准接口：认 Bearer 密钥，返回 data 里的模型名
func TestListModels(t *testing.T) {
	var gotPath, gotAuth string
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		gotPath, gotAuth = r.URL.Path, r.Header.Get("Authorization")
		w.Header().Set("Content-Type", "application/json")
		fmt.Fprint(w, `{"object":"list","data":[
			{"id":"deepseek-chat","object":"model"},
			{"id":"deepseek-reasoner","object":"model"},
			{"id":"deepseek-chat","object":"model"},
			{"id":"  ","object":"model"}]}`)
	}))
	t.Cleanup(srv.Close)

	names, err := ListModels(context.Background(), srv.URL+"/", "sk-test")
	if err != nil {
		t.Fatalf("拉模型列表不该失败: %v", err)
	}
	if gotPath != "/models" {
		t.Fatalf("请求路径不对: %q", gotPath)
	}
	if gotAuth != "Bearer sk-test" {
		t.Fatalf("密钥没带上: %q", gotAuth)
	}
	// 空名字和重复的都要滤掉，顺序保持接口给的顺序
	if strings.Join(names, ",") != "deepseek-chat,deepseek-reasoner" {
		t.Fatalf("模型列表不对: %v", names)
	}
}

// 有的转发把裸数组直接端出来，也得认
func TestListModelsBareArray(t *testing.T) {
	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `[{"id":"a"},{"id":"b"}]`)
	}))
	t.Cleanup(srv.Close)

	names, err := ListModels(context.Background(), srv.URL, "sk-test")
	if err != nil {
		t.Fatalf("裸数组也该认: %v", err)
	}
	if strings.Join(names, ",") != "a,b" {
		t.Fatalf("模型列表不对: %v", names)
	}
}

// 地址或密钥没填就别发请求了；密钥不对时把接口的说法带回来
func TestListModelsErrors(t *testing.T) {
	if _, err := ListModels(context.Background(), "  ", "sk-test"); err == nil {
		t.Fatal("地址没填应该报错")
	}
	if _, err := ListModels(context.Background(), "https://api.deepseek.com", ""); err == nil {
		t.Fatal("密钥没填应该报错")
	}

	srv := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		w.WriteHeader(http.StatusUnauthorized)
		fmt.Fprint(w, `{"error":{"message":"invalid api key"}}`)
	}))
	t.Cleanup(srv.Close)

	_, err := ListModels(context.Background(), srv.URL, "sk-bad")
	if err == nil {
		t.Fatal("密钥不对应该报错")
	}
	if !strings.Contains(err.Error(), "401") || !strings.Contains(err.Error(), "invalid api key") {
		t.Fatalf("错误里该说明状态码和原因: %v", err)
	}

	// 接口回了个空列表：说明地址或密钥不对，不能当成"没有模型可用"悄悄过去
	empty := httptest.NewServer(http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		fmt.Fprint(w, `{"object":"list","data":[]}`)
	}))
	t.Cleanup(empty.Close)
	if _, err := ListModels(context.Background(), empty.URL, "sk-test"); err == nil {
		t.Fatal("空列表应该报错")
	}
}
