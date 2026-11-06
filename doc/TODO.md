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
- [x] `src/web/` —— 联网能力。形状是 **seam + provider**（学 DSH 的 `ctx.web`）：
      工具层只认 `web.Search` / `web.Fetch`，不认厂商；换后端是加一个文件、改配置，
      不用碰工具本身。参考过 Claude Code 和 DSH 两家的做法，结论写在
      `doc/web-design.md`（为什么搜索不按"支持几个引擎"来设计）。
      - `web.go`：类型（`SearchRequest` / `SearchResult` / `Source`）、两个 provider 接口、
        注册表、**选择策略**、错误码、`capSources`。规范化结果里 `title` / `snippet` /
        `publishedAt` 都是可选的——后端不返回就空着，逼适配器编一个等于让这一层说谎。
      - 选择策略（搜索和抓取共用一份，`pickProvider` 泛型）：配置或
        `AGENT_WEB_SEARCH_PROVIDER` 指定了 id 就必须是它，没注册报 `CONFIGURED_MISSING`、
        不可用报 `CONFIGURED_UNAVAILABLE`；没指定就按注册顺序取第一个可用的。
        **不在多个可用后端之间"智能选择"**：多一个分支就多一种线上和本地不一致的可能。
      - `deepseek.go`：主路径，走 DeepSeek 的 Anthropic 兼容端点 +
        `web_search_20250305` 服务端工具。**实测可用**（2026-11-06）：回来的块是
        `text` / `server_tool_use` / `web_search_tool_result`，`text` 里的内容是模型写的
        带来源总结。代价是一次搜索 = 一次完整模型轮次（实测 4401 input + 636 output token）。
        - 这个端点**不给 snippet**（真 Anthropic 会在 `text` 块的 `citations[].cited_text`
          里给，它没有），所以 `Snippet` 只能空着。
        - 没有 `web_search_tool_result` 块时报错而不是降级：那种情况说明厂商没真搜，
          拿它的文字当搜索结果就是"模型自己编的搜索结果"。
      - `duckduckgo.go`：兜底，刮 HTML 结果页，不用 key、永远可用。
        它的契约是别人页面上的 `result__a` / `result__snippet` 两个 class——改一次就全废，
        所以排在优先级最后。解析不引 HTML 库；链接从 `//duckduckgo.com/l/?uddg=` 还原，
        广告位（`/y.js?`）跳过。
      - `fetch.go`：唯一的抓取后端，本机发 HTTP。**安全是这里的主要职责**：
        - 只认 http/https，URL 限 2048 字符；
        - 解析出的每个地址都必须是公网地址（拒回环、私有网段、链路本地含
          `169.254.169.254`、运营商级 NAT `100.64/10`、组播、`::1`、`fd00::/8`）；
        - `DialContext` 自己解析、校验、然后**连到校验过的那个 IP**——只靠请求前校验一次
          挡不住 DNS rebinding（先给公网地址过校验，真连时再给 127.0.0.1）；
        - 跳转最多 5 次，每跳重新校验，不能 302 到内网把前面的校验绕过去；
        - 只接 html/text，PDF、图片这类直接说读不了；
        - HTML → 文本不引 DOM 库：扔 script/style/注释、块级元素换行、空白压紧。
        - **走环境代理时防线要退一步**：配了 `HTTP(S)_PROXY` 时拨号目标是代理自己
          （实测本机是 `127.0.0.1:7897`），不放行的话凡是用代理的机器 WebFetch 100% 失败。
          放行的只有环境变量里声明的那个地址，其余内网照拦；rebinding 那一层改由代理负责。
      - `configure.go`：从配置装配。DeepSeek 的 key / 模型名 / 端点留空时从当前生效的
        那套模型借（前提是它确实指向 DeepSeek——拿 MiniMax 的 key 打 DeepSeek 只会 401）。
      - 工具层（`src/tool/websearch.go`、`webfetch.go`）只负责面向模型的约定：
        `queries` 是 1~4 个、并发发、按 URL 去重、轮询交错合并再截断；
        **任何一个查询失败就整体失败**——一半成功一半失败的结果模型不知道该信哪部分。
      - 两个工具 `Mutating` 都是 false，只读模式下也留着。
