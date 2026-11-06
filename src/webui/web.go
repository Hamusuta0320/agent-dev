// Package webui 是 agent-dev 的浏览器前端：同一条 core.Loop 跑在服务端，
// 进度通过 SSE 推给页面。core 本身只认 domain.EmitFunc，不关心对端是终端还是浏览器，
// 所以这一层不需要改 core 一行代码。
//
// 注意别和 src/web 混：那个目录在 doc/TODO.md 里留给"联网能力"（WebFetch / WebSearch），
// 这个包只是把界面从终端搬到浏览器。
package webui

import (
	"context"
	"embed"
	"encoding/base64"
	"encoding/json"
	"errors"
	"fmt"
	"io"
	"io/fs"
	"net"
	"net/http"
	"os"
	"path/filepath"
	"sort"
	"strings"
	"sync"
	"time"

	"hamusuta/agent-dev/src/command"
	"hamusuta/agent-dev/src/config"
	"hamusuta/agent-dev/src/core"
	"hamusuta/agent-dev/src/domain"
	"hamusuta/agent-dev/src/session"
	"hamusuta/agent-dev/src/tool"
)

// staticFS 是打包进二进制的页面。前端不引 node 工具链：改完页面直接 go build
// 就带上了，部署也只有一个可执行文件
//
//go:embed static
var staticFS embed.FS

// 服务端限制。两个上限都是为了"浏览器乱发也不会把服务端拖垮"
const (
	// maxBodyBytes 是单个请求体的上限：图片本来就有 5MB 的限制，base64 再胖三分之一，
	// 再留点余量给多条消息
	maxBodyBytes = 32 << 20
	// sseHeartbeat 是多久发一次心跳注释。中间隔着代理时，长时间没字节的空闲连接会被掐掉
	sseHeartbeat = 25 * time.Second
)

// Options 是起 web 前端要的东西，和 TUI 启动时拿到的是同一套
type Options struct {
	Addr string
	// WorkDir 是启动时所在目录，也就是默认工作区
	WorkDir string
	CfgPath string
	Cfg     config.GlobalConfig
	// Store 是默认工作区的会话目录，cli 那边已经准备好（含老会话迁移）
	Store *session.Store
	// Root 是所有工作区的会话总目录，留空用 session.DefaultRoot()。
	// 换工作区时按 <Root>/<工作区 id> 现算，所以这个值决定"能看见哪些工作区"
	Root string
	// Out 是启动提示写到哪儿，默认 os.Stdout
	Out io.Writer
}

// Serve 起 HTTP 服务，ctx 取消时优雅关掉。返回的服务错误里已经带上了地址，
// 调用方直接打出来就行
func Serve(ctx context.Context, opt Options) error {
	if opt.Out == nil {
		opt.Out = os.Stdout
	}
	m := newManager(opt)
	srv := &http.Server{
		Handler:           m.routes(),
		ReadHeaderTimeout: 15 * time.Second,
		ReadTimeout:       60 * time.Second,
		// 不设 WriteTimeout：SSE 是长连接，写超时会把正常的空转连接也切掉。
		// 单个响应的写上限由 handler 自己用 http.ResponseController 管
		IdleTimeout: 120 * time.Second,
	}

	ln, err := net.Listen("tcp", addrOrDefault(opt.Addr))
	if err != nil {
		return fmt.Errorf("监听 %s 失败: %w", addrOrDefault(opt.Addr), err)
	}
	fmt.Fprintf(opt.Out, "web 界面: http://%s\n", displayAddr(ln.Addr().String()))
	if !loopbackOnly(ln.Addr().String()) {
		// 这个界面没有任何登录验证，谁连上谁就能让 agent 读写这台机器的文件。
		// 不是不能让外面连，但得让人知道自己在做什么
		fmt.Fprintf(opt.Out, "注意: 正在监听所有网卡，界面没有登录验证，能读写本机文件。"+
			"只给自己用的话请监听 127.0.0.1\n")
	}
	fmt.Fprintf(opt.Out, "当前工作区: %s（Ctrl+C 停掉）\n", opt.WorkDir)
	fmt.Fprintf(opt.Out, "页面左上角可以切换或添加工作区\n")

	errCh := make(chan error, 1)
	go func() {
		if err := srv.Serve(ln); err != nil && !errors.Is(err, http.ErrServerClosed) {
			errCh <- err
		}
		close(errCh)
	}()

	select {
	case err := <-errCh:
		return err
	case <-ctx.Done():
		// 关的时候给在跑的请求一点时间收尾，别让正在写文件的那一轮被打断
		shutdownCtx, cancel := context.WithTimeout(context.Background(), 5*time.Second)
		defer cancel()
		_ = srv.Shutdown(shutdownCtx)
		return nil
	}
}

// addrOrDefault 没给地址时听本机 8080。默认只到 localhost 之外也能连，
// 但这是个能读写磁盘的 agent，别默认对全网开放：见 displayAddr 的提示
func addrOrDefault(addr string) string {
	addr = strings.TrimSpace(addr)
	if addr == "" {
		return "127.0.0.1:8080"
	}
	return addr
}

// displayAddr 把监听的地址补成能点的样子；只听本机时如实说明，别让人以为外面连不上
func displayAddr(addr string) string {
	if strings.HasPrefix(addr, "0.0.0.0:") {
		return "127.0.0.1:" + strings.TrimPrefix(addr, "0.0.0.0:")
	}
	return addr
}

