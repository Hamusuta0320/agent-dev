# 待办

当前状态：`go build` / `go vet` / `go test ./...` 全过。TUI、会话持久化与恢复、上下文压缩、
贴图提问、后台任务、审批 diff 预览、约定文件与技能、工作模式隔离、失败重试都已可用。

技能除了模型自己按需读，用户也能直接点名：`/技能名 [补充说明]` 会把那份 `SKILL.md`
整段并进这一条提问（`src/core/skills.go` 的 `SkillPrompt`），`/skills` 列出装了哪些；
输入 `/` 开头时技能和内置命令一起补全，认不出来的 `/xxx` 会提示一句而不是发给模型。
这套在终端界面和浏览器界面里是一样的：命令表只有一份（`src/command/`），
两个界面各自负责怎么把结果画出来。

下面按"代码里已经留了空目录"→"功能缺口"→"工程化"排。

## 一、规划了但还没写的目录

源码里已经有这几个空目录，是当初预留的位置，目前都还没有代码：

- [ ] `src/plugin/` —— 插件 / MCP 接入。现在加能力只能改代码重新编译。
- [ ] `src/web/` —— 联网能力。全项目搜不到一处 HTTP 请求，模型**完全没法查在线文档**。
      至少要有 WebFetch（抓一个 URL 转纯文本）和 WebSearch。
      （浏览器界面不在这儿，在 `src/webui/`，见下面"浏览器界面"。）
- [ ] `src/prompt/` —— 提示词模板。提示词目前硬编码在 `src/core/prompt.go`，
      想调措辞得改代码；独立出来还能做多套模板，比如给子任务用的精简版。
- [ ] `src/log/` —— 结构化日志。现在只有界面展示和 `core.ConsoleEmit` 的纯文本输出，
      跑完之后出了问题没法回溯。
- [ ] `src/model/` —— 接非 OpenAI 兼容的模型（比如 Anthropic 的 `/v1/messages`）。
      配置结构已经换成 `config.ModelConfig`（`DeepSeekConfig` 只是老名字的别名），
      造 provider 的入口也收敛到 `provider.New`，缺的是新协议本身的实现。
      剩下的多模型工作不需要这个目录也能做，先空着。

## 二、功能缺口

### 浏览器界面

- [x] `agent-dev --web [地址]` 起一个浏览器界面（`src/webui/`），不带参数还是原来的 TUI。
      同一个二进制、同一条 `core.Loop`：core 只认 `domain.EmitFunc`，所以这一层没改 core 一行。
      `go:embed` 把页面打包进可执行文件，没有 node 工具链、没有构建步骤。
      - 样式用 Bootstrap 5（`static/vendor/`，整份下载放进仓库，**不引 CDN**）：
        内网、断网机器上照样能打开。主题变量在 `style.css` 里覆盖成项目原来的深底配色，
        按钮、下拉、进度条、徽标这些直接用 Bootstrap 的类，不再自己造。
      - 事件走 SSE（`/api/sessions/{id}/events`），流式思考/回答、工具调用的状态与耗时、
        上下文用量都实时推；断线由 EventSource 自己重连，重连后前端重拉一次 snapshot 补齐。
      - 顶部常驻**连接状态**：已连接 / 重连中 / 已断开。判断依据是 `EventSource.readyState`
        加上服务端心跳（`kind:"ping"`，25 秒一次）：光看 readyState 识别不出"连着但收不到东西"
        的半死连接，所以前端每 5 秒检查一次最后收到消息的时间，超过 70 秒就自己重建连接。
      - **工作区**（`session.Workspace`）：一个工作区就是一个工作目录，会话按
        `<用户目录>/.agent-dev/sessions/<工作区 id>/` 分开存。侧栏左上角能切换、
        也能填一个已有目录把它加成新工作区（`GET/POST /api/workspaces`）。
        接口按 `?ws=` 取列表、建会话时带 `workspace`，各工作区的会话互不串门；
        切工作区只换列表和默认目录，已经开着的会话照旧在服务端跑着。
      - 确认框（`EventApproval`）搬到了页面上：允许一次 / 本回合都允许 / 拒绝并说明，
        拒绝时写的话照样转给模型。回答按 id 指认，多个工具同时等确认也不会串。
        用的是原生 `<dialog>`，Esc 不当作拒绝，免得手滑。
      - 会话列表、切换、新建、附图（选文件或直接粘贴截图）、排队（运行中再发就排着，
        这一轮正常结束自动接上）都有。
      - 默认只听 `127.0.0.1:8080`：这个界面**没有登录验证**，谁连上谁就能让 agent 读写本机文件。
        手动指定 `0.0.0.0:8080` 时启动会打一句警告。
      - 运行中拉快照走的是磁盘上那份（`store.Load`）：内存里那份 core 正在追加，
        读它会撞上并发写。第一轮开始时会把提问先落一次盘，所以刷新页面看得见本次提问。