- [ ] 配置还没有 `/config` 表单入口：`WebSearch` 段现在只能手改 YAML。
      形状是 `WebSearch: {Provider, DeepSeek: {ApiUrl, Model, ApiKey, MaxUses},
      DuckDuckGo: {Enabled, Endpoint}}`，整段不写也能用。
- [ ] 没有搜索结果/抓取结果的缓存：同一个查询连问两次就是两次模型轮次的钱。
- [ ] PDF / 图片读不了（`fetch.go` 只解 html/text）。要读 PDF 得接一个抽取库，
      或者走 vLLM 的多模态（见本地 `pdf-to-markdown-vllm` 技能）。
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
- [x] **浏览器界面能直接切换已配好的模型**：以前只有"改当前那套配置"这一个表单，
      想换到另一套只能在输入框手打 `/model 名字`，而 `/model` 的说明文案还写着
      "顶栏的模型徽标点开也能直接挑"——点开是配置表单，没有挑选的地方。
      - 表单顶部新增一排「已配好 N 套」按钮（`#cfg-switch`）：当前那套高亮且点不动，
        点别的当场切过去；只有一套、或在"新增一套"模式时整块藏起来
        （新增时"切到哪套"和"正在填一套新的"混在一起，容易让人以为保存会写进被点的那套）。
      - 走 `POST /api/model`（body `{"model":"名字"}`），和 `/api/mode`、`/api/quiet`
        一样是全局开关、不要会话 id；名字认法复用 `config.PickModel`（名字、模型名、
        唯一前缀都认），和 `/model` 命令是同一份。
      - 服务端把 `/model` 的切换逻辑抽成 `Manager.switchModel`：内存 → 广播 meta → 写回配置。
        命令和接口共用一条路，不再各写一份。写回失败时切换照常生效（只提示一句），
        认不出名字则整个不生效、配置不动，回 400。
      - 切开之后重拉一次表单：上面那些输入框得换成新那套的值，不然看着像没切。
      - 测试 `TestHTTPSetModel`：切换成功、`Current` 写回、SSE 推一条新 meta、
        认不出的名字 400 且不动当前那套。
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
- [x] **跑着的时候输入框上方有一行状态**（`#turn-status`）：细圆环转着 + 一句"现在在干什么"，
      文案照抄终端底部那行 —— 连接模型… / 思考中… / 回答中… / 执行 <工具>… /
      还有 N 个工具在跑… / 等模型继续… / 连接失败，正在重试… / 等待确认… / 正在中断…；
      答完确认换成「已允许，执行 xxx…」或「已拒绝，等模型继续…」。
      - 精简显示开着（默认）时思考内容整段不摆，这行就是唯一的"它在想"信号：
        以前模型闷头想的那几十秒里界面上什么都没有，分不清是在想还是卡住了。
      - 它只说明走到哪一步，所以不进聊天记录：`done` / `error` / `cancelled` 一收尾整行收掉。
      - 重连、切会话回来时不知道这一轮具体走到哪一步，先挂「运行中…」，
        下一条事件到了换成准确的；切会话时先清掉上一条留下的那句。
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
- [x] **会话跑一步存一步，刷新/切会话不再丢历史**：以前只在"本轮第一个 step 开始"
      落过一次盘（那时会话里还只有一句提问），整轮结束再存一次。中途刷新页面、切走再切回来，
      读的是磁盘那份（运行中不能读内存，core 正在往它上面追加），所以只剩一句提问，
      助手说了什么、跑了哪些工具全没了。
      - 现在 `emit` 里按步落盘（`shouldSave`）：`EventStep` / `EventAnswerEnd` /
        `EventToolResult` 三个边界都存一次。流式的 `EventContent` / `EventReasoning`
        一个 token 一条，绝不在那个粒度上写文件。
      - **`core.go` 里把 `AppendAssistant` / `AppendToolResult` 提到 emit 之前**。
        原来顺序是"先 emit 再 Append"，界面收到事件时这条还没进会话，落盘存下去的
        永远是上一步，刷新看到的进度总差一截。`EventUsage` 保持在 AppendAssistant 之前
        （它报的是"请求模型时占了多少"，不能把刚生成的回答算进去）。
      - 测试 `TestSessionPersistsEachStepWhileRunning` 守着这条：跑一轮到中途，
        磁盘上必须已经有"提问 + 回答 + 工具结果"。验证过它能抓住旧行为（旧逻辑下
        磁盘只有 1 条提问）。
      - 顺带修了个测试卫生问题：`TestHTTPDeleteWorkspaceBusy` 以前 `defer s.Cancel()`
        就返回，不等 core 那个 goroutine 走完，而它还在往 `t.TempDir()` 里落盘，
        Windows 上清理目录会报"目录非空"删不掉。加了 `stopSession` 等它停干净。
        落盘变密集之后这个窗口才明显，连跑 5 轮已不再偶发。