// loopbackOnly 判断这个监听地址是不是只有本机能连
func loopbackOnly(addr string) bool {
	host, _, err := net.SplitHostPort(addr)
	if err != nil {
		return false
	}
	if host == "" {
		return false
	}
	ip := net.ParseIP(host)
	return ip != nil && ip.IsLoopback()
}

// Manager 管着所有正在用的会话：谁在前端打开过就留在这儿，多个标签页打开同一条
// 会话时拿到的是同一个运行时，事件也就广播到每一页。
//
// 会话按工作区分开存：一个工作区就是一个工作目录（见 session.Workspace），
// 各自的会话目录挂在 <Root>/<工作区 id> 下。前端切工作区只是换一套列表和默认目录，
// 已经打开的会话照旧在 live 里挂着——不同工作区的会话可以同时跑。
type Manager struct {
	mu      sync.Mutex
	cfg     config.GlobalConfig
	cfgPath string
	// root 是工作区总目录，工作区的 store 都按它就现算
	root string
	// workdir 是启动时所在目录，也是默认工作区
	workdir string
	// spaces 是已经认识的工作区，key 是工作区 id
	spaces map[string]*workspace
	live   map[string]*Session
}

// workspace 是一个工作区在服务端的形态：它存会话的地方，加上它自己的目录
type workspace struct {
	Id    string
	Path  string
	Store *session.Store
}

func newManager(opt Options) *Manager {
	root := strings.TrimSpace(opt.Root)
	if root == "" {
		if r, err := session.DefaultRoot(); err == nil {
			root = r
		}
	}
	m := &Manager{
		cfg:     opt.Cfg,
		cfgPath: opt.CfgPath,
		root:    root,
		workdir: opt.WorkDir,
		spaces:  make(map[string]*workspace),
		live:    make(map[string]*Session),
	}
	// 启动目录永远算一个工作区，它的 store 由调用方准备好（老会话迁移在里面）
	m.register(opt.WorkDir, opt.Store)
	// 磁盘上别的工作区也一起登记：前端要能直接切过去，不用先"添加"
	m.discover()
	return m
}

// register 把一个工作区挂到内存里。store 为空时按 root 现算一个
func (m *Manager) register(path string, store *session.Store) *workspace {
	if strings.TrimSpace(path) == "" {
		return nil
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return nil
	}
	id, err := session.WorkspaceId(abs)
	if err != nil {
		return nil
	}
	if store == nil {
		if m.root == "" {
			return nil
		}
		store = session.NewStore(filepath.Join(m.root, id))
	}
	// 顺手刷新"最近打开时间"：列表按它排序，切过去的那次就是最近
	_ = store.SaveWorkspace(abs)

	ws := &workspace{Id: id, Path: abs, Store: store}
	m.mu.Lock()
	m.spaces[id] = ws
	m.mu.Unlock()
	return ws
}

// discover 把会话根目录下已有的工作区都认一遍，已知的不重复登记
func (m *Manager) discover() {
	if m.root == "" {
		return
	}
	infos, err := session.ListWorkspaces(m.root)
	if err != nil {
		return
	}
	for _, info := range infos {
		m.mu.Lock()
		_, known := m.spaces[info.Id]
		m.mu.Unlock()
		if known {
			continue
		}
		if info.Path != "" {
			m.register(info.Path, session.NewStore(filepath.Join(m.root, info.Id)))
			continue
		}
		// 目录在、但 workspace.json 丢了：当成一个路径未知的工作区留着，
		// 列表里能看见、能打开旧会话，只是没法在里面开新会话
		m.mu.Lock()
		m.spaces[info.Id] = &workspace{
			Id:    info.Id,
			Store: session.NewStore(filepath.Join(m.root, info.Id)),
		}
		m.mu.Unlock()
	}
}

// defaultSpace 是启动时那个工作区
func (m *Manager) defaultSpace() *workspace {
	id, err := session.WorkspaceId(m.workdir)
	if err != nil {
		return nil
	}
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.spaces[id]
}

// space 按 id 取工作区。内存里没有时（比如刚被别处建出来）按 root 现算一个
func (m *Manager) space(id string) (*workspace, bool) {
	m.mu.Lock()
	ws, ok := m.spaces[id]
	m.mu.Unlock()
	if ok {
		return ws, true
	}
	if m.root == "" || id == "" || id != filepath.Base(id) {
		return nil, false
	}
	dir := filepath.Join(m.root, id)
	info, err := session.LoadWorkspace(dir)
	if err != nil || info.Path == "" {
		return nil, false
	}
	if ws := m.register(info.Path, session.NewStore(dir)); ws != nil {
		return ws, true
	}
	return nil, false
}

// spacesSnapshot 拿一份工作区快照，锁外遍历用
func (m *Manager) spacesSnapshot() []*workspace {
	m.mu.Lock()
	defer m.mu.Unlock()
	out := make([]*workspace, 0, len(m.spaces))
	for _, ws := range m.spaces {
		out = append(out, ws)
	}
	return out
}

// Config 返回当前配置的快照。配置会被 /api/mode 之类的接口改，所以要读锁
func (m *Manager) Config() config.GlobalConfig {
	m.mu.Lock()
	defer m.mu.Unlock()
	return m.cfg
}

