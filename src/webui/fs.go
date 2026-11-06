package webui

import (
	"errors"
	"fmt"
	"io/fs"
	"os"
	"path/filepath"
	"runtime"
	"sort"
	"strings"
)

// 目录选择器的数据源。界面可能在远程浏览器里打开，不能弹服务器那台机器的
// 原生文件对话框，所以目录得由页面通过这几个接口自己列、自己画。

// dirEntry 是选择器列表里的一项。只列目录：这个选择器是用来挑工作区的，
// 文件在这儿没有用，反而会把列表撑得很长
type dirEntry struct {
	Name string `json:"name"`
	Path string `json:"path"`
}

// dirList 是 GET /api/fs/list 的响应。roots 为 true 时 entries 是盘符（Unix 下是 /），
// path 和 parent 都为空；否则 path 是当前目录，parent 是上一级（已经是顶了就是空）
type dirList struct {
	Path    string     `json:"path"`
	Parent  string     `json:"parent,omitempty"`
	Roots   bool       `json:"roots"`
	Entries []dirEntry `json:"entries"`
	Home    string     `json:"home,omitempty"`
	WorkDir string     `json:"work_dir,omitempty"`
}

// listDirs 列一个目录的子目录。path 为空表示列"根"：Windows 是各盘符，Unix 是 /
func (m *Manager) listDirs(path string) (dirList, error) {
	if strings.TrimSpace(path) == "" {
		return dirList{
			Roots:   true,
			Entries: rootEntries(),
			Home:    homeDir(),
			WorkDir: m.workdir,
		}, nil
	}

	abs, err := resolveDir(path)
	if err != nil {
		return dirList{}, err
	}
	entries, err := readDirs(abs)
	if err != nil {
		return dirList{}, err
	}
	out := dirList{
		Path:    abs,
		Entries: entries,
		Home:    homeDir(),
		WorkDir: m.workdir,
	}
	if parent := filepath.Dir(abs); parent != abs {
		out.Parent = parent
	}
	return out, nil
}

// resolveDir 把用户给的路径规范化成一个存在的目录。写错、指向文件都在这儿挡住，
// 后面就不用到处判了
func resolveDir(path string) (string, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return "", errors.New("要填一个目录路径")
	}
	// Windows 上单写 "C:" 是"这个盘的当前目录"，不是盘根。不补分隔符会被当相对路径
	// 拼到服务端的工作目录后面，看着像路径写坏了
	if runtime.GOOS == "windows" && len(path) == 2 && path[1] == ':' {
		path += `\`
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return "", fmt.Errorf("解析目录 %s 失败: %w", path, err)
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return "", fmt.Errorf("打不开 %s: %w", abs, err)
	}
	if !fi.IsDir() {
		return "", fmt.Errorf("%s 不是目录", abs)
	}
	return abs, nil
}

// readDirs 读一个目录下的子目录。隐藏目录（点开头）照常返回，
// 显不显示、怎么显示交给前端
func readDirs(dir string) ([]dirEntry, error) {
	items, err := os.ReadDir(dir)
	if err != nil {
		return nil, fmt.Errorf("读目录 %s 失败: %w", dir, err)
	}
	out := make([]dirEntry, 0, len(items))
	for _, item := range items {
		if !isDirEntry(dir, item) {
			continue
		}
		name := item.Name()
		out = append(out, dirEntry{Name: name, Path: filepath.Join(dir, name)})
	}
	// 按名字排，忽略大小写：Windows 的资源管理器也是这个习惯
	sort.Slice(out, func(i, j int) bool {
		return strings.ToLower(out[i].Name) < strings.ToLower(out[j].Name)
	})
	return out, nil
}

// isDirEntry 判断一项是不是目录。符号链接 / junction 自身不算目录，
// 但指向目录的时候要让用户点得进去
func isDirEntry(dir string, item fs.DirEntry) bool {
	if item.IsDir() {
		return true
	}
	if item.Type()&fs.ModeSymlink == 0 {
		return false
	}
	fi, err := os.Stat(filepath.Join(dir, item.Name()))
	return err == nil && fi.IsDir()
}

// rootEntries 是"根"那一层。Windows 逐个探一下盘符在不在（标准库没有列盘符的
// 便携接口，为此引一个依赖不值当）；别的系统就是 /
func rootEntries() []dirEntry {
	if runtime.GOOS == "windows" {
		out := make([]dirEntry, 0, 26)
		for c := 'C'; c <= 'Z'; c++ {
			root := string(c) + `:\`
			if fi, err := os.Stat(root); err == nil && fi.IsDir() {
				out = append(out, dirEntry{Name: root, Path: root})
			}
		}
		if len(out) > 0 {
			return out
		}
	}
	return []dirEntry{{Name: "/", Path: "/"}}
}

// homeDir 是用户主目录，找不到就空着（前端会不显示这个快捷按钮）
func homeDir() string {
	home, err := os.UserHomeDir()
	if err != nil {
		return ""
	}
	return home
}

// createDir 在 parent 下建一个叫 name 的目录，返回新目录的绝对路径。
//
// name 必须是一层名字，不是路径：带分隔符、. 和 .. 一律拒绝。
// 不然界面上的"新建文件夹"就成了任意位置写目录的入口
func createDir(parent, name string) (string, error) {
	abs, err := resolveDir(parent)
	if err != nil {
		return "", err
	}
	name = strings.TrimSpace(name)
	switch {
	case name == "":
		return "", errors.New("要填文件夹名字")
	case name == "." || name == "..":
		return "", errors.New("文件夹名字不能是 . 或 ..")
	case strings.ContainsAny(name, `/\`):
		return "", errors.New("文件夹名字不能带路径分隔符")
	case filepath.Base(name) != name:
		return "", errors.New("文件夹名字不能带路径")
	}
	target := filepath.Join(abs, name)
	if err := os.Mkdir(target, 0o755); err != nil {
		if errors.Is(err, fs.ErrExist) {
			return "", fmt.Errorf("%s 已经存在", target)
		}
		return "", fmt.Errorf("创建 %s 失败: %w", target, err)
	}
	return target, nil
}
