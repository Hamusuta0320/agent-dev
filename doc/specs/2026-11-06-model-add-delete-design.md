# 模型配置的新增与删除

日期：2026-11-06
状态：已确认，待实现

## 背景

现在 `/config` 只能改**当前生效的那一套**：表单里的名字是锁死的（`newConfigForm` 直接取
当前模型的 `Label`），保存走 `config.ReplaceModel`——名字对得上就替换、对不上就追加。
名字锁死 ⇒ 永远只会覆盖当前那套。`/model` 只列 + 切，**没有新增、没有删除、没有改名**。
想加一套模型只能手改 `~/.agent-dev/config.yaml` 的 `Models` 列表。

浏览器界面（`src/webui/`）是同一个毛病：`/config` 弹出来的表单也只改当前那套。

## 目标

- `/model add` 新增一套模型配置
- `/model del` 删除一套
- `/config` 表单能改名（真改名：原地换名字，不会多出一套）
- 终端和浏览器两边都能做

## 确认过的选择

- 入口挂在 `/model` 上：`/model add` 新增、`/model del` 删除
- 表单里加一格「名字」；新增时留空就用模型名当名字
- **新增存完不切**到新那套（当前那套不动）
- 编辑当前那套时名字可改，改了就是真改名
- 删除要二次确认；只剩一套时删不掉
- 两个界面都做。浏览器的二次确认用**确认框**（现有的 `askConfirm`），终端用「再打一遍命令」

## config 层：三个明确动作

`src/config/provider.go` 里现有 `ReplaceModel`（靠名字匹配猜"是替换还是追加"）退场，
换成三个各自只管一件事的动作。界面明明白白告诉它"这次干什么"，不让它猜。

```go
// EditModel 改 models 里叫 label 的那套：换成 mc。mc.Name 是新名字，
// 和 label 不同就是改名（原地换，不会多出一套）。返回新的列表和新的 Current
func EditModel(models []ModelConfig, current, label string, mc ModelConfig) ([]ModelConfig, string, error)

// AddModel 追加一套新的。mc.Name 空就用模型名当名字。
// 名字和现有的撞了报错——不默默覆盖人家配好的那套
func AddModel(models []ModelConfig, mc ModelConfig) ([]ModelConfig, error)

// DeleteModel 删掉 models 里叫 label 的那套。删的是当前那套就改用剩下的第一套。
// 只剩一套时删不了：配置里一套都没有的话启动就报错
func DeleteModel(models []ModelConfig, current, label string) ([]ModelConfig, string, error)
```

共同规则：

- 认名字按 `Label`（`Name` 和 `Model` 都认，和 `PickModel` 一个口径）
- 撞名检查：新名字和**别的**那套撞了 → 报错「已经有叫 X 的模型了」。
  改回自己原来的名字不算撞
- `Current` 留空时按第一套算（和 `CurrentModel()` 一个口径）
- `EditModel` 改的正是当前那套时，返回的 `Current` 跟着换成新名字
- `DeleteModel` 删掉的正是当前那套时，返回的 `Current` 指向剩下的第一套

这三个动作只管「模型列表怎么变」。字段校验（地址、密钥、模型不能空）还留在界面的表单里，
保存前已经查过了，这里不重复查。

## 终端（`src/cli/`）

### 命令分流

`switchModel` 里按参数分四种：

```
/model            → 列出几套，↑↓ 挑（现在一样）
/model 名字       → 切过去（现在一样）
/model add        → 打开「新增」的 /config 表单
/model del        → 打开「要删哪一套」的列表
/model del 名字   → 删那套（要确认）
```

`add` / `del` 是保留字，优先当命令解释（模型名不会叫这两个）。

### 表单加「名字」格

放最前面——它是这套模型的标识：

```
模型配置  新模型 · DeepSeek
▸ 名字     （未填）留空就用模型名
  类型     DeepSeek            ←→
  接口地址 https://api.deepseek.com
  密钥     ••••
  模型     deepseek-chat
  思考强度 默认                ←→
  上下文上限 128k
```

`configForm` 多一个字段 `editLabel`：**正在改哪一套**，空串 = 新增。
`newConfigForm(mc config.ModelConfig, editLabel string)`：