// open 拿到某条会话的运行时：内存里有就复用，没有就到各个工作区的目录里找。
// 会话 id 全局唯一，所以前端不用告诉我们它在哪个工作区。
// 找不到时返回错误，让前端去开新会话，而不是凭空造一个空会话盖掉磁盘上的
func (m *Manager) open(id string) (*Session, error) {
	m.mu.Lock()
	if s, ok := m.live[id]; ok {
		m.mu.Unlock()
		return s, nil
	}
	cfgMode := m.cfg.Mode
	m.mu.Unlock()

	spaces := m.spacesSnapshot()
	if len(spaces) == 0 {
		// 连默认工作区都没登记上：只能是 root 或启动目录算不出来，
		// 这时候老实报错，别让人以为会话丢了
		return nil, fmt.Errorf("还没有可用的工作区")
	}
	var lastErr error
	for _, ws := range spaces {
		data, err := ws.Store.Load(id)
		if err != nil {
			lastErr = err
			continue
		}
		m.mu.Lock()
		// 两个请求同时到、都发现内存里没有：后来的那个用先放进去的，别把订阅者丢了
		if existing, ok := m.live[id]; ok {
			m.mu.Unlock()
			return existing, nil
		}
		s := newSession(ws.Store, data, cfgMode)
		m.live[id] = s
		m.mu.Unlock()
		return s, nil
	}
	return nil, lastErr
}

// create 在默认工作区开一条新会话
func (m *Manager) create() *Session {
	return m.createIn(m.defaultSpace())
}

// createIn 在指定工作区开一条新会话。工作目录取这个工作区自己的目录，
// 所以不同工作区里的会话是在不同目录下干活的
func (m *Manager) createIn(ws *workspace) *Session {
	m.mu.Lock()
	defer m.mu.Unlock()
	if ws == nil || ws.Path == "" {
		return nil
	}
	data := domain.NewSession(ws.Path)
	s := newSession(ws.Store, data, m.cfg.Mode)
	m.live[data.Id] = s
	return s
}

// remove 把会话从内存里摘掉（前端删掉时用）。磁盘上的文件不动：
// /resume 那边还看得见，这里只是不再挂着一份
func (m *Manager) remove(id string) {
	m.mu.Lock()
	delete(m.live, id)
	m.mu.Unlock()
}

// setMode 换工作模式：写回配置文件，并把已经打开的会话一起切过去。
// 模式和模型一样是全局的，不能有的标签页在只读、有的在完全授权
func (m *Manager) setMode(mode domain.Mode) error {
	if err := config.SaveMode(m.cfgPath, mode.String()); err != nil {
		return err
	}
	m.mu.Lock()
	m.cfg.Mode = mode.String()
	sessions := make([]*Session, 0, len(m.live))
	for _, s := range m.live {
		sessions = append(sessions, s)
	}
	m.mu.Unlock()

	for _, s := range sessions {
		s.mu.Lock()
		s.mode = mode.String()
		s.mu.Unlock()
	}
	return nil
}

// setQuiet 切"精简显示"：写回配置文件，内存里也跟着换。
// 和模式一样是全局的——显示方式不该有的标签页精简、有的详细
func (m *Manager) setQuiet(quiet bool) error {
	if err := config.SaveQuiet(m.cfgPath, quiet); err != nil {
		return err
	}
	m.mu.Lock()
	m.cfg.Quiet = &quiet
	m.mu.Unlock()
	return nil
}

// sessionInfo 是会话列表里的一项
type sessionInfo struct {
	Id      string `json:"id"`
	Title   string `json:"title,omitempty"`
	Size    int64  `json:"size"`
	ModTime string `json:"mod_time,omitempty"`
	// Running 说明这条会话这会儿有人在跑。列表里标出来，免得点进去才发现在忙
	Running bool `json:"running"`
	// Status 是 running / waiting / done / idle：列表上的圆点按它上色，
	// running 转圈、waiting 黄、done 绿、idle 灰
	Status string `json:"status"`
}

// list 列出默认工作区存过的会话
func (m *Manager) list() ([]sessionInfo, error) {
	return m.listIn(m.defaultSpace())
}

// listIn 列出某个工作区存过的会话，最近改过的排最前
func (m *Manager) listIn(ws *workspace) ([]sessionInfo, error) {
	if ws == nil {
		return nil, nil
	}
	infos, err := ws.Store.ListInfo()
	if err != nil {
		return nil, err
	}
	m.mu.Lock()
	live := make(map[string]*Session, len(m.live))
	for id, s := range m.live {
		live[id] = s
	}
	m.mu.Unlock()

	out := make([]sessionInfo, 0, len(infos))
	for _, info := range infos {
		item := sessionInfo{Id: info.Id, Title: info.Title, Size: info.Size, Status: statusIdle}
		if !info.ModTime.IsZero() {
			item.ModTime = info.ModTime.Format("2006-01-02 15:04")
		}
		if s, ok := live[info.Id]; ok {
			item.Running = s.Running()
			item.Status = s.Status()
		} else if item.Size > 0 {
			// 内存里没挂着的会话：磁盘上有内容，说明以前聊过，算"完成"
			item.Status = statusDone
		}
		out = append(out, item)
	}
	return out, nil
}

// workspaceInfo 是工作区列表里的一项，前端拿它画下拉菜单
type workspaceInfo struct {
	Id   string `json:"id"`
	Path string `json:"path,omitempty"`
	// Name 是给人看的名字：取目录名，没有路径的（老目录）就用 id
	Name         string `json:"name"`
	LastOpenedAt string `json:"last_opened_at,omitempty"`
	// Default 标出启动时所在的那个工作区，前端第一次打开时先选它
	Default bool `json:"default"`
}