- [x] **切会话不再卡顿**：以前点一条会话要等"1.5 MB 下载 + 162 ms 渲染"才有任何变化，
      体感就是"点了没反应，过一会儿才出来"。先量了一遍，四个原因叠在一起：
      - **响应太大**：一条聊得久的会话 snapshot 有 1.5 MB，其中 **43% 是工具输出**，
        而精简显示（默认开着）下前端连那些 DOM 都不建，传过去纯浪费。
      - **一次渲染 300 条**：`renderHistory` 全量重建 + 每条 assistant 都跑一遍 markdown 解析，
        实测 162 ms 主线程同步阻塞，3370 个 DOM 节点。
      - **串行阻塞**：`await refresh()` 之后才 subscribe、才拉 runtime，点下去到最后一段全是空白。
      - **零反馈**：点下去到数据回来之间界面上什么都不变，连"点上了没有"都不知道。
      - 改法：
        - 服务端 `GET /api/sessions/{id}?limit=N` 只返回最近 N 条（`historyOptions`）；
          工具输出按当前 quiet 配置裁掉（`callFromHistory` 的 quiet 分支）。
          实测同一会话：**1.5 MB → 59 KB**（limit=60 且 quiet）。
        - 前端 `openSession` 改成**先把界面切过去再去拉数据**：选中态、标题、"
          正在加载历史…"占位全是同步完成的（实测 **0～2 ms**），SSE 和 runtime 也并行起来不等 snapshot。
        - 默认只渲染最近 `HISTORY_PAGE`（60）条，上面留一条"加载更早的内容"入口，
          点一下把 limit 加倍再拉（实测渲染 **162 ms → 48 ms**，DOM 3370 → 351）。
        - 切会话时只挪选中态（`markActiveSession`），不再重画整个侧栏。
        - 快速连点多条时靠 `id !== state.sessionId` 丢弃过期响应（实测连点三条 4 ms，
          最终正确停在最后点的那条）。
      - **别拿"原始消息数"和"显示块数"相减**：工具结果会并进对应的 assistant 块，
        两个数不是一个单位。所以接口只返回 `has_more` 布尔值，前端也不显示"还有 N 条"这种数字。
- [ ] 没有 token 级的重放能力：断线期间的事件不补发，靠重拉历史对齐。

