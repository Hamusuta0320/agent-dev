package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
	"time"
	"unicode"

	"hamusuta/agent-dev/src/domain"
)

// Store 把会话按 JSON 一个文件存到磁盘上
type Store struct {
	Dir string
}

func NewStore(dir string) *Store {
	return &Store{Dir: dir}
}

// workspaceFile 记着这个会话目录对应哪个工作目录。文件名固定，所以列会话时要跳过它
const workspaceFile = "workspace.json"

// Workspace 是一个工作区（也就是启动时所在的那个目录）。
// 会话统一存在用户目录下，按工作区分开放，换个目录启动就只看那个目录的会话。
type Workspace struct {
	// Id 是工作目录路径换出来的 id，同时用作会话目录名
	Id string `json:"id"`
	// Path 是工作目录的绝对路径，留着好排查——id 里的指纹认不出人话
	Path string `json:"path"`
	// LastOpenedAt 是最后一次在这个目录里启动的时间
	LastOpenedAt time.Time `json:"last_opened_at"`
}

// DefaultRoot 返回所有工作区的会话总目录：<用户目录>/.agent-dev/sessions
func DefaultRoot() (string, error) {
	home, err := os.UserHomeDir()
	if err != nil {
		return "", fmt.Errorf("找不到用户目录: %w", err)
	}
	return filepath.Join(home, ".agent-dev", "sessions"), nil
}

// WorkspaceId 把工作目录换算成一个能当目录名的 id：<目录名>-<路径指纹>。
// 带目录名是为了人能看出这是哪个项目，带指纹是为了不同路径不会撞在一起。
func WorkspaceId(workDir string) (string, error) {
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return "", fmt.Errorf("解析工作目录失败: %w", err)
	}
	abs = filepath.Clean(abs)
	// Windows 上路径大小写不敏感，同一个目录别被当成两个工作区
	key := abs
	if runtime.GOOS == "windows" {
		key = strings.ToLower(key)
	}
	sum := sha256.Sum256([]byte(key))
	return slug(filepath.Base(abs)) + "-" + hex.EncodeToString(sum[:])[:10], nil
}

// slug 把目录名收拾成能当文件名的一段：字母数字留着，其余换成 -，
// 太长截断，什么都没剩就用 root（比如直接根目录启动）
func slug(name string) string {
	var b strings.Builder
	lastDash := false
	for _, r := range name {
		if unicode.IsLetter(r) || unicode.IsDigit(r) {
			b.WriteRune(r)
			lastDash = false
			continue
		}
		if b.Len() > 0 && !lastDash {
			b.WriteByte('-')
			lastDash = true
		}
	}
	out := strings.Trim(b.String(), "-")
	if runes := []rune(out); len(runes) > 32 {
		out = strings.Trim(string(runes[:32]), "-")
	}
	if out == "" {
		return "root"
	}
	return out
}

// DefaultDir 返回某个工作区的会话目录：<用户目录>/.agent-dev/sessions/<工作区 id>
func DefaultDir(workDir string) (string, error) {
	root, err := DefaultRoot()
	if err != nil {
		return "", err
	}
	id, err := WorkspaceId(workDir)
	if err != nil {
		return "", err
	}
	return filepath.Join(root, id), nil
}

// LegacyDir 是老版本的会话目录：<workDir>/.agent/sessions。
// 会话搬到用户目录之后，老目录只用来迁移一次。
func LegacyDir(workDir string) string {
	return filepath.Join(workDir, ".agent", "sessions")
}

// SaveWorkspace 在会话目录里记一份工作区信息，顺便把"最近打开时间"刷新一下
func (s *Store) SaveWorkspace(workDir string) error {
	abs, err := filepath.Abs(workDir)
	if err != nil {
		return fmt.Errorf("解析工作目录失败: %w", err)
	}
	info := Workspace{Id: filepath.Base(s.Dir), Path: abs, LastOpenedAt: time.Now()}
	data, err := json.MarshalIndent(info, "", "  ")
	if err != nil {
		return fmt.Errorf("序列化工作区信息失败: %w", err)
	}
	if err := os.MkdirAll(s.Dir, 0o755); err != nil {
		return fmt.Errorf("创建会话目录失败: %w", err)
	}
	return os.WriteFile(filepath.Join(s.Dir, workspaceFile), data, 0o644)
}

// Migrate 把老目录里的会话复制到新目录，返回复制了几个。
// 只复制不删除：老目录留着当备份，同一个 id 的目标文件已存在时跳过，重复跑也没事。
func Migrate(oldDir, newDir string) (int, error) {
	if oldDir == "" || newDir == "" || filepath.Clean(oldDir) == filepath.Clean(newDir) {
		return 0, nil
	}
	entries, err := os.ReadDir(oldDir)
	if errors.Is(err, os.ErrNotExist) {
		return 0, nil
	}
	if err != nil {
		return 0, fmt.Errorf("读取老会话目录失败: %w", err)
	}
	copied := 0
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !strings.HasSuffix(name, ".json") {
			continue
		}
		dst := filepath.Join(newDir, name)
		if _, err := os.Stat(dst); err == nil {
			continue // 已经迁过了
		}
		data, err := os.ReadFile(filepath.Join(oldDir, name))
		if err != nil {
			return copied, fmt.Errorf("读取老会话 %s 失败: %w", name, err)
		}
		if err := os.MkdirAll(newDir, 0o755); err != nil {
			return copied, fmt.Errorf("创建会话目录失败: %w", err)
		}
		if err := os.WriteFile(dst, data, 0o644); err != nil {
			return copied, fmt.Errorf("写入会话 %s 失败: %w", name, err)
		}
		copied++
	}
	return copied, nil
}

func (s *Store) path(id string) (string, error) {
	// id 会被拼进路径，挡一下跨目录的文件名
	if id == "" || id != filepath.Base(id) || strings.ContainsAny(id, `/\`) {
		return "", fmt.Errorf("非法的会话 id: %q", id)
	}
	return filepath.Join(s.Dir, id+".json"), nil
}

// isSessionFile 判断目录项是不是一个会话文件（工作区信息不算）
func isSessionFile(name string) bool {
	return name != workspaceFile && strings.HasSuffix(name, ".json")
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

// Info 是一条会话的摘要，用来在列表里挑一个恢复
type Info struct {
	Id string
	// Size 是会话文件大小，粗略体现会话有多长
	Size    int64
	ModTime time.Time
}

// ListInfo 列出已保存的会话摘要，按修改时间从新到旧（最新的排最前）。
// 只看文件名和文件属性，不解析会话内容——会话多了也不会卡。
func (s *Store) ListInfo() ([]Info, error) {
	entries, err := os.ReadDir(s.Dir)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取会话目录失败: %w", err)
	}
	infos := make([]Info, 0, len(entries))
	for _, e := range entries {
		name := e.Name()
		if e.IsDir() || !isSessionFile(name) {
			continue
		}
		info := Info{Id: strings.TrimSuffix(name, ".json")}
		if fi, err := e.Info(); err == nil {
			info.Size = fi.Size()
			info.ModTime = fi.ModTime()
		}
		infos = append(infos, info)
	}
	sort.Slice(infos, func(i, j int) bool {
		if !infos[i].ModTime.Equal(infos[j].ModTime) {
			return infos[i].ModTime.After(infos[j].ModTime)
		}
		return infos[i].Id > infos[j].Id
	})
	return infos, nil
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
		if e.IsDir() || !isSessionFile(name) {
			continue
		}
		ids = append(ids, strings.TrimSuffix(name, ".json"))
	}
	sort.Strings(ids)
	return ids, nil
}