- [x] **本地命令搬到了浏览器**：命令表在 `src/command/`，两个界面共用一份（终端按它生成帮助和补全，
      浏览器按它做候选和分派），加命令只改那儿一处。
      - 输入框里打 `/` 浮出候选（内置命令 + 装好的技能名，`GET /api/commands` 拉一次，
        之后本地做前缀匹配）：↑↓ 选、Tab 或 Enter 补全、Esc 收起；一打空格就该写参数了，
        候选自动让位。打全了不再提示。
      - 打全的命令交给 `POST /api/sessions/{id}/command` 执行，回包是个 `{kind, text, session, meta}`：
        `text` 画成等宽的一段贴进对话，`session` 表示该切到那条会话（`/new`、`/resume`），
        `meta` 表示配置变了、顶栏跟着刷新（`/mode`、`/model`、`/effort`）。
      - 支持 `/help /new /resume /session /mode /model /effort /skills /todo /quiet /config /exit`；
        `/config` 不开文本，直接弹表单。`/quiet`、`/exit` 是终端专有的，这里回一句说明。
      - `/技能名 补充说明` 不算命令：服务端把那份 `SKILL.md` 整段并进提问（`core.SkillPrompt`）。
        形状像命令却既不是命令也不是技能的（`/zzz`）挡住并提示相近的，不发给模型；
        `/tmp/foo.log` 这种带路径分隔符的照旧当普通提问。
- [x] **`/config` 表单**（`#cfg-dialog`）：类型 / 接口地址 / 密钥 / 模型名 / 上下文上限 / 思考强度。
      值从 `GET /api/config` 取，保存走 `POST /api/config`。
      - **密钥不回传明文**：只告诉前端"配过了没有"（`api_key_set`）。提交时留空就是"不改"，
        所以换模型名、改地址都不用把密钥再抄一遍。
      - 切类型时地址跟着换成该类型的内置地址（手填过的地址不动），和终端界面一致。
      - 地址密钥填好后「拉列表」按 `GET /api/config/models?url=&key=` 现拉 `/models`，
        模型名点一下就填进去；key 留空时用配置里已有的那个。
      - 保存改的是当前生效的那套（`config.ReplaceModel` + `SaveModels`），内存里的配置同步换掉，
        下一轮请求就用新的，不用重启；顶栏的模型 / 思考强度徽标点开也是这个表单。
- [ ] 顶栏没有会话标题的重命名入口：标题还是第一轮聊完模型自己起的。
- [x] **排队消息和后台任务不再挤成一片**：两个原因叠在一起。
      - 排队块以前单独夹在对话区和输入区之间，样式是 Bootstrap `alert`（全宽、无圆角、
        只有一条顶边虚线），紧贴着输入区的 `border-top`；而输入区内部第一块就是无背景的
        后台任务小字 —— 两块挨着只隔一条 1px 线，看着像叠住了。
        现在排队块也搬进输入区容器，三块按 **待办 → 后台任务 → 排队 → 输入框** 自上而下排，
        各自是带圆角、带底色的小卡片（排队黄底、待办/后台任务中性底），一眼分得清。
      - 更硬的问题：`.chat` 是 flex item，默认 `min-height: auto` = 不肯比内容矮。
        对话一长它就把底下的排队块和输入区顶出屏幕，两块挤在一起才真的像"重叠"。
        给它补上 `min-height: 0`，现在它自己滚，底部那几块永远留在原位；
        三块各自限高（排队 180px、待办/后台任务 120px，内部滚），矮屏上也不会把对话挤没。