- [x] **切工作模式（授权）当场生效，不用等下一轮**：以前 `Session.Start` 把模式读成
      快照塞进 `domain.Ctx.Mode`，`tool.ForMode` 的清单也开跑那一刻就定死了。结果顶栏
      那个下拉框虽然随时能点、配置也写回去了，**这一轮还在按老模式走**：切到"完全授权"
      照样一个个弹确认，切到"只读"照样能写文件，看着就是"切了没生效"。
      - `domain.Ctx` 加 `ModeLive func() Mode` + `CurrentMode()`：有它就每一步现问一次，
        没有就用 `Mode` 那个快照（终端界面还是快照，那边跑着的时候本来也切不了模式）。
        webui 递的是 `s.Mode`，也就是顶栏切换改的那个字段。
      - `approved`、只读兜底、系统提示词的 `modeSection` 全部改读 `CurrentMode()`。
      - 工具清单也每步重算：`tool.FilterByMode(ctx.Tools, mode)`。webui 现在递 `tool.All()`
        （全套），core 按此刻的模式现筛，所以**从只读切到完全授权，下一步请求就把写类工具
        带上**；反过来切到只读，写类工具当场从请求里消失（漏进来的也由只读兜底挡住）。
        工具定义变了就 `usage.retrackTools` 重算那块的 token 估算。
      - **别的标签页也得跟着变**：新增 `meta` 事件（`kindMeta`），`setMode` / `setQuiet`
        改完广播给所有活着的会话订阅者，前端 `case "meta"` 直接 `applyMeta`；
        显示方式变了还会重画一遍历史。以前另一个标签页的下拉框停在旧值上，
        用户甚至会照着旧值又切回去。
      - 删了 `Session.SetMode`：没人用的第二条切换路径，只写配置和一条会话，
        不广播、也不管别的会话，留着迟早被误用。
      - 测试：`TestModeSwitchMidRoundStopsAsking`（问到一半切完全授权，后面不再问）、
        `TestModeSwitchMidRoundAddsWriteTools`（只读切完全授权，下一步的工具定义里
        就有写类工具并真的执行）、`TestSetModeNotifiesPages`（切模式推一条 meta）、
        `TestAllAndFilterByMode`。

- [x] **附图在聊天记录里也显示缩略图**：以前图发出去就从界面上消失了——输入区里那份预览
      已经被 `takeImages()` 清掉，而聊天记录那条只画文字，要刷新页面走 snapshot 才重新出现。
      - `started` 事件带上 `images`（data: 地址），前端 `case "started"` 交给 `userBubble(text, images)`。
        两条路现在都有图：实时（started 事件）和历史（`historyFromSession` 的 `imageURLs`）。
      - 缺 `media_type` 或 `data` 的坏图直接丢掉（`Image.Empty()`），不在页面上留加载失败的破图标。
      - 实测：625×471 的截图在记录里显示成 180×136 缩略图（`max-width: 180px` 等比缩）。
- [x] **`/config` 表单不再挤成一团**：620px 宽时底部四个按钮全被压成两行
      （"删除这套"竖着排、"拉列表"也折行）。改：
      - 弹窗放宽到 760px，标签列 7.5em，行距和 padding 都加了一点。
      - `.dialog .btn { white-space: nowrap }` —— 中文按钮在窄容器里会被逐字竖排，是所有弹窗的通病。
      - 底部拆成两行：路径和「共 N 套」各占一行（常很长，挤在按钮旁边会把按钮压扁），
        按钮再分左右两组（新增/删除 | 取消/保存）。
      - 窄屏（<560px）标签挪到输入框上面一列排；`.dialog-actions` 允许整体换行。
      - 实测（CDP 量渲染尺寸）：五个按钮高度全部 31px = 单行，弹窗 760px，路径区 2 行。

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
      （留空表示不发这个参数），`/config` 里用 ←→ 切，
      也可以直接打命令：`/effort` 看当前这档，`/effort high`、`/effort 默认` 就地改并写回配置。
      请求体由 `src/provider/deepseek.go` 拼。写错档位在造 provider 时就报错，
      不至于让接口回一个 400 让人猜。各家认不认这个字段不一样，所以默认不动它。
      - **档位按类型分开给**（`config.EffortLevelsFor`）。一开始是全局一套
        `minimal/low/medium/high`，那是照着 OpenAI 抄的，对 DeepSeek 是错的。
        DeepSeek 那份是**实测**出来的，不是查文档：故意发一个非法值，它会把期望列表原样吐回来——
        `422 unknown variant \`ludicrous\`, expected one of none, minimal, low, medium, high, xhigh, ultra, max`。
        现在 DeepSeek 九档（含空档）：默认 / none / minimal / low / medium / high / xhigh / ultra / max，
        八档全发过一遍确认都 200。
      - **`none` 不是"默认"的别名**。以前 `none`/`off` 都归到空串，但 DeepSeek 真认 `none`
        （明确让它别思考），和"压根不发这个字段、用模型自己的默认"是两回事。
        归到空串等于用户想关思考、结果配成了"看模型心情"。现在只有 默认/default/auto/off 归空串。
      - OpenAI / MiniMax 保持原来那几档：手上没有可信的验证手段，不确定的档位不往里加——
        加了只会让人选到一个接口不认的值，换回一个 400。认不出的类型按公共那几档算。
      - 浏览器表单切类型时档位下拉跟着换（`configView.EffortLevels` 带上各类型的表）；
        终端表单切类型时，若原来那档新类型不认，归回"默认"，不然保存会报错。

