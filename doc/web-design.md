# 联网能力（src/web）为什么这么设计

代码在 `src/web/`，工具层在 `src/tool/websearch.go`、`src/tool/webfetch.go`。
这份文档记的是**决策和实测**，不是教程——看代码能知道"怎么做的"，这里说"为什么"。

## 一句话

形状是 **seam + provider**：工具层只认 `web.Search` / `web.Fetch`，不认厂商。
换后端 = 加一个文件 + 改一行配置，不动工具本身。

## 起点：先看两家怎么做

"是不是要支持多种搜索引擎"这个问题的答案取决于别人怎么解决同一件事。翻了两份源码：

### Claude Code（`D:\code\js\claude-code-sourcemap\restored-src\src\tools\`）

`WebSearchTool/WebSearchTool.ts` 的 `call()` 里**没有任何抓网页的代码**。它做的事是发第二次
LLM 请求，把厂商的服务端搜索工具塞进去：

```ts
{ type: 'web_search_20250305', name: 'web_search', max_uses: 8,
  allowed_domains?, blocked_domains? }     // makeToolSchema()

queryModelWithStreaming({ messages: [userMessage], tools: [],
  extraToolSchemas: [toolSchema], ... })   // call()
```

搜索在 Anthropic 后端执行，流里回来三类块：`server_tool_use` /
`web_search_tool_result`（`{title,url}[]`）/ `text`（带 `citations`）。
`isEnabled()` 只在 firstParty / vertex(Claude 4.0+) / foundry 返回 true——**它不是通用联网
能力，是"厂商能力反射"**：走不支持的网关时这个工具直接不出现。

`WebFetchTool` 才是本地实现：`getURLMarkdownContent` + `applyPromptToMarkdown`
（用小 prompt 从正文提炼），配 `isPreapprovedHost` 预批准域名、`maxResultSizeChars: 100_000`。

**值得抄的**：域名过滤是输入参数而不是靠提示词自觉；输出末尾强制"必须列出 Sources"。

### DSH（`~/.dsh/profiles/node_modules/@deepseek-ai/`）

比 Claude Code 分得干净，四层：

| 包 | 职责 |
|---|---|
| `dsh-web` | seam：`WebSearchProvider` / `WebFetchProvider` 接口、注册表、执行时选 provider、`capSources` |
| `dsh-web-search-deepseek` | 搜索 provider（唯一一个，id `deepseek-official`，同样走 `web_search_20250305`） |
| `dsh-web-fetch-http` | 抓取 provider（本地 HTTP，SSRF 防护、同源重定向、只解文本） |
| `dsh-tool-web` | 工具层：schema、上限、渲染、提示词；**"绝不调用 `available()`，绝不枚举提供方"** |

选择策略是明确拒绝猜的：

| 情形 | 结果 |
|---|---|
| 配了 id 且已注册可用 | 跑它 |
| 配了 id，没注册 | `WEB_PROVIDER_CONFIGURED_MISSING` |
| 配了 id，注册了但不可用 | `WEB_PROVIDER_CONFIGURED_UNAVAILABLE` |
| 没配 id，恰好一个可用 | 自动跑它 |
| 没配 id，多个可用 | `WEB_PROVIDER_AMBIGUOUS` |
| 没配 id，没有可用 | `WEB_PROVIDER_UNAVAILABLE` |

**值得抄的三条**：
1. 规范化结果里可选字段就是可选（`WebSearchResult{content?, sources[]{url, title?, snippet?, publishedAt?}, truncated}`）。
   DSH 的注释写得很硬：*forcing adapters to invent them would make the seam lie*。
2. 截断（`capSources`）归 seam 做，provider 不做。
3. 工具参数是 `queries: string[]`（默认上限 4），并发搜、按 URL 去重、轮询合并再截断。

## 我们的选择

和 DSH 同一个骨架，但优先级规则简化成"注册顺序就是优先级"——我们是一个应用，不是一个
插件生态，`AMBIGUOUS` 那种"拒绝猜"在这里没有收益，反而多一条要解释的分支。固定的 id
仍然是刚性的（配了谁就必须是谁，不静默换），这一条和 DSH 一致。

