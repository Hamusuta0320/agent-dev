package session

import (
	"crypto/sha256"
	"encoding/hex"
	"encoding/json"
	"errors"
	"fmt"
	"io"
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

// LoadWorkspace 读回某个工作区目录里记的 workspace.json。
// 目录里没有这份文件（老版本建的、或者文件被删了）时返回 os.ErrNotExist，
// 调用方自己决定是当"未知工作区"跳过还是用目录名兜底
func LoadWorkspace(dir string) (Workspace, error) {
	data, err := os.ReadFile(filepath.Join(dir, workspaceFile))
	if err != nil {
		return Workspace{}, err
	}
	var info Workspace
	if err := json.Unmarshal(data, &info); err != nil {
		return Workspace{}, fmt.Errorf("解析工作区信息失败: %w", err)
	}
	// 目录名才是权威的 id：workspace.json 里那位是写文件时算出来的，
	// 目录被手工改过名就对不上了
	info.Id = filepath.Base(dir)
	return info, nil
}

// ListWorkspaces 列出 root 下所有工作区，最近打开过的排最前。
//
// 只认子目录：每个工作区就是一层目录。目录里没有 workspace.json 的也算一个
// 工作区（Path 留空），不然手工建出来的目录在列表里就凭空消失了。
// 点开头的目录是内部用的（比如删除工作区时搬去 .trash 的那份），不当工作区列出来。
func ListWorkspaces(root string) ([]Workspace, error) {
	entries, err := os.ReadDir(root)
	if errors.Is(err, os.ErrNotExist) {
		return nil, nil
	}
	if err != nil {
		return nil, fmt.Errorf("读取工作区目录失败: %w", err)
	}
	out := make([]Workspace, 0, len(entries))
	for _, e := range entries {
		if !e.IsDir() || strings.HasPrefix(e.Name(), ".") {
			continue
		}
		dir := filepath.Join(root, e.Name())
		info, err := LoadWorkspace(dir)
		if err != nil {
			if errors.Is(err, os.ErrNotExist) {
				out = append(out, Workspace{Id: e.Name()})
				continue
			}
			// 单个工作区坏了不该让整个列表报错
			continue
		}
		out = append(out, info)
	}
	sort.Slice(out, func(i, j int) bool {
		if !out[i].LastOpenedAt.Equal(out[j].LastOpenedAt) {
			return out[i].LastOpenedAt.After(out[j].LastOpenedAt)
		}
		return out[i].Id < out[j].Id
	})
	return out, nil
}

// Delete 删掉一个会话文件。文件本来就不在时 ok 为 false、err 为 nil——
// 这不是错误，界面上把那条摘掉就行
func (s *Store) Delete(id string) (bool, error) {
	path, err := s.path(id)
	if err != nil {
		return false, err
	}
	if err := os.Remove(path); err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return false, nil
		}
		return false, fmt.Errorf("删除会话失败: %w", err)
	}
	return true, nil
}

// trashDir 是"仅移除展示"时把工作区目录搬去的地方，藏在会话根目录下。
// 带前导点，所以 ListWorkspaces 不会把它自己当成一个工作区
const trashDir = ".trash"

// RemoveWorkspace 把一个工作区从列表里摘掉：
//   - purge 为真：连会话目录一起删，里面的会话文件就真没了
//   - purge 为假：整个目录搬到 <root>/.trash/<id>-<时间戳>，会话文件还在，
//     想找回来手工搬回 <root>/<id> 就行
//
// 只动 <root>/<id> 这一层，绝不碰工作目录本身（那是用户的代码）。
// 返回搬去（或删掉）的路径，调用方可以打出来给人看
func RemoveWorkspace(root, id string, purge bool) (string, error) {
	if root == "" {
		return "", fmt.Errorf("算不出会话根目录")
	}
	// id 会拼进路径，挡一下跨目录的名字
	if id == "" || id != filepath.Base(id) || strings.ContainsAny(id, `/\`) || strings.HasPrefix(id, ".") {
		return "", fmt.Errorf("非法的工作区 id: %q", id)
	}
	src := filepath.Join(root, id)
	fi, err := os.Stat(src)
	if err != nil {
		if errors.Is(err, os.ErrNotExist) {
			return "", fmt.Errorf("找不到工作区 %s 的会话目录", id)
		}
		return "", fmt.Errorf("查看工作区目录失败: %w", err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%s 不是目录", src)
	}
	if purge {
		if err := os.RemoveAll(src); err != nil {
			return "", fmt.Errorf("删除工作区目录失败: %w", err)
		}
		return src, nil
	}

	trash := filepath.Join(root, trashDir)
	if err := os.MkdirAll(trash, 0o755); err != nil {
		return "", fmt.Errorf("创建回收目录失败: %w", err)
	}
	// 目录名带上时间戳：同一个工作区删两次也不会撞在一起
	dst := filepath.Join(trash, fmt.Sprintf("%s-%s", id, time.Now().Format("20060102-150405")))
	if err := os.Rename(src, dst); err != nil {
		return "", fmt.Errorf("把工作区目录搬到回收站失败: %w", err)
	}
	return dst, nil
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
	// Title 是会话的标题，第一轮聊完由模型起；老会话没有，是空串
	Title string
	// Size 是会话文件大小，粗略体现会话有多长
	Size    int64
	ModTime time.Time
}

// titleHeadBytes 是找标题时只读文件头的字节数。标题紧跟在 id 后面，
// 这点字节足够；会话再长也不用整个读进来
const titleHeadBytes = 512

// readTitle 从会话文件头上认出标题。没标题（老会话）或者读不出来时返回空串。
// 文件只读开头一小段，所以里面那个 JSON 多半是断的——不能直接解整个对象
// （截断的 JSON 解出来字段是空的），得顺着键一个个走，碰到 title 就取出来收工。
func (s *Store) readTitle(id string) string {
	path, err := s.path(id)
	if err != nil {
		return ""
	}
	f, err := os.Open(path)
	if err != nil {
		return ""
	}
	defer f.Close()

	dec := json.NewDecoder(io.LimitReader(f, titleHeadBytes))
	if tok, err := dec.Token(); err != nil || tok != json.Delim('{') {
		return ""
	}
	for dec.More() {
		keyTok, err := dec.Token()
		if err != nil {
			return ""
		}
		if keyTok != "title" {
			// 别的字段整段跳过，值太长（比如 messages）就在这儿断了，反正标题已经错过了
			var skip json.RawMessage
			if err := dec.Decode(&skip); err != nil {
				return ""
			}
			continue
		}
		var title string
		if err := dec.Decode(&title); err != nil {
			return ""
		}
		return strings.TrimSpace(title)
	}
	return ""
}

// ListInfo 列出已保存的会话摘要，按修改时间从新到旧（最新的排最前）。
// 只读文件名、文件属性和文件头里的标题，不解析整个会话——会话多了也不会卡。
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
		info.Title = s.readTitle(info.Id)
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