- [x] **精简显示（quiet）在浏览器里也生效，并且默认就是开着的**：
      用的还是终端那个配置项（`config.GlobalConfig.Quiet`，没配过时 `QuietMode()` 返回 true），
      所以两边看的是同一个开关，改哪边都算数。
      - 开着（默认）：只显示正文——思考过程整段不摆（连折叠块都不建），工具调用只留一行标题
        （名字、意图、状态还在）；和终端 `/quiet` 开着时的规矩一致。
      - 关掉：思考变成折叠块、工具输出点开能看（默认仍折叠，不像终端那样全打出来）。
      - 顶栏多了一枚「精简 / 详细」徽标，点一下切；`/quiet`（可带 on/off）也真的生效了
        （以前是个桩，只会回一句"这一项不用切"），改完重画历史。
      - `POST /api/quiet` 是全局开关，不需要会话 id（和 `/api/mode` 一个路子），
        顶栏徽标在还没开会话时也能点；`meta.quiet` 带出当前值。
- [x] **删除会话不再凭空建新会话**：以前删掉当前会话后会走 `newSession()`"别让界面空着"，
      而 `newSession()` 是 `POST /api/sessions`，**真的在磁盘上建一条并落盘**；新会话随即成了当前那条，
      于是再删又多一条——列表里攒一堆没人用过的空会话。
      现在删完只做两件事：列表里还有会话就打开最近那条；一条都不剩就停在空状态
      （提示点「＋ 新会话」，或者直接写也行）。
      空状态下真要发东西时才按需建会话（`ensureSession`），所以不写就不建。
      顺带修了切工作区：以前"上次打开的那条不在"就凭空开新的，现在优先打开这个工作区最近的一条。
- [x] **会话与工作区都能删**：
      - 会话：hover 时右边露出 ✕，确认后 `DELETE /api/sessions/{id}?purge=1`
        连磁盘文件一起删（不带 purge 只摘内存，给"关标签页"留了条不删文件的路）。
        删的是当前这条时先断订阅、清草稿，删完手上没会话就自动开一条新的。
      - 工作区：下拉里每项一个 ✕，确认框给两种删法：
        **仅移除展示** → `DELETE /api/workspaces/{id}`，会话目录搬到 `<root>/.trash/<id>-<时间>/`，
        列表里不再出现、文件还在，想找回来手工搬回 `<root>/<id>` 就行；
        **连同文件删除** → 加 `?purge=1`，会话目录整个删掉。
        两种都只动 `<root>/<id>` 这一层，绝不碰工作目录里的代码；
        有会话在这个工作区里跑着时挡掉（那一轮还在往目录里写）。
        `ListWorkspaces` 现在跳过点开头的目录，`.trash` 自己不会被当成工作区。
- [x] **会话列表的状态圆点**：`GET /api/sessions` 每条带 `status`。
      `running` 转圈、`waiting` 黄（等你介入：有确认要答，或上一轮出错/被中断）、
      `done` 绿、`idle` 灰（还没聊过）。判定在 `Session.Status()`：
      在跑 > 有确认挂着 > 上一轮的结果（`outcome` 在 `Start`/收尾两处写）。
      `started`、`approval`、`done`、`error`、`cancelled` 几个事件都会顺手重拉列表。
- [x] **待办清单与后台任务栏**（`GET /api/sessions/{id}/runtime`）：
      CLI 上这两样常驻在输入框上方，浏览器这边也补上了，前端每 2 秒轮询一次（页面在后台时跳过）。
      - 待办：`TodoWrite` 每次传的是整份清单，`Session.emit` 顺手存一份**按会话**的副本
        （`tool.CurrentTodos()` 是进程级的，多开几个会话会串）。
      - 后台任务：`Bash background=true` 的结果里带 `task_id=task_N`，
        从结果里认出来记到该会话名下，再拿 `tool.TaskSnapshots()` 按 id 过滤。
        后台任务表本身仍是进程级的（`src/tool/task.go`），这里只做归属过滤。
- [x] **输入框、附图、队列按会话隔离**：以前只有一套 DOM、一份 `state.images/queue`，
      在 A 里写一半切到 B 会看见 A 的字，更糟的是在 B 里回车会把 A 的草稿发出去。
      现在 `state.drafts`（Map，key 是会话 id）存 `{text, images, queue}`，
      切会话 / 切工作区时存旧的、取新的；打字、附图、排队变化都顺手存一份。