// workspaceInfoOf 把内部的工作区结构翻成给前端的那份
func workspaceInfoOf(ws *workspace, defId string) workspaceInfo {
	name := filepath.Base(ws.Path)
	if ws.Path == "" || name == "." || name == string(filepath.Separator) {
		name = ws.Id
	}
	return workspaceInfo{Id: ws.Id, Path: ws.Path, Name: name, Default: ws.Id == defId}
}

// workspaces 列出所有认识的工作区，最近打开过的排最前。
// 打开时间存在磁盘上的 workspace.json 里，内存这份只是缓存
func (m *Manager) workspaces() []workspaceInfo {
	def := m.defaultSpace()
	defId := ""
	if def != nil {
		defId = def.Id
	}
	stamps := make(map[string]time.Time)
	if m.root != "" {
		if infos, err := session.ListWorkspaces(m.root); err == nil {
			for _, info := range infos {
				stamps[info.Id] = info.LastOpenedAt
			}
		}
	}
	spaces := m.spacesSnapshot()
	out := make([]workspaceInfo, 0, len(spaces))
	for _, ws := range spaces {
		item := workspaceInfoOf(ws, defId)
		if at := stamps[ws.Id]; !at.IsZero() {
			item.LastOpenedAt = at.Format("2006-01-02 15:04")
		}
		out = append(out, item)
	}
	sort.Slice(out, func(i, j int) bool {
		if !stamps[out[i].Id].Equal(stamps[out[j].Id]) {
			return stamps[out[i].Id].After(stamps[out[j].Id])
		}
		return out[i].Name < out[j].Name
	})
	return out
}

// addWorkspace 把一个已有目录登记成新工作区。
// 只接已经存在的目录：打错一个字母就建一堆空会话目录，比报错难收拾得多
func (m *Manager) addWorkspace(path string) (workspaceInfo, error) {
	path = strings.TrimSpace(path)
	if path == "" {
		return workspaceInfo{}, errors.New("要填一个目录路径")
	}
	abs, err := filepath.Abs(path)
	if err != nil {
		return workspaceInfo{}, fmt.Errorf("解析目录 %s 失败: %w", path, err)
	}
	fi, err := os.Stat(abs)
	if err != nil {
		return workspaceInfo{}, fmt.Errorf("打不开 %s: %w", abs, err)
	}
	if !fi.IsDir() {
		return workspaceInfo{}, fmt.Errorf("%s 不是目录", abs)
	}
	if m.root == "" {
		return workspaceInfo{}, errors.New("算不出会话根目录，加不了工作区")
	}
	ws := m.register(abs, nil)
	if ws == nil {
		return workspaceInfo{}, fmt.Errorf("登记工作区 %s 失败", abs)
	}
	def := m.defaultSpace()
	defId := ""
	if def != nil {
		defId = def.Id
	}
	return workspaceInfoOf(ws, defId), nil
}

// meta 是页面顶部那一行要的东西：用哪套模型、什么思考强度、什么模式、还剩多少窗口
type meta struct {
	Model     string `json:"model"`
	ModelType string `json:"model_type,omitempty"`
	Effort    string `json:"effort"`
	// Quiet 是精简显示：只显示正文，思考过程和工具输出的内容都不显示。
	// 和终端界面用的是同一个配置项（config.GlobalConfig.QuietMode），
	// 没配过时默认就是开着
	Quiet        bool   `json:"quiet"`
	Mode         string `json:"mode"`
	ModeLabel    string `json:"mode_label"`
	WorkDir      string `json:"work_dir"`
	Workspace    string `json:"workspace"`
	ContextLimit int    `json:"context_limit"`
}

func (m *Manager) meta() meta {
	cfg := m.Config()
	mc, err := cfg.CurrentModel()
	if err != nil {
		mc = config.ModelConfig{}
	}
	mode := parseMode(cfg.Mode)
	label := mc.Label()
	if label == "" {
		label = "未配置"
	}
	def := m.defaultSpace()
	workdir, wsId := m.workdir, ""
	if def != nil {
		wsId = def.Id
		if def.Path != "" {
			workdir = def.Path
		}
	}
	return meta{
		Model:        label,
		ModelType:    strings.TrimSpace(mc.Type),
		Effort:       config.EffortLabel(mc.ReasoningEffort),
		Quiet:        cfg.QuietMode(),
		Mode:         mode.String(),
		ModeLabel:    mode.Label(),
		WorkDir:      workdir,
		Workspace:    wsId,
		ContextLimit: mc.Limit(),
	}
}

