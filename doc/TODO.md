# 待办

当前状态：`go build` / `go vet` / `go test ./...` 全过。TUI、会话持久化与恢复、上下文压缩、
贴图提问、后台任务、审批 diff 预览、约定文件与技能、工作模式隔离、失败重试都已可用。

技能除了模型自己按需读，用户也能直接点名：`/技能名 [补充说明]` 会把那份 `SKILL.md`
整段并进这一条提问（`src/core/skills.go` 的 `SkillPrompt`），`/skills` 列出装了哪些；
输入 `/` 开头时技能和内置命令一起补全，认不出来的 `/xxx` 会提示一句而不是发给模型。

下面按"代码里已经留了空目录"→"功能缺口"→"工程化"排。

## 一、规划了但还没写的目录

源码里已经有这几个空目录，是当初预留的位置，目前都还没有代码：

- [ ] `src/web/` —— 联网能力。全项目搜不到一处 HTTP 请求，模型**完全没法查在线文档**。
      至少要有 WebFetch（抓一个 URL 转纯文本）和 WebSearch。
- [ ] `src/plugin/` —— 插件 / MCP 接入。现在加能力只能改代码重新编译。
- [ ] `src/prompt/` —— 提示词模板。提示词目前硬编码在 `src/core/prompt.go`，
      想调措辞得改代码；独立出来还能做多套模板，比如给子任务用的精简版。
- [ ] `src/log/` —— 结构化日志。现在只有界面展示和 `core.ConsoleEmit` 的纯文本输出，
      跑完之后出了问题没法回溯。
- [ ] `src/model/` —— 接非 OpenAI 兼容的模型（比如 Anthropic 的 `/v1/messages`）。
      配置结构已经换成 `config.ModelConfig`（`DeepSeekConfig` 只是老名字的别名），
      造 provider 的入口也收敛到 `provider.New`，缺的是新协议本身的实现。
      剩下的多模型工作不需要这个目录也能做，先空着。

## 二、功能缺口

### 无界面 / 单次执行模式

- [ ] `main()`（`src/cli/cli.go:18`）完全不看 `os.Args`，只能进 TUI。
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
- [ ] 还差一家：Anthropic 那套 `/v1/messages` 协议和 OpenAI 兼容接口不一样，得单独实现一个
      provider 并在 `provider.New` 里分派。

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