- [x] **排队可编辑**：每条队列消息带「取回」（回填输入框并从队列拿走，`takeBackQueued`）
      和「删除」，对应 CLI 的 Alt+E / Del。Esc 清空附图也补上了。
- [x] **工具调用前的图标换了**：以前是实心圆点 `●` / `◐`，一行行堆起来满屏噪点；
      现在是成功一个细勾、失败一个叉、进行中一个细圆环转圈（纯 CSS 画的，`.tool-icon`）。
- [x] **模型输出用 markdown 排版**（`static/markdown.js`）：
      自己写的一个小渲染器，不引外部库 —— 现成的 markdown 库基本都吐 HTML 字符串，
      上屏就得 `innerHTML`，那等于把模型输出（和它读到的仓库文件）当 HTML 执行。
      这里全程 `createElement` / `textContent`，**一个 `innerHTML` 都没有**
      （`TestStaticScriptsNeverUseInnerHTML` 盯着这条，谁写了测试就红）。
      - 支持：围栏代码块（语言标签 + 复制按钮）、标题、列表（可嵌套）、表格（含对齐）、
        引用、水平线、粗体/斜体/删除线/行内代码/链接/裸链接、反斜杠转义、软换行保留成 `<br>`。
      - 链接只放行 `http/https/mailto` 和页内锚点，`javascript:` 之类原样当文字，不给点。
      - **流式**：模型是一小块一小块吐字的，每来一条就整段重新解析，回答一长就卡。
        所以攒 `streamText`，按 `STREAM_RENDER_MS`（90ms）节流重排，收尾时补最后一拍。
        流式光标（▌）从容器的 `::after` 换成真元素，塞进最后一个块里，不会掉到下一行。
      - 精简显示（quiet）只管"思考过程和工具输出显不显示"，正文照样按 markdown 排。
      - 工具输出、命令结果、思考过程仍然是等宽纯文本：那些是机器输出，排版反而添乱。
- [ ] 没有 token 级的重放能力：断线期间的事件不补发，靠重拉历史对齐。

### 无界面 / 单次执行模式

- [ ] `main()`（`src/cli/cli.go`）现在只认 `--web`，还没有单次执行。
      加 `-p/--print "把活干了"` 和"从 stdin 读提问"，就能塞进脚本、git hook、CI。
      `core.Loop` 本来就能脱离 TUI 跑（`ConsoleEmit` 就是为此准备的），成本不高、收益最大。

### 回滚

- [ ] 没有 `/undo`。`src/tool/write.go` 写之前会把原件备份到 `.agent/backups/`，
      但没有任何入口把本回合改过的文件退回去，只能进目录里手工翻。

### 多模型

- [x] 配置可以写多套模型（`config.yaml` 的 `Models` 列表 + `Current` 指定默认用哪套），
      `/model` 列出并切换，切换会写回配置；`/config` 改的是当前这套。
      老配置只写 `Provider` 一段照常能跑，保存时会把 Models / Current / Provider 一起写齐。
      新增 `provider.New(config.ModelConfig)`，Type 认 DeepSeek / OpenAI（都是 OpenAI 兼容接口）。
- [x] `/model add` 新增一套、`/model del 名字` 删掉一套，不用手改 `config.yaml` 了。
      `/config` 表单多一格「名字」：新增时留空就用模型名，编辑时留空表示不改名；改了就是给这套改名
      （原地换，不会多出一套）。新增存完不自动切过去，当前那套不动。
      config 层是三个明确动作 `EditModel` / `AddModel` / `DeleteModel`（替掉了靠名字猜的
      `ReplaceModel`），终端和浏览器两边共用。删除要二次确认：终端是再打一遍同样的命令，
      浏览器走确认框；只剩一套时两边都删不掉。
- [ ] 还差一家：Anthropic 那套 `/v1/messages` 协议和 OpenAI 兼容接口不一样，得单独实现一个
      provider 并在 `provider.New` 里分派。