// routes 是全部接口。路径里带 {id} 的用 Go 1.22 起支持的方法+通配写法
func (m *Manager) routes() http.Handler {
	mux := http.NewServeMux()
	mux.Handle("GET /api/meta", m.handleMeta())
	mux.Handle("POST /api/mode", m.handleSetMode())
	// 显示方式（精简 / 详细）：全局开关，不需要会话 id —— 顶栏徽标在没开会话时也能点
	mux.Handle("POST /api/quiet", m.handleSetQuiet())
	// 命令表 + 技能名：前端打 / 时的补全候选就从这儿来
	mux.Handle("GET /api/commands", m.handleCommands())
	// /config 表单：读当前这套模型、保存、从接口拉模型列表
	mux.Handle("GET /api/config", m.handleGetConfig())
	mux.Handle("POST /api/config", m.handleSaveConfig())
	mux.Handle("GET /api/config/models", m.handleListModels())
	mux.Handle("DELETE /api/config/models", m.handleDeleteConfigModel())
	mux.Handle("GET /api/workspaces", m.handleWorkspaces())
	mux.Handle("POST /api/workspaces", m.handleAddWorkspace())
	// 删工作区：不带 purge 只是从列表里摘掉（目录搬去 .trash），purge=1 连文件一起删
	mux.Handle("DELETE /api/workspaces/{id}", m.handleDeleteWorkspace())
	mux.Handle("GET /api/fs/list", m.handleListDirs())
	mux.Handle("POST /api/fs/mkdir", m.handleMkdir())
	mux.Handle("GET /api/sessions", m.handleList())
	mux.Handle("POST /api/sessions", m.handleCreate())
	mux.Handle("GET /api/sessions/{id}", m.handleSnapshot())
	// 待办清单和后台任务：界面上常驻显示，CLI 那边是输入框上方那块
	mux.Handle("GET /api/sessions/{id}/runtime", m.handleRuntime())
	mux.Handle("DELETE /api/sessions/{id}", m.handleDelete())
	mux.Handle("GET /api/sessions/{id}/events", m.handleEvents())
	mux.Handle("POST /api/sessions/{id}/messages", m.handleSend())
	mux.Handle("POST /api/sessions/{id}/command", m.handleCommand())
	mux.Handle("POST /api/sessions/{id}/approve", m.handleApprove())
	mux.Handle("POST /api/sessions/{id}/cancel", m.handleCancel())
	mux.Handle("GET /", m.handleStatic())
	return mux
}

// handleStatic 送页面。前端是打包进来的，没有构建步骤，
// 所以这里就是"找得到就发，找不到回 index.html"
func (m *Manager) handleStatic() http.Handler {
	sub, err := fs.Sub(staticFS, "static")
	if err != nil {
		// embed 的路径是编译期定的，走不到这儿
		return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
			http.Error(w, err.Error(), http.StatusInternalServerError)
		})
	}
	files := http.FileServer(http.FS(sub))
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		// 页面改完不用手动刷缓存：这类小工具前端缓存一份旧的只会添乱
		w.Header().Set("Cache-Control", "no-store")
		files.ServeHTTP(w, r)
	})
}

// writeJSON 统一回一份 JSON。出错就写 500，反正是服务端自己的问题
func writeJSON(w http.ResponseWriter, code int, v any) {
	w.Header().Set("Content-Type", "application/json; charset=utf-8")
	w.WriteHeader(code)
	_ = json.NewEncoder(w).Encode(v)
}

// fail 回一条带原因的失败，前端直接把 message 摆给人看
func fail(w http.ResponseWriter, code int, format string, args ...any) {
	writeJSON(w, code, map[string]string{"error": fmt.Sprintf(format, args...)})
}

// decodeBody 读 JSON 请求体，顺手挡住过大的请求
func decodeBody(w http.ResponseWriter, r *http.Request, v any) error {
	body := http.MaxBytesReader(w, r.Body, maxBodyBytes)
	dec := json.NewDecoder(body)
	dec.DisallowUnknownFields()
	if err := dec.Decode(v); err != nil {
		return fmt.Errorf("请求体不是合法的 JSON: %w", err)
	}
	return nil
}

// sessionFrom 从路径里取会话 id 并拿到运行时，失败时已经把响应写好了
func (m *Manager) sessionFrom(w http.ResponseWriter, r *http.Request) (*Session, bool) {
	id := r.PathValue("id")
	if strings.TrimSpace(id) == "" {
		fail(w, http.StatusBadRequest, "缺少会话 id")
		return nil, false
	}
	s, err := m.open(id)
	if err != nil {
		fail(w, http.StatusNotFound, "找不到会话 %s: %v", id, err)
		return nil, false
	}
	return s, true
}

func (m *Manager) handleMeta() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, m.meta())
	})
}

func (m *Manager) handleSetMode() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Mode string `json:"mode"`
		}
		if err := decodeBody(w, r, &req); err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		mode, err := domain.ParseMode(req.Mode)
		if err != nil {
			fail(w, http.StatusBadRequest, "认不出这个模式: %v", err)
			return
		}
		if err := m.setMode(mode); err != nil {
			fail(w, http.StatusInternalServerError, "写回配置失败: %v", err)
			return
		}
		writeJSON(w, http.StatusOK, m.meta())
	})
}

// handleSetQuiet 切"精简显示"。和 /api/mode 一样是全局开关（配置里的 Quiet），
// 不需要会话 id：顶栏那枚徽标在还没开会话时也该能点
func (m *Manager) handleSetQuiet() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Quiet bool `json:"quiet"`
		}
		if err := decodeBody(w, r, &req); err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		if err := m.setQuiet(req.Quiet); err != nil {
			fail(w, http.StatusInternalServerError, "写回配置失败: %v", err)
			return
		}
		writeJSON(w, http.StatusOK, m.meta())
	})
}

// workspaceFrom 从查询参数 ws 里取工作区。没带就用默认工作区，
// 带了但认不出时返回 false 并回 404——不同工作区的会话不该串在一起
func (m *Manager) workspaceFrom(w http.ResponseWriter, r *http.Request) (*workspace, bool) {
	id := strings.TrimSpace(r.URL.Query().Get("ws"))
	if id == "" {
		ws := m.defaultSpace()
		if ws == nil {
			fail(w, http.StatusInternalServerError, "还没有可用的工作区")
			return nil, false
		}
		return ws, true
	}
	ws, ok := m.space(id)
	if !ok {
		fail(w, http.StatusNotFound, "找不到工作区 %s", id)
		return nil, false
	}
	return ws, true
}

