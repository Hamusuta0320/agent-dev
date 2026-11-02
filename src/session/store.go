package session

import (
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"sort"
	"strings"

	"hamusuta/agent-dev/src/domain"
)

// Store 把会话按 JSON 一个文件存到磁盘上
type Store struct {
	Dir string
}

func NewStore(dir string) *Store {
	return &Store{Dir: dir}
}

// DefaultDir 返回会话默认存放目录：<workDir>/.agent/sessions
func DefaultDir(workDir string) string {
	return filepath.Join(workDir, ".agent", "sessions")
}

func (s *Store) path(id string) (string, error) {
	// id 会被拼进路径，挡一下跨目录的文件名
	if id == "" || id != filepath.Base(id) || strings.ContainsAny(id, `/\`) {
		return "", fmt.Errorf("非法的会话 id: %q", id)
	}
	return filepath.Join(s.Dir, id+".json"), nil
}

// Save 覆盖保存整个会话。先写临时文件再改名，中途挂掉不会留下半个文件
func (s *Store) Save(session *domain.Session) error {
	if session == nil {
		return errors.New("会话为空")
	}
	path, err := s.path(session.Id)
	if err != nil {
		return err
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return fmt.Errorf("创建会话目录失败: %w", err)
	}
	data, err := json.MarshalIndent(session, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化会话失败: %w", err)
	}
	tmp := path + ".tmp"
	if err := os.WriteFile(tmp, data, 0o644); err != nil {
		return fmt.Errorf("写入会话失败: %w", err)
	}
	if err := os.Rename(tmp, path); err != nil {
		return fmt.Errorf("保存会话失败: %w", err)
	}
	return nil
}

func (s *Store) Load(id string) (*domain.Session, error) {
	path, err := s.path(id)
	if err != nil {
		return nil, err
	}
	data, err := os.ReadFile(path)
	if err != nil {
		return nil, fmt.Errorf("读取会话失败: %w", err)
	}
	var session domain.Session
	if err := json.Unmarshal(data, &session); err != nil {
		return nil, fmt.Errorf("解析会话失败: %w", err)
	}
	if session.Id == "" {
		session.Id = id
	}
	return &session, nil
}

// List 列出已保存的会话 id，按时间（文件名）从旧到新
func (s *Store) List() ([]string, error) {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取会话目录失败: %w", err)
	}
	ids := make([]string, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		ids = append(ids, strings.TrimSuffix(name, ".json"))
	}
	sort.Strings(ids)
	return ids, nil
}