### 待办跟踪

- [x] `TodoWrite` 工具（`src/tool/todo.go`）：整份传上来整份替换，状态只有 pending /
      in_progress / completed 三种。清单存在内存里，输入框上方常驻一行进度，`/todo` 看完整清单，
      `/new`、`/resume` 时清空。系统提示词里写清了什么时候该开清单、任何时候最多一条进行中。
      注意 `Task` 工具（`src/tool/task.go`）是后台 bash 进程，不是子 agent，别混。
- [x] **收尾自检**（`src/core/todo.go`）：光靠系统提示词里那句"做完一项立刻改成 completed"
      不管用。翻了本地所有会话，最后一次带内容的清单更新几乎都停在"完成数 = 总条数 - 1"，
      最后一项永远挂着 in_progress，或者干脆传空数组把清单抹掉；有的会话列完清单后
      只更新过一两次，剩下的活全在清单外干。
      现在模型准备收尾（这一轮不再调用工具）时，`core.Run` 会回头看一眼清单：
      还有没做完的就补一条提醒再要一轮，让它自己核销掉。一次提问只催一次，
      催完还不改就直接收；就剩最后一步时也不催，免得撞上 `MaxSteps` 那个刹车。
      提醒只追加在请求末尾、**不写进会话历史**（和后台任务通知同一个路子，
      写进去会被落盘，恢复会话时又冒出来一遍）。
      清单从**这条会话自己的历史**里读（`lastTodos`），不读 `tool.CurrentTodos()`——
      后者是进程级的一份，浏览器界面多开几个会话会串。
- [x] 顺手修掉一个会把清单静默清空的 bug：`ParseTodoArgs`（`src/tool/todo.go`）现在
      用 `ok` 明确区分"显式传空数组 = 清空"和"参数写坏/没带 todos 字段 = 别动原来那份"。
      以前 webui 把两种情况都当成 nil 直接赋值，模型偶尔只传个 intent 时，
      界面上的进度就凭空消失了。