func (m *Manager) handleWorkspaces() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		writeJSON(w, http.StatusOK, m.workspaces())
	})
}

func (m *Manager) handleAddWorkspace() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path string `json:"path"`
		}
		if err := decodeBody(w, r, &req); err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		info, err := m.addWorkspace(req.Path)
		if err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		writeJSON(w, http.StatusOK, info)
	})
}

// handleListDirs 是文件夹选择器的数据源：不带 path 列根（盘符），带了就列那个目录。
// 只回目录，前端点一项就往下走一层
func (m *Manager) handleListDirs() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		list, err := m.listDirs(r.URL.Query().Get("path"))
		if err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		writeJSON(w, http.StatusOK, list)
	})
}

// handleMkdir 在选择器里新建一个文件夹，省得为了建目录切到终端去。
// 只建一层，名字不许带路径分隔符（见 createDir）
func (m *Manager) handleMkdir() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req struct {
			Path string `json:"path"`
			Name string `json:"name"`
		}
		if err := decodeBody(w, r, &req); err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		created, err := createDir(req.Path, req.Name)
		if err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		writeJSON(w, http.StatusOK, map[string]string{"path": created})
	})
}

func (m *Manager) handleList() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		ws, ok := m.workspaceFrom(w, r)
		if !ok {
			return
		}
		infos, err := m.listIn(ws)
		if err != nil {
			fail(w, http.StatusInternalServerError, "%v", err)
			return
		}
		writeJSON(w, http.StatusOK, infos)
	})
}

// createRequest 是新建会话的请求体。不带 body 就在默认工作区开
type createRequest struct {
	Workspace string `json:"workspace"`
}

func (m *Manager) handleCreate() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		var req createRequest
		// 老前端（和测试）发的是空 body，那就在默认工作区开；
		// ContentLength 为 -1 时是分块上传，照样要解析
		if r.ContentLength != 0 {
			if err := decodeBody(w, r, &req); err != nil {
				fail(w, http.StatusBadRequest, "%v", err)
				return
			}
		}
		ws := m.defaultSpace()
		if id := strings.TrimSpace(req.Workspace); id != "" {
			var ok bool
			if ws, ok = m.space(id); !ok {
				fail(w, http.StatusNotFound, "找不到工作区 %s", id)
				return
			}
		}
		if ws == nil {
			fail(w, http.StatusInternalServerError, "还没有可用的工作区")
			return
		}
		s := m.createIn(ws)
		if s == nil {
			fail(w, http.StatusInternalServerError, "工作区 %s 没有可用的目录，开不了新会话", ws.Id)
			return
		}
		// 新建的会话也落一次盘：不然它在列表里不出现，刷新页面就找不着了
		if err := ws.Store.Save(s.Data()); err != nil {
			fail(w, http.StatusInternalServerError, "保存新会话失败: %v", err)
			return
		}
		writeJSON(w, http.StatusOK, s.snapshot())
	})
}

func (m *Manager) handleSnapshot() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, ok := m.sessionFrom(w, r)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, s.snapshot())
	})
}

// handleDelete 删掉一条会话。默认只从内存里摘掉（磁盘文件留着，靠 ?purge=true 才真删）：
// 后端不知道前端那次"删"是不是误点，所以清理磁盘这件事得前端明确要求
func (m *Manager) handleDelete() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		s, ok := m.sessionFrom(w, r)
		if !ok {
			return
		}
		if s.Running() {
			fail(w, http.StatusConflict, "这条会话还在跑，先中断再删")
			return
		}
		purge := truthy(r.URL.Query().Get("purge"))
		m.remove(id)
		if purge {
			if _, err := s.store.Delete(id); err != nil {
				fail(w, http.StatusInternalServerError, "%v", err)
				return
			}
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// truthy 认查询参数里表示"是"的写法
func truthy(v string) bool {
	switch strings.ToLower(strings.TrimSpace(v)) {
	case "1", "true", "yes", "on":
		return true
	}
	return false
}

// handleDeleteWorkspace 把一个工作区从列表里摘掉。
//
// purge=1 连会话目录一起删（里面的会话文件就真没了）；不带 purge 时只把目录搬到
// <root>/.trash/<id>-<时间>/ —— 列表里看不见了，会话文件还在，想找回来手工搬回去就行。
// 两种情况都只动 <root>/<id>，绝不碰工作目录本身
func (m *Manager) handleDeleteWorkspace() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		id := r.PathValue("id")
		if strings.TrimSpace(id) == "" {
			fail(w, http.StatusBadRequest, "缺少工作区 id")
			return
		}
		ws, ok := m.space(id)
		if !ok {
			fail(w, http.StatusNotFound, "找不到工作区 %s", id)
			return
		}
		// 有人在这条工作区的会话里跑着就别动它：那一轮还在往目录里写文件
		if busy := m.runningIn(ws); len(busy) > 0 {
			fail(w, http.StatusConflict, "这条工作区里还有会话在跑（%s），先中断再删",
				strings.Join(busy, "、"))
			return
		}
		purge := truthy(r.URL.Query().Get("purge"))
		path, err := session.RemoveWorkspace(m.root, ws.Id, purge)
		if err != nil {
			fail(w, http.StatusInternalServerError, "%v", err)
			return
		}
		// 内存里也摘掉：不摘的话下拉里还在，点进去能开新会话
		m.forget(ws.Id)
		writeJSON(w, http.StatusOK, map[string]any{
			"id":     ws.Id,
			"path":   ws.Path,
			"purged": purge,
			// dest 是实际动过的目录：purge 时是删掉的、否则是搬去回收站的位置
			"dest": path,
		})
	})
}

