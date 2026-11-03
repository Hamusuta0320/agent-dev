# 待办

当前状态：`go build` / `go vet` / `go test ./...` 全过。TUI、会话持久化与恢复、上下文压缩、
贴图提问、后台任务、审批 diff 预览、约定文件与技能、工作模式隔离、失败重试都已可用。

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
- [ ] `src/model/` —— 模型抽象。`config.DeepSeekConfig` 名字写死在类型上，
      协议也只有 OpenAI 兼容一套（`src/provider/openai/`），接 Anthropic 得动到配置结构。

## 二、功能缺口

### 无界面 / 单次执行模式

- [ ] `main()`（`src/cli/cli.go:18`）完全不看 `os.Args`，只能进 TUI。
      加 `-p/--print "把活干了"` 和"从 stdin 读提问"，就能塞进脚本、git hook、CI。
      `core.Loop` 本来就能脱离 TUI 跑（`ConsoleEmit` 就是为此准备的），成本不高、收益最大。

### 回滚

- [ ] 没有 `/undo`。`src/tool/write.go` 写之前会把原件备份到 `.agent/backups/`，
      但没有任何入口把本回合改过的文件退回去，只能进目录里手工翻。

### 多模型

- [ ] 配置只有一份 `Provider`，没有 `/model` 切换。换模型要 `/config` 走一遍表单再写回文件。
      多个 provider 配置 + 运行时切换，是接多家模型的前置条件。

### 待办跟踪

- [ ] 没有 todo 清单。多步任务全靠模型自己记，长任务容易跑偏。
      注意 `Task` 工具（`src/tool/task.go`）是后台 bash 进程，不是子 agent，别混。

### 会话管理

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