- [x] **修掉"拿清单打转"**（2026-11-06，用户当场反馈"AI 一直在纠结"）。翻会话记录复盘的：
      一轮问答里 `TodoWrite` 被调了 **16 次**、期间 **237 条消息**没有一句是用户说的，
      真正的活早就干完了。三个成因，逐个修：
      - **催办把人逼进死胡同**。原来那句"还没做完就接着做"没有给"这轮做不了"留出口。
        实测踩到的坑：清单里写了"补 doc/TODO 记录**并重启生效**"，而
        `restart-web.ps1 -WaitIdle` 等的正是**这个会话空闲**（= 本轮跑完），
        也就是说这条永远不可能在本轮内完成——自指死锁。模型唯一的出路就成了
        **把"重启生效"改写成"重启"**，让条目"看起来能收尾"，于是清单被反复重写。
        现在提醒给两条明确的出口：能做完就标 completed；这轮做不了的（要等用户拿主意、
        已经不需要、或者**必须等这一轮结束才谈得上验证**）就从清单里删掉并在回答里说明白。
        并明确写"改措辞让它看起来能收尾不算做完"。
      - **刹车形同虚设**。`TodoWrite` 一轮里调多少次都没人管，全靠 `MaxSteps`（1200）兜底。
        现在 `core` 里数着一轮改了几次，超过 `maxTodoEdits`（10）就往末尾补一句
        "改清单不是干活，别再重写它了"。加进去一次，之后跟着本回合剩下的请求走
        （和收尾自检一个路子），回合结束就没了。
      - **全做完的清单还赖在屏幕上**。`renderRuntime` 以前只在清单为空时才隐藏，
        六条全绿照样占六七行，看着就像"待办没清掉"（用户连着催了好几次清单该清掉）。
        现在全部完成时收成一行「✓ 待办 6/6 全部完成」（转成绿字），点一下能展开看明细；
        展开状态存在 `state.todosExpanded`，不然每几秒一次的轮询会把它折回去。
      - 测试：`TestTodoNoticeGivesBothWaysOut`（提醒里必须有两条出口、不能再说"接着做"）、
        `TestRunWarnsAboutRepeatedTodoEdits`（上限前不提醒、过了上限才提醒）。
        前者对着旧文案跑会失败（旧的那句就是"还没做完就接着做"），确认抓得住旧行为。
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

- [x] **改完代码怎么更新正在跑的服务**（`restart-web.ps1` + `restart-web.bat`）：
      `install.bat` 只管 `go build -o %GOPATH%\bin\agent-dev.exe`，服务在跑时那个 exe
      被进程占着，Windows 直接拒绝写入（实测 `The process cannot access the file
      because it is being used by another process`），所以边跑边装装不上去；而且它装完不启。
      脚本改成"先编好，再换，再起"：编译到 `agent-dev.exe.new`（旧服务照跑，实测 0.9 秒、
      零影响）→ 停旧进程 → 旧 exe 改名 `.old` 留底、`.new` 顶到正式名字 →
      后台隐藏启动 + 日志写 `agent-dev.out.log` / `.err.log` → TCP 探活（配了登录验证时
      回 401 也算活）→ 起不来就把旧 exe 换回去重启。
      断开只有换名前后的几百毫秒：浏览器 EventSource 自己重连，连上后前端重拉快照补齐，
      页面显示"已重新连上服务端"。代价是正在跑的那一轮会断（会话文件每轮结束才落盘）。
      `-Status` 只看现状、`-Stop` 只停不启。
      - **修：起不来时那段回滚从来没执行过。** 脚本里有 `$ErrorActionPreference = "Stop"`，
        而回滚前用 `Write-Error` 报错——它会当场终止整个脚本，下面"换回旧 exe 再拉起"
        一行都跑不到。现象就是"重启脚本没自动恢复，还得手动启动"（实测 pwsh 7：`Write-Error`
        之后的语句全部不执行）。改用 `Write-Host`。
      - **探活不再只认"端口通"。** 端口上要是别人的服务（8080 常被别的东西占着），或者旧进程
        还没退干净，脚本会喜报"好了"而实际跑的仍是旧代码。现在多校验一步：监听该端口的必须是
        刚起的那个 PID；回滚后同样校验。
      - **只动这个地址上的那一个。** `Get-WebProcs` 按 `--web` 的地址过滤，同机另开的
        （换端口试新版）只提一句不停；`Stop-WebProcs` 改成按 PID 逐个确认退出，
        不再用 CIM 计数判断——它偶发返回空，会让脚本误判"已经停了"，然后带着旧进程去抢端口。
      - **只能在网页上操作时怎么重启（`-Detach`）**：`restart-web.ps1 -Detach` 把自己
        以完全脱离的方式再起一份，然后 1 秒内返回。脱离出去的那份先 `-WaitIdle` 等空闲
        （轮询 `GET /api/sessions`，人可能在别的工作区里聊，所以要遍历所有工作区，
        只看默认工作区会把"正忙"误判成"空闲"），等所有会话都不再 running 才动手。
        这样从会话里触发重启也不会切断当前这一轮：这一轮的 done 一落盘就开始重启。
        超时（`-IdleTimeout`，默认 600 秒）就放弃并写日志，不硬杀。
      - **`-Detach` 必须用 WMI 创建进程，不能用 Start-Process。** 后者起的子进程攥着
        当前 shell 的 stdout 管道不放，调用方（agent 自己的工具、或任何捕获输出的终端）
        会一直等到超时——实测卡满 3 分钟，重启早完成了却看不到结果。WMI 创建的进程
        父是 WmiPrvSE，完全脱离这棵树：实测 44ms 创建、调用方 1 秒内拿回控制权，
        而且 `taskkill /F /T` 也波及不到它。代价是它没有 stdout，所以子实例自己
        `Start-Transcript` 写日志。
      - **换文件的顺序改成"先换，再停进程"。** Windows 允许重命名正在运行的 exe
        （映像已加载，不影响运行，实测重命名后服务照跑），所以先把 `$Exe` 改成
        `$Exe.<时间戳>.bak`、把新编译的顶上去，这两步失败时旧服务毫发无损；
        然后才停进程。原来的顺序是"停进程 → 换文件"，一旦换文件失败（实测遇到过），
        服务就停在那儿起不来了。
      - **备份名带时间戳，不再用固定的 `.old`。** 固定的 `agent-dev.exe.old` 实测会遇到
        "ACL 明明给了 FullControl、`Remove-Item` 却说 Access denied、但重命名可以"的情况，
        删不掉旧备份就直接卡死整个重启。唯一名永不撞车，那一步根本不需要先删东西。
        回滚同理：不靠删掉坏版本，而是把它重命名成 `.broken` 挪开。
