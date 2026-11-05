package provider

import (
	"context"
	"encoding/json"
	"fmt"
	"io"
	"net/http"
	"strings"
	"time"
)

const (
	// listModelsTimeout 是拉一次模型列表最多等多久。它只是给表单摆个候选，
	// 拖太久不如让用户手填
	listModelsTimeout = 20 * time.Second
	// maxModelsBody 是模型列表响应体的上限：接口不正常时别把一大坨东西读进内存
	maxModelsBody = 4 << 20
)

// ListModels 请求 {ApiUrl}/models 拿到这套接口上能用的模型名。
//
// 这是 OpenAI 兼容协议里的标准接口，DeepSeek、OpenAI 和各家转发都认，
// 所以配好地址和密钥之后模型名不用再去别处查（手填也照样支持）。
// 地址不通、密钥不对时返回带原因的错误，界面直接摆给用户看。
func ListModels(ctx context.Context, apiUrl, apiKey string) ([]string, error) {
	base := strings.TrimRight(strings.TrimSpace(apiUrl), "/")
	if base == "" {
		return nil, fmt.Errorf("接口地址没填")
	}
	key := strings.TrimSpace(apiKey)
	if key == "" {
		return nil, fmt.Errorf("密钥没填")
	}

	ctx, cancel := context.WithTimeout(contextOrBackground(ctx), listModelsTimeout)
	defer cancel()

	req, err := http.NewRequestWithContext(ctx, http.MethodGet, base+"/models", nil)
	if err != nil {
		return nil, fmt.Errorf("构建请求失败: %w", err)
	}
	req.Header.Set("Authorization", "Bearer "+key)

	resp, err := httpClient.Do(req)
	if err != nil {
		return nil, fmt.Errorf("请求模型列表失败: %w", err)
	}
	defer resp.Body.Close()

	body, err := io.ReadAll(io.LimitReader(resp.Body, maxModelsBody))
	if err != nil {
		return nil, fmt.Errorf("读取模型列表失败: %w", err)
	}
	if resp.StatusCode != http.StatusOK {
		return nil, &statusError{code: resp.StatusCode, body: truncateBody(string(body))}
	}

	names := parseModelIds(body)
	if len(names) == 0 {
		return nil, fmt.Errorf("接口没返回任何模型，密钥或地址可能不对: %s", truncateBody(string(body)))
	}
	return names, nil
}

// parseModelIds 从 /models 的响应里挑出模型名。标准写法是 {"data":[{"id":...}]}，
// 也有转发把裸数组直接端出来的，两种都认；重复的按第一次出现算
func parseModelIds(body []byte) []string {
	var payload struct {
		Data []struct {
			Id string `json:"id"`
		} `json:"data"`
	}
	if err := json.Unmarshal(body, &payload); err != nil || len(payload.Data) == 0 {
		var direct []struct {
			Id string `json:"id"`
		}
		if err := json.Unmarshal(body, &direct); err != nil {
			return nil
		}
		payload.Data = direct
	}

	seen := make(map[string]bool, len(payload.Data))
	out := make([]string, 0, len(payload.Data))
	for _, m := range payload.Data {
		id := strings.TrimSpace(m.Id)
		if id == "" || seen[id] {
			continue
		}
		seen[id] = true
		out = append(out, id)
	}
	return out
}