// runningIn 列出这条工作区里正在跑的会话 id
func (m *Manager) runningIn(ws *workspace) []string {
	if ws == nil {
		return nil
	}
	m.mu.Lock()
	live := make([]*Session, 0, len(m.live))
	for _, s := range m.live {
		live = append(live, s)
	}
	m.mu.Unlock()

	var busy []string
	for _, s := range live {
		if s.store == ws.Store && s.Running() {
			busy = append(busy, s.Data().Id)
		}
	}
	return busy
}

// forget 把一个工作区从内存里摘掉：它那些还挂着的会话也一起摘（磁盘文件不动）
func (m *Manager) forget(id string) {
	m.mu.Lock()
	defer m.mu.Unlock()
	ws, ok := m.spaces[id]
	if !ok {
		return
	}
	for sid, s := range m.live {
		if s.store == ws.Store {
			delete(m.live, sid)
		}
	}
	delete(m.spaces, id)
}

// runtimeView 是"输入框上方那块"要的东西：待办清单 + 后台任务。
// 这两样在 CLI 里是常驻显示的，浏览器这边按会话取一份自己的
type runtimeView struct {
	// Todos 是这条会话最近一次 TodoWrite 传上来的清单
	Todos []todoItem `json:"todos"`
	// TodoDone / TodoTotal 是给标题那句"2/5 完成"用的，前端不用自己数
	TodoDone  int `json:"todo_done"`
	TodoTotal int `json:"todo_total"`
	// Tasks 是这条会话起的后台任务（进程级任务表里按 id 过滤出来的那几个）
	Tasks []taskItem `json:"tasks"`
	// RunningTasks 是其中还在跑的数量，前端靠它决定要不要跟着刷新
	RunningTasks int `json:"running_tasks"`
	// Status 和列表上那个圆点一致，前端切会话/轮询时一并更新
	Status string `json:"status"`
}

type todoItem struct {
	Content string `json:"content"`
	Status  string `json:"status"`
}

type taskItem struct {
	ID      string `json:"id"`
	Status  string `json:"status"`
	Running bool   `json:"running"`
	Elapsed string `json:"elapsed"`
	Command string `json:"command"`
}

func (m *Manager) handleRuntime() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, ok := m.sessionFrom(w, r)
		if !ok {
			return
		}
		writeJSON(w, http.StatusOK, s.runtimeView())
	})
}

// runtimeView 攒这条会话的待办和后台任务
func (s *Session) runtimeView() runtimeView {
	todos := s.Todos()
	out := runtimeView{Status: s.Status(), Tasks: []taskItem{}}
	for _, item := range todos {
		out.Todos = append(out.Todos, todoItem{Content: item.Content, Status: item.Status})
		if item.Status == tool.TodoCompleted {
			out.TodoDone++
		}
	}
	out.TodoTotal = len(todos)

	// 后台任务注册表是进程级的：只挑这条会话起的那几个，别把别的会话的也列出来
	wanted := make(map[string]bool, len(s.TaskIds()))
	for _, id := range s.TaskIds() {
		wanted[id] = true
	}
	if len(wanted) == 0 {
		return out
	}
	for _, snap := range tool.TaskSnapshots() {
		if !wanted[snap.ID] {
			continue
		}
		if snap.Running {
			out.RunningTasks++
		}
		out.Tasks = append(out.Tasks, taskItem{
			ID:      snap.ID,
			Status:  snap.Status,
			Running: snap.Running,
			Elapsed: tool.FormatDuration(snap.Elapsed),
			Command: snap.Command,
		})
	}
	return out
}

// handleEvents 是 SSE：一条连接对应一个浏览器标签页，事件按到达顺序推过去。
// 断开时只摘掉自己那个订阅者，会话和这一轮都继续跑
func (m *Manager) handleEvents() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, ok := m.sessionFrom(w, r)
		if !ok {
			return
		}
		flusher, ok := w.(http.Flusher)
		if !ok {
			fail(w, http.StatusInternalServerError, "这个连接不支持流式推送")
			return
		}
		h := w.Header()
		h.Set("Content-Type", "text/event-stream; charset=utf-8")
		h.Set("Cache-Control", "no-store")
		// 关掉 nginx 之类的缓冲，否则事件会攒着一起到
		h.Set("X-Accel-Buffering", "no")
		w.WriteHeader(http.StatusOK)
		flusher.Flush()

		ch, unsubscribe := s.Subscribe()
		defer unsubscribe()

		// 断线重连时 EventSource 会带 Last-Event-ID，这里不做补发：
		// 前端重连后直接重拉一次 snapshot，比在服务端攒历史简单也可靠
		ping := time.NewTicker(sseHeartbeat)
		defer ping.Stop()
		for {
			select {
			case <-r.Context().Done():
				return
			case data, open := <-ch:
				if !open {
					return
				}
				if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
					return
				}
				flusher.Flush()
			case <-ping.C:
				// 心跳走正常事件而不是 ": ping" 注释：注释前端看不见，
				// 页面就没法区分"连着但没人说话"和"断了"（见 kindPing）
				data, err := json.Marshal(pingEvent())
				if err != nil {
					continue
				}
				if _, err := fmt.Fprintf(w, "data: %s\n\n", data); err != nil {
					return
				}
				flusher.Flush()
			}
		}
	})
}

