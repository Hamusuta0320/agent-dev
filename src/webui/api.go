package webui

import (
	"net/http"
	"strings"

	"hamusuta/agent-dev/src/command"
	"hamusuta/agent-dev/src/core"
)

// 命令补全的候选要的东西：内置命令 + 装好的技能。
// 前端打一个 / 就把这份拉过去，之后在本地做前缀匹配（断网、卡顿都不影响打字手感）
type completions struct {
	Commands []command.Spec `json:"commands"`
	Skills   []command.Spec `json:"skills"`
}

func (m *Manager) handleCommands() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		out := completions{Commands: command.Specs}
		for _, skill := range core.Skills() {
			out.Skills = append(out.Skills, command.Spec{
				Name: "/" + skill.Name,
				Desc: skill.Desc,
			})
		}
		if out.Skills == nil {
			out.Skills = []command.Spec{}
		}
		writeJSON(w, http.StatusOK, out)
	})
}

// commandRequest 是执行一条本地命令的请求体
type commandRequest struct {
	Command string `json:"command"`
}

func (m *Manager) handleCommand() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, ok := m.sessionFrom(w, r)
		if !ok {
			return
		}
		var req commandRequest
		if err := decodeBody(w, r, &req); err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		input := strings.TrimSpace(req.Command)
		if input == "" {
			fail(w, http.StatusBadRequest, "命令不能是空的")
			return
		}
		writeJSON(w, http.StatusOK, m.runCommand(s, input))
	})
}

func (m *Manager) handleGetConfig() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// ?new=1 是"新增一套"：给一张空白表单，不带就是改当前那套
		if r.URL.Query().Get("new") == "1" {
			writeJSON(w, http.StatusOK, m.newConfigForm())
			return
		}
		writeJSON(w, http.StatusOK, m.configForm())
	})
}

func (m *Manager) handleSaveConfig() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req configRequest
		if err := decodeBody(w, r, &req); err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		view, err := m.saveConfig(req)
		if err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		meta := m.meta()
		writeJSON(w, http.StatusOK, map[string]any{"config": view, "meta": meta})
	})
}

// handleDeleteConfigModel 删掉一套模型配置：?name=xxx
func (m *Manager) handleDeleteConfigModel() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		name := strings.TrimSpace(r.URL.Query().Get("name"))
		if name == "" {
			fail(w, http.StatusBadRequest, "没说要删哪一套")
			return
		}
		view, err := m.deleteModel(name)
		if err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		meta := m.meta()
		writeJSON(w, http.StatusOK, map[string]any{"config": view, "meta": meta})
	})
}

// handleListModels 从接口拉模型列表，给表单里"挑一个"用。
// 地址和密钥从查询参数来（用户刚在表单里改过，还没保存）
func (m *Manager) handleListModels() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		q := r.URL.Query()
		names, err := m.listModels(q.Get("url"), q.Get("key"))
		if err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]any{"models": names})
	})
}