```
web.go          类型 + 接口 + 注册表 + 选择策略（pickProvider 泛型，搜索/抓取共用一份）
configure.go    从 config.GlobalConfig 装配
deepseek.go     搜索主路径：Anthropic 兼容端点 + web_search_20250305
duckduckgo.go   搜索兜底：刮 HTML 结果页，不用 key
fetch.go        抓取：本机 HTTP + SSRF 防护
```

工具层（`src/tool/`）只管"面向模型的约定"：参数校验、条数上限、输出格式、引用提示。

## 实测记录（2026-11-06，本机）

这些都是**真跑出来的**，不是照文档抄的。

### DeepSeek 的 Anthropic 兼容端点确实支持服务端搜索

```
POST https://api.deepseek.com/anthropic/v1/messages
     x-api-key: <key>   anthropic-version: 2023-06-01
body: { model: "deepseek-flash", max_tokens: 1024,
        messages: [{role:"user", content:[{type:"text", text:"Perform a web search for the query: ..."}]}],
        tools: [{type:"web_search_20250305", name:"web_search", max_uses:3}],
        thinking: {type:"disabled"} }
```

200，`content` 的块顺序是 `text` → `server_tool_use` → `web_search_tool_result` → `text`，
`usage.server_tool_use.web_search_requests = 1`，一次搜索约 **4401 input + 636 output token**。

**关键差异**：`text` 块**没有 `citations` 字段**（真 Anthropic 有 `cited_text` 可当 snippet），
`web_search_result` 里也只有 `title` / `url` / `encrypted_content` / `page_age`（实测 `null`）。
所以 DeepSeek 这家给不出 snippet，`Source.Snippet` 只能空着——这正是"可选字段就该可选"的用处。

### 用 config.yaml 里的 key 要认准文件

配置在 `~/.agent-dev/config.yaml`；项目目录下那份 `config.yaml` 是迁移前的旧文件，
里面的 key 已失效（打过去 401 `Authentication Fails`）。

### 抓取会被自己的代理绊倒

本机 `HTTP_PROXY=127.0.0.1:7897`（clash 之类）。`DialContext` 拿到的是**代理地址**，
被 SSRF 校验当成内网拦掉——表现是"抓 pkg.go.dev 失败，报 127.0.0.1 不许抓"。

处理：`isEnvProxyAddr` 放行环境变量里声明的那个代理地址，其余内网照拦；
DNS rebinding 那一层改由代理负责（拨号前的 `checkHostAllowed` 仍然校验真实目标的公网性，
"抓内网"照样挡得住）。不放行这条路的话，凡是用代理的机器 WebFetch 100% 失败。

### 一个差点上线的 bug

`classify()` 里原本写的是"provider 已经带码的错误就补一条底层原因"：

```go
if we.Err == nil { we.Err = err }   // err 就是 we 时 → 自引用
```

`Error()` 里拼 `e.Err.Error()` 直接无限递归，**栈溢出崩溃**（不是返回错误，是进程挂掉）。
触发它的是最常见的一条路径：抓内网地址被拒。真网验证时炸出来的，
单测当时没覆盖"provider 返回 WebError"这条路径。修法是不再补：原样返回即可。

## 已知取舍

| 取舍 | 为什么 |
|---|---|
| 刮 HTML 的兜底随时可能废 | DDG 改版就全废。留它的理由只有一个：不用 key、永远能用 |
| 一次 DeepSeek 搜索 = 一次模型轮次 | 换来的是带来源的总结。不该当成"免费联网开关" |
| 多查询同步失败 | 一半成功一半失败最难解释；全失败让模型换说法重来 |
| 走代理时 rebinding 防护弱一层 | 见上，代理解析 DNS，本地无从校验 |
| 只解 html/text | PDF 要抽取库或多模态，见 TODO |
| 没有缓存 | 同一查询问两次 = 两次钱，见 TODO |