// sendRequest 是发提问的请求体。图片和 TUI 里贴图走同一套校验：
// 类型按文件头认，太大的挡掉
type sendRequest struct {
	Prompt string `json:"prompt"`
	Images []struct {
		MediaType string `json:"media_type"`
		Data      string `json:"data"`
	} `json:"images"`
}

func (m *Manager) handleSend() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, ok := m.sessionFrom(w, r)
		if !ok {
			return
		}
		var req sendRequest
		if err := decodeBody(w, r, &req); err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		prompt := strings.TrimSpace(req.Prompt)
		images, err := decodeImages(req.Images)
		if err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		if prompt == "" && len(images) == 0 {
			fail(w, http.StatusBadRequest, "消息不能是空的")
			return
		}
		if prompt == "" {
			// 只贴了图没写字：和 TUI 一样替用户说一句，接口那边总得有段文字
			prompt = imageOnlyPrompt(len(images))
		}

		// "直接点名用某个技能"的写法：/技能名 [补充说明]。那份 SKILL.md 整段并进这一条
		// 提问，模型照着做，不用再读一遍文件——和终端界面是同一套规矩
		if skill, rest, ok := skillCall(prompt); ok {
			prompt = core.SkillPrompt(skill, rest)
		} else if name := command.FirstField(prompt); command.LooksLike(name) && !command.IsKnown(name) {
			// 形状像一条命令、却既不是内置命令也不是技能：说一句比闷头发给模型强。
			// /tmp/foo.log 这种带路径分隔符的照旧当普通提问
			fail(w, http.StatusBadRequest, "%s", unknownCommandText(name))
			return
		}

		switch err := s.Start(m.Config(), prompt, images); {
		case errors.Is(err, ErrBusy):
			fail(w, http.StatusConflict, "上一轮还在跑，等它结束再发")
			return
		case err != nil:
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

// skillCall 把 "/技能名 补充说明" 拆成技能和后面那截。认不出技能时 ok 为 false——
// 是命令打错了还是技能名不对，交给调用方去看
func skillCall(input string) (core.Skill, string, bool) {
	name := command.FirstField(input)
	if name == "" {
		return core.Skill{}, "", false
	}
	skill, ok := core.FindSkill(core.Skills(), name)
	if !ok {
		return core.Skill{}, "", false
	}
	rest := strings.TrimSpace(strings.TrimPrefix(strings.TrimSpace(input), name))
	return skill, rest, true
}

// imageOnlyPrompt 是只贴图时替用户说的那句话
func imageOnlyPrompt(n int) string {
	if n <= 1 {
		return "看看这张图"
	}
	return fmt.Sprintf("看看这 %d 张图", n)
}

// decodeImages 把前端传上来的图片还原成 domain.Image。前端给的是 base64
// 加一个自报的 media_type，但类型和大小一律按内容重算——前端说是什么不算数
func decodeImages(raw []struct {
	MediaType string `json:"media_type"`
	Data      string `json:"data"`
}) ([]domain.Image, error) {
	if len(raw) == 0 {
		return nil, nil
	}
	out := make([]domain.Image, 0, len(raw))
	for i, item := range raw {
		data := strings.TrimSpace(item.Data)
		if data == "" {
			return nil, fmt.Errorf("第 %d 张图片没有内容", i+1)
		}
		// 有人会连 data:...;base64, 前缀一起发上来，剥掉
		if idx := strings.Index(data, ","); strings.HasPrefix(data, "data:") && idx > 0 {
			data = data[idx+1:]
		}
		buf, err := base64.StdEncoding.DecodeString(data)
		if err != nil {
			return nil, fmt.Errorf("第 %d 张图片不是合法的 base64: %w", i+1, err)
		}
		img, err := domain.NewImage(buf)
		if err != nil {
			return nil, fmt.Errorf("第 %d 张图片用不了: %w", i+1, err)
		}
		out = append(out, img)
	}
	return out, nil
}

// approveRequest 是回答一次确认。Feedback 只在拒绝时有意义，会原样转给模型
type approveRequest struct {
	ID       string `json:"id"`
	Allow    bool   `json:"allow"`
	Always   bool   `json:"always"`
	Feedback string `json:"feedback"`
}

func (m *Manager) handleApprove() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, ok := m.sessionFrom(w, r)
		if !ok {
			return
		}
		var req approveRequest
		if err := decodeBody(w, r, &req); err != nil {
			fail(w, http.StatusBadRequest, "%v", err)
			return
		}
		ans := domain.ApprovalAnswer{
			Allowed: req.Allow,
			// 拒绝时说"以后都允许"没有意义，忽略掉
			Always:   req.Allow && req.Always,
			Feedback: strings.TrimSpace(req.Feedback),
		}
		if !s.Answer(req.ID, ans) {
			// 已经答过、或者这一轮被中断了：不是错误，但得让前端把弹窗收起来
			fail(w, http.StatusConflict, "这项确认已经不需要回答了")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}

func (m *Manager) handleCancel() http.Handler {
	return http.HandlerFunc(func(w http.ResponseWriter, r *http.Request) {
		s, ok := m.sessionFrom(w, r)
		if !ok {
			return
		}
		if !s.Cancel() {
			fail(w, http.StatusConflict, "这一轮已经结束了")
			return
		}
		w.WriteHeader(http.StatusNoContent)
	})
}