- 编辑：传当前那套的 `ModelConfig` 和它的 `Label`，名字格预填这个名字
- 新增：传空 `ModelConfig` 和空串 —— 类型默认 DeepSeek、地址给内置的、名字空着、其余空

刚配过任何模型之前 `/config` 也是这个形态（那时本来就没有"当前那套"可改），
`editLabel` 为空正好就是新增，不用额外分支。

名字格是**普通输入框**：编辑时改了就是改名；新增时留空则用模型名当名字。

### 保存

`saveConfig` 按模式分派：

- 新增 → `config.AddModel` → 追加到 `Models` 末尾，**`Current` 不变**，提示「已新增 X」
- 编辑 → `config.EditModel(models, current, editLabel, mc)` → 改名了 `Current` 跟着更新，
  提示「已保存」（改名时说「已改名成 X」）

### 删除

- picker 加一种：标题「要删哪一套？」，items 是各套模型
- 选中 → 打印「删掉 X ？」+「再打一遍 `/model del X` 就删」，**先不删**
- model 里记一个 `pendingDelete`（**上一次**请求删的那套）；再来一条同样名字的命令才真删。
  名字对不上就当成一次新的请求：先问一遍，不删
- 真删走 `config.DeleteModel` → 写回配置文件 → 内存同步 →
  删掉的正是当前那套时，上下文窗口跟着换（界面上「已用 x%」才不是按旧的算）
- 只剩一套 → 命令直接报错「只剩一套了，删不掉」

## 浏览器（`src/webui/`）

### 后端

- `/model add` → 回一个 `config` 回复，带 `mode=new`；前端弹**同一个** dialog，
  拉的是空白表单（`GET /api/config?new=1`）
- `configView` 多一个 `Mode`（`edit` / `new`）
- `configRequest` 多两个字段：`Name`（这一套叫什么）和 `New`（true = 新增而不是改当前那套）
- `DELETE /api/config/models?name=xxx` 删掉一套
- `commandReply` 多一个 `Kind: "confirm"` 和一个 `Confirm` 字段（值形如
  `del-model:MiniMax-M3`）：前端看到它就弹确认框，用户点确定后再发对应的请求

### 前端

- 配置 dialog 里加「名字」输入框
- dialog 里加「＋ 新增一套」按钮：点了就切到新增模式（重新拉一份空白表单）
- dialog 里加「删除这套」按钮（只在编辑模式显示）：点了弹现有的 `askConfirm`
  确认框，确认后发 `DELETE /api/config/models`
- `/model del 名字` → 服务端回 `Kind: "confirm"` → 前端弹同一个确认框
- `/model del`（不带名字）→ 回一段文字列出各套模型，让人照着写名字

## 错误处理

| 情况 | 行为 |
|---|---|
| 新增时名字留空 | 用模型名当名字 |
| 新增/改名时名字和现有的撞了 | 报错，不覆盖 |
| 只剩一套时删 | 报错「只剩一套了，删不掉」 |
| 删掉的正是当前那套 | 切到剩下的第一套 |
| 新增保存后 | `Current` 不变，不自动切 |
| 改名保存后 | `Current` 跟着更新，列表里不会多出一套 |
| 写配置文件失败 | 和现在一样：本次运行仍然生效，提示一句 |

## 测试

Go 侧（`src/config`）：

- `EditModel`：改名后列表长度不变、`Current` 跟着更新；改的不是当前那套时 `Current` 不动；
  新名字和别的撞了报错；找不到要改的那套报错
- `AddModel`：追加到末尾、`Current` 不变；名字留空用模型名兜底；撞名报错
- `DeleteModel`：删掉当前那套时 `Current` 指向剩下的第一套；删别的时 `Current` 不动；
  只剩一套报错；找不到报错

Go 侧（`src/cli`）：

- `/model add` 打开的表单是空白的，存完 `Models` 里多一套
- 表单里改名字存完是**改名**：`Models` 长度不变
- `/model del` 打第一次只打印确认提示、不删；再打一次才真删
- 只剩一套时 `/model del` 报错

Go 侧（`src/webui`）：

- `/model add` 回的是新增模式的 config 回复
- `POST /api/config` 带 `new=true` 时是追加而不是替换
- `DELETE /api/config/models?name=xxx` 删掉那套，删的是当前那套时 `Current` 跟着换

现有用到 `ReplaceModel` 的测试改成新的三个动作。