- [x] 思考强度（`reasoning_effort`）：`ModelConfig.ReasoningEffort` 存档位
      （minimal / low / medium / high，留空表示不发这个参数），`/config` 里用 ←→ 切，
      也可以直接打命令：`/effort` 看当前这档，`/effort high`、`/effort 默认` 就地改并写回配置。
      请求体由 `src/provider/deepseek.go` 拼。写错档位在造 provider 时就报错，
      不至于让接口回一个 400 让人猜。各家认不认这个字段不一样，所以默认不动它。

### 待办跟踪

- [x] `TodoWrite` 工具（`src/tool/todo.go`）：整份传上来整份替换，状态只有 pending /
      in_progress / completed 三种。清单存在内存里，输入框上方常驻一行进度，`/todo` 看完整清单，
      `/new`、`/resume` 时清空。系统提示词里写清了什么时候该开清单、任何时候最多一条进行中。
      注意 `Task` 工具（`src/tool/task.go`）是后台 bash 进程，不是子 agent，别混。
- [ ] 清单没有跟着会话存盘：现在退出就没了。要留的话得进 `domain.Session`。

### 会话管理

- [x] `/resume <序号|id>` 切过去之后把这段历史重新打进历史区（`src/cli/ui.go` 的 `historyLines`）：
      提问、思考、回答、工具调用和结果按原来的顺序重放，和当时聊出来的样子一致。会话文件里
      没存工具状态和耗时，圆圈按结果开头认（core 失败固定写"错误: / 执行失败: "），耗时就不显示；
      当初被打断、没写回结果的调用标成"未完成"。恢复时上下文占用那行跟着清掉，空闲槽位换成
      这段历史最后一轮的回答。
- [x] 第一轮聊完自动给会话起标题（`src/core/title.go`）：把用户第一句提问和模型那段回答
      发给模型拟一个 20 字以内的中文标题，存进 `domain.Session.Title`；模型不给力就退回用
      提问本身截一段。`/resume` 列表里标题跟在 id 后面（`session.Store.readTitle` 只读文件头，
      不解析整份会话），`/session` 和恢复说明里也带上了。老会话没有标题，恢复后接着聊一轮会补上。
- [x] 运行中排队（`src/cli/queue.go`）：一轮还在跑时按 Enter 不再丢弃，消息排进队里
      显示在输入框上方；这一轮正常跑完就自动接上队头那条，中断或出错时留着不发。
      Alt+↑ / Alt+↓ 选中某条、Alt+E 取回输入框改、Del 删掉、Esc 取消选中，一打字就取消选中。
      排队用 Alt 组合键而不是裸 ↑↓ / Ctrl+E：裸 ↑↓ 是输入框挪光标、Ctrl+E 是 textarea 的"移到行尾"，
      抢了就没法写多行。输入框空着时裸 ↑↓ 也能选，给 Alt+方向键送不到程序的环境兜底。
      出队时内容放回输入框再走一遍 `submit`，所以排队里写 /命令、点技能名都和在空闲时敲一样生效。
- [ ] `/export` 导出成 markdown，方便贴 issue 或存档。
- [ ] `/rename`、`/delete`，现在只能 `/new` 和 `/resume`。
- [ ] 手动 `/compact`。压缩目前只在上下文占到 80% 时自动触发（`src/core/compact.go`）。

### 用量

- [ ] `src/core/usage.go` 只数 token，不折算成钱。按模型配置单价后显示本轮/累计费用。

## 三、工程化

- [ ] **README**。clone 下来不知道 `go run ./src/cli` 怎么跑、`config.yaml` 该填什么
      （注意 `**/config.yaml` 已在 `.gitignore` 里，得配一份 `.example` 提交）。
- [ ] `doc/ARCH.md` 是空文件，架构说明一行没写。
- [ ] **CI**。没有 `.github/`，现在全靠本地手动 `go test ./...`。
- [ ] 测试补齐：`src/provider/openai/` 和 `src/util/` 还没有测试文件。

## 建议顺序

1. 无界面模式 —— 改动集中在 `src/cli/cli.go`，复用现有的 `core.Loop`
2. `src/web` 联网工具 —— "查这个库最新用法"这类问题的成功率会明显提升
3. README + `doc/ARCH.md` —— 顺手把上面几个空目录的规划写进去，免得下次又忘