- [ ] **`restart-web.bat` 现在跑不了**（它内部调 `pwsh`）。等 pwsh 修好就行，
      或者把 bat 里的 `pwsh` 换成绝对路径 `%SystemRoot%\System32\WindowsPowerShell\v1.0\powershell.exe`。
- [ ] **`go\bin\agent-dev.exe.old` 删不掉**：`Remove-Item` 报 Access denied 但重命名可以，
      像是某个已退出进程残留的句柄。不影响使用，占 16MB，想清就手工重命名后删。
- [ ] **`detectShell` 只做 `LookPath`，不验证 shell 能不能跑。** 所以 `pwsh` 被换成
      别的程序之后，agent 的 Bash 工具每次调用都报"不认识的参数 -NoLogo"，
      而且重启服务也没用（`LookPath` 只看文件在不在）。可以补一条"跑一次 `--version`
      确认能响应"的探活，或者 `LookPath` 失败时依次往下退。
- [ ] **重启不是真正零中断**：真想做到不断，得在前面挂一层反向代理（caddy/nginx）切 upstream，
      或者加个 `POST /api/shutdown` 让服务自己优雅退出（现在只能 `Stop-Process` 硬杀，
      正在跑的那一轮和 `tool.StopAllTasks` 都来不及）。
- [ ] **README**。clone 下来不知道 `go run ./src/cli` 怎么跑、`config.yaml` 该填什么
      （注意 `**/config.yaml` 已在 `.gitignore` 里，得配一份 `.example` 提交）。
- [ ] `doc/ARCH.md` 是空文件，架构说明一行没写。
- [ ] **CI**。没有 `.github/`，现在全靠本地手动 `go test ./...`。
- [ ] 测试补齐：`src/provider/openai/` 和 `src/util/` 还没有测试文件。

## 建议顺序

1. 无界面模式 —— 改动集中在 `src/cli/cli.go`，复用现有的 `core.Loop`
2. README + `doc/ARCH.md` —— 顺手把上面几个空目录的规划写进去，免得下次又忘
3. 搜索结果缓存 —— 现在同一个查询连问两次就是两次模型轮次的钱
