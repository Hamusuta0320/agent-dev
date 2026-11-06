// agent-dev 的浏览器前端。没有构建步骤，也没有框架：
// 服务端把 core 的事件用 SSE 推过来，这里按事件把对话往上长。
// 样式和交互组件用 Bootstrap 5（static/vendor/ 里打包进来的那份，不引 CDN），
// 这里只写业务逻辑。
//
// 三条原则：
// 1. 所有模型/工具产出的文本一律走 textContent，绝不 innerHTML —— 工具输出里带着
//    用户仓库里的任意内容，拼进 HTML 就是自己给自己开了个 XSS。
// 2. 界面状态以服务端为准：重连、切会话、切工作区都重新拉一次 snapshot，
//    不在前端攒"我认为的历史"。
// 3. 连接状态是显示出来的，不是猜的：EventSource 的 readyState 加上服务端心跳
//    一起判断，断了要让用户看见。
"use strict";

const $ = (id) => document.getElementById(id);

// 连接状态的文案。idle 是"还没开会话、没订阅"，不是出故障
const CONN_TEXT = {
  idle: "未连接",
  connecting: "连接中",
  online: "已连接",
  offline: "已断开",
};
// 心跳是 25 秒一次（sseHeartbeat），两个多周期没消息就当作这条连接半死了：
// 断网时 EventSource 往往还以为自己连着，光看 readyState 判断不出来
const HEARTBEAT_STALE_MS = 70000;
const HEARTBEAT_CHECK_MS = 5000;

const el = {
  sessions: $("sessions"),
  workdir: $("workdir"),
  wsToggle: $("ws-toggle"),
  wsName: $("ws-name"),
  wsPath: $("ws-path"),
  wsMenu: $("ws-menu"),
  wsDialog: $("ws-dialog"),
  wsInput: $("ws-input"),
  wsAdd: $("ws-add"),
  wsCancel: $("ws-cancel"),
  wsError: $("ws-error"),
  wsServer: $("ws-server"),
  wsUp: $("ws-up"),
  wsRoots: $("ws-roots"),
  wsHome: $("ws-home"),
  wsList: $("ws-list"),
  wsNew: $("ws-new"),
  wsNewFolder: $("ws-newfolder"),
  wsNewName: $("ws-new-name"),
  title: $("title"),
  badgeModel: $("badge-model"),
  badgeModelName: $("badge-model-name"),
  badgeEffort: $("badge-effort"),
  badgeEffortLevel: $("badge-effort-level"),
  badgeQuiet: $("badge-quiet"),
  badgeQuietMode: $("badge-quiet-mode"),
  mode: $("mode"),
  conn: $("conn"),
  connText: $("conn-text"),
  usage: $("usage"),
  usageFill: $("usage-fill"),
  usageText: $("usage-text"),
  chat: $("chat"),
  queue: $("queue"),
  composer: $("composer"),
  input: $("input"),
  suggest: $("suggest"),
  runtime: $("runtime"),
  todos: $("todos"),
  tasks: $("tasks"),
  attachments: $("attachments"),
  file: $("file"),
  hint: $("composer-hint"),
  statusLine: $("turn-status"),
  statusText: $("turn-status-text"),
  send: $("send"),
  cancel: $("cancel"),
  approval: $("approval"),
  approvalKind: $("approval-kind"),
  approvalTool: $("approval-tool"),
  approvalTarget: $("approval-target"),
  approvalDetail: $("approval-detail"),
  approvalDiff: $("approval-diff"),
  approvalAllow: $("approval-allow"),
  approvalAlways: $("approval-always"),
  approvalDeny: $("approval-deny"),
  approvalFeedback: $("approval-feedback"),
  // /config 表单
  cfgDialog: $("cfg-dialog"),
  cfgTitle: $("cfg-title"),
  cfgName: $("cfg-name"),
  cfgType: $("cfg-type"),
  cfgUrl: $("cfg-url"),
  cfgKey: $("cfg-key"),
  cfgModel: $("cfg-model"),
  cfgLimit: $("cfg-limit"),
  cfgEffort: $("cfg-effort"),
  cfgFetch: $("cfg-fetch"),
  cfgModels: $("cfg-models"),
  cfgSwitch: $("cfg-switch"),
  cfgError: $("cfg-error"),
  cfgPath: $("cfg-path"),
  cfgSave: $("cfg-save"),
  cfgCancel: $("cfg-cancel"),
  cfgNew: $("cfg-new"),
  cfgDelete: $("cfg-delete"),
  // 通用确认框（删会话、删工作区都用它）
  confirmDialog: $("confirm-dialog"),
  confirmTitle: $("confirm-title"),
  confirmText: $("confirm-text"),
  confirmOk: $("confirm-ok"),
  confirmAlt: $("confirm-alt"),
  confirmCancel: $("confirm-cancel"),
};

// 一次拉多少条历史。几百条一次渲染要 160ms（markdown 解析是大头），
// 白屏卡在那儿就不像"切会话"了。先给最近这么多，往上翻再要更早的
const HISTORY_PAGE = 60;

const state = {
  // 当前工作区（服务端算出来的 id）
  workspaceId: null,
  // 服务端认识的工作区列表
  workspaces: [],
  sessionId: null,
  running: false,
  // statusText 是输入框上方那行"现在在干什么"（连接模型 / 思考中 / 执行 bash…）。
  // 空串表示这一行收起来了——闲着的时候不该还挂着一句"思考中"
  statusText: "",
  // status 是服务端给的会话状态（running / waiting / done / idle），
  // 拉快照时更新；接排队消息时用它判断"上一轮是不是正常跑完了"
  status: "idle",
  source: null,
  // 连接状态：idle / connecting / online / offline
  conn: "idle",
  // 最后一次从这条连接收到任何东西的时间（心跳也算），用来发现半死连接
  lastSeen: 0,
  everConnected: false,
  // 当前这一轮的 DOM 槽位。收到 started 时重建，done/error 时清空
  turn: null,
  // historyLimit 是这次要拉多少条历史（0 表示全量）。切会话时给 HISTORY_PAGE，
  // 点"加载更早"往上加——一次渲染几百条 markdown 会卡住主线程
  historyLimit: HISTORY_PAGE,
  // historyTotal 不再需要：服务端只告诉我们"上面还有没有"，不给条数
  // （原始消息数和显示块数不是一个单位，相减会得到假数字）
  // sessionTitles 是 id → 标题的缓存，只为"点下会话标题立刻变"，不用等 snapshot
  sessionTitles: new Map(),
  // call_id → 那条工具调用的 DOM，结果回来按 id 对上
  calls: new Map(),
  // 正在等的确认：{id, prompted}
  approval: null,
  // 运行中又敲了消息就先排在这儿，这一轮正常结束自动发下一条。
  // 这是当前会话的那一份：切走的时候存进 drafts，切回来再取出来
  queue: [],
  // 附图，{mediaType, data, url}
  images: [],
  // drafts 是每条会话自己的草稿：输入框里的字、附图、排队那几条。  // 界面只有一套 DOM，但内容必须按会话分开——不然在 A 里写了一半，切到 B 就看见 A 的字，
  // 更糟的是 B 里一敲回车，把 A 的草稿发出去了
  drafts: new Map(),
  // quiet 是精简显示：只显示正文，思考过程和工具输出的内容都不显示。
  // 值来自服务端（配置里的 Quiet，没配过时默认就是开着的），/quiet 和顶栏徽标都能切
  quiet: true,
  // configMode 是配置表单当前的模式："edit" 改当前那套，"new" 新增一套
  configMode: "edit",
  // configEditLabel 是正在改的那套的名字，保存时原样带回去（新增时是空串）
  configEditLabel: "",
  // 文件夹选择器上一次列目录的结果。roots 为 true 时是盘符列表，path 为空
  fs: { path: "", parent: "", roots: false, entries: [], home: "", work_dir: "" },
  // 正在列目录；以及这次列目录的序号，连点过快时用来丢掉过期响应
  fsBusy: false,
  fsSeq: 0,
  // 命令补全：服务端给的命令表和技能名（{commands:[], skills:[]}），
  // 拉一次就够了，之后本地做前缀匹配
  completions: { commands: [], skills: [] },
  // 当前候选列表（{name, desc}）和选中的下标，-1 表示没开
  suggest: [],
  suggestIdx: -1,
  // /config 表单里各类型的内置接口地址（切类型时带出来用）
  configDefaults: {},
  // /config 表单里各类型的思考强度档位（切类型时换下拉用）
  configEfforts: {},
  // 待办清单全部完成时是否展开（默认收起成一行）
  todosExpanded: false,
};

/* ---------------- 小工具 ---------------- */

function api(path, options = {}) {
  return fetch(path, {
    headers: options.body ? { "Content-Type": "application/json" } : undefined,
    ...options,
  }).then(async (res) => {
    if (res.status === 204) return null;
    const text = await res.text();
    let data = null;
    if (text) {
      try {
        data = JSON.parse(text);
      } catch {
        data = { error: text };
      }
    }
    if (!res.ok) {
      throw new Error((data && data.error) || `请求失败（HTTP ${res.status}）`);
    }
    return data;
  });
}

/* ---------------- 右键菜单 ---------------- */

// ctxMenu 是整页共用的那一个菜单元素。右键是在哪一行触发的就填哪一行的动作，
// 所以只建一个、反复用：每行各建一个的话，菜单会跟着列表重画一起被回收，
// 而菜单可能是开着的
let ctxMenu = null;
// ctxCleanup 收起菜单时要摘掉的那几个全局监听，每次打开换一份
let ctxCleanup = null;

// closeContextMenu 收起菜单。菜单开着的时候页面还在轮询刷新列表，
// 所以收起这件事必须能被别处随时调用
function closeContextMenu() {
  if (!ctxMenu) return;
  ctxMenu.remove();
  ctxMenu = null;
  if (ctxCleanup) {
    ctxCleanup();
    ctxCleanup = null;
  }
}

// openContextMenu 在鼠标位置弹一个菜单。items 是 [{label, danger, onClick}]。
//
// 为什么不用原生 <menu>：这个页面里已经有 Bootstrap 的下拉（工作区列表就在里面），
// 原生菜单和它抢同一次点击，行为看浏览器。自己画一个，收起时机完全可控
function openContextMenu(event, items) {
  closeContextMenu();
  const menu = node("div", "ctx-menu");
  items.forEach((item) => {
    const btn = node("button", "ctx-item" + (item.danger ? " danger" : ""), item.label);
    btn.type = "button";
    btn.addEventListener("click", () => {
      closeContextMenu();
      item.onClick();
    });
    menu.append(btn);
  });
  // 先挂上量一遍尺寸，再按视口边缘往回推：贴着屏幕右边右键时菜单不能跑出去
  menu.style.left = "-9999px";
  menu.style.top = "-9999px";
  document.body.append(menu);
  const box = menu.getBoundingClientRect();
  const x = Math.min(event.clientX, window.innerWidth - box.width - 8);
  const y = Math.min(event.clientY, window.innerHeight - box.height - 8);
  menu.style.left = `${Math.max(8, x)}px`;
  menu.style.top = `${Math.max(8, y)}px`;
  ctxMenu = menu;

  // 点别处、按 Esc、滚动、窗口失焦都收起。用 capture 让它在行的 click 之前生效
  const onDown = (e) => {
    if (ctxMenu && ctxMenu.contains(e.target)) return;
    closeContextMenu();
  };
  const onKey = (e) => {
    if (e.key === "Escape") closeContextMenu();
  };
  document.addEventListener("mousedown", onDown, true);
  document.addEventListener("keydown", onKey);
  window.addEventListener("resize", closeContextMenu);
  window.addEventListener("blur", closeContextMenu);
  // 侧栏是独立滚动的，滚它就等于"换地方了"，菜单跟着关
  document.addEventListener("scroll", closeContextMenu, true);
  ctxCleanup = () => {
    document.removeEventListener("mousedown", onDown, true);
    document.removeEventListener("keydown", onKey);
    window.removeEventListener("resize", closeContextMenu);
    window.removeEventListener("blur", closeContextMenu);
    document.removeEventListener("scroll", closeContextMenu, true);
  };
}

function node(tag, className, text) {
  const n = document.createElement(tag);
  if (className) n.className = className;
  if (text !== undefined && text !== null) n.textContent = text;
  return n;
}

function scrollToBottom() {
  el.chat.scrollTop = el.chat.scrollHeight;
}

/* ---------------- 确认框 ---------------- */

// askConfirm 弹一个确认框，返回 Promise<"ok" | "alt" | null>。
// ok 是那个"确定"（默认红色），alt 是可选的另一个选项（比如"仅移除展示"），
// 关掉或用 Esc 都算 null（什么都没做）
function askConfirm({ title, text, okLabel = "确定", altLabel = "", danger = true }) {
  return new Promise((resolve) => {
    el.confirmTitle.textContent = title || "确认";
    el.confirmText.textContent = text || "";
    el.confirmOk.textContent = okLabel;
    el.confirmOk.className = `btn btn-sm ${danger ? "btn-danger" : "btn-primary"}`;
    el.confirmAlt.hidden = !altLabel;
    el.confirmAlt.textContent = altLabel || "";

    // 三个按钮各自只认一次：先摘掉上一次留下的监听，免得点了"取消"又触发上一回的"确定"
    const handler = (value) => () => finish(value);
    const onOk = handler("ok");
    const onAlt = handler("alt");
    const onCancel = handler(null);
    function finish(value) {
      el.confirmOk.removeEventListener("click", onOk);
      el.confirmAlt.removeEventListener("click", onAlt);
      el.confirmCancel.removeEventListener("click", onCancel);
      el.confirmDialog.removeEventListener("cancel", onEsc);
      el.confirmDialog.close();
      resolve(value);
    }
    function onEsc(e) {
      // Esc 就是"算了"，不当作任何一边
      e.preventDefault();
      finish(null);
    }
    el.confirmOk.addEventListener("click", onOk);
    el.confirmAlt.addEventListener("click", onAlt);
    el.confirmCancel.addEventListener("click", onCancel);
    el.confirmDialog.addEventListener("cancel", onEsc);

    el.confirmDialog.showModal();
    // 焦点给"取消"：这种框多半是手滑点出来的，默认落在不伤人的那个上
    el.confirmCancel.focus();
  });
}

/* ---------------- 每条会话自己的草稿 ---------------- */

// draftFor 取某条会话的草稿格子，没有就建一个空的
function draftFor(id) {
  if (!id) return { text: "", images: [], queue: [] };
  let draft = state.drafts.get(id);
  if (!draft) {
    draft = { text: "", images: [], queue: [] };
    state.drafts.set(id, draft);
  }
  return draft;
}

// saveDraft 把界面上这一份存回当前会话名下（切走之前、每次改动之后都调一下）
function saveDraft() {
  if (!state.sessionId) return;
  const draft = draftFor(state.sessionId);
  draft.text = el.input.value;
  draft.images = state.images;
  draft.queue = state.queue;
}

// loadDraft 把某条会话的草稿摆到界面上。切会话时用，所以顺带把队列和附图重画一遍
function loadDraft(id) {
  const draft = draftFor(id);
  el.input.value = draft.text || "";
  state.images = draft.images || [];
  state.queue = draft.queue || [];
  resizeInput();
  renderAttachments();
  renderQueue();
}

/* ---------------- 连接状态 ---------------- */

// setConn 只负责把状态摆到界面上。要不要在对话里提一句，由各个事件自己决定：
// 状态可能因为切会话、切工作区被主动改，那些时候不该喊"连接断了"
function setConn(status, text) {
  state.conn = status;
  el.conn.dataset.state = status;
  el.connText.textContent = text || CONN_TEXT[status] || "";
  el.conn.title = `与服务端的连接：${el.connText.textContent}`;
}

// watchdog 专治"看起来连着、其实收不到东西"：EventSource 自己不会察觉网络黑洞，
// 靠服务端心跳的到达时间判断，超时就自己重建一条
function startWatchdog() {
  setInterval(() => {
    if (!state.sessionId) {
      setConn("idle");
      return;
    }
    const src = state.source;
    if (!src) return;
    if (src.readyState === EventSource.CLOSED) {
      setConn("offline");
      return;
    }
    if (Date.now() - state.lastSeen > HEARTBEAT_STALE_MS) {
      if (state.conn === "online") notice("与服务端的连接没有响应，正在重连…", true);
      setConn("offline", "无响应，重连中");
      subscribe(true);
      return;
    }
    if (src.readyState === EventSource.CONNECTING) setConn("connecting", "重连中");
  }, HEARTBEAT_CHECK_MS);
}

/* ---------------- 页面骨架 ---------------- */

function renderEmpty() {
  el.chat.replaceChildren();
  const box = node("div", "empty");
  box.append(
    node("div", null, "问点什么试试"),
    node("div", null, "Enter 发送 · Shift+Enter 换行 · 可以直接粘贴截图"),
    node("div", null, "写操作会先弹出来问你要不要放行"),
  );
  el.chat.append(box);
}

function setRunning(running) {
  state.running = running;
  el.cancel.hidden = !running;
  el.send.disabled = running;
  el.hint.textContent = running ? "这一轮在跑：Enter 排进队里，点「中断」可以停下" : "";
  // 停下来了就没有"进行中"可言：这一行跟着收掉，免得屏幕上一直挂着"思考中…"
  if (!running) setStatus("");
  renderQueue();
}

// setStatus 换掉输入框上方那行状态。文案照抄终端底部那行（连接模型…、思考中…、
// 回答中…、执行 某个工具…），两个界面看到的东西一致；传空就是收起来。
// 它只说明"走到哪一步了"，所以模型一开口就换成下一句，不留在聊天记录里
function setStatus(text) {
  state.statusText = text || "";
  el.statusText.textContent = state.statusText;
  el.statusLine.hidden = !state.statusText;
}

// renderQueue 画排队那几条。每行带"取回"和"删除"两个按钮，
// 和 CLI 的 Alt+E（取回输入框改）/ Del（删掉）是一回事
function renderQueue() {
  if (state.queue.length === 0) {
    el.queue.hidden = true;
    el.queue.replaceChildren();
    return;
  }
  el.queue.hidden = false;
  el.queue.replaceChildren();
  el.queue.append(node("b", null, `排队 ${state.queue.length} 条（这一轮跑完自动接着发）`));
  state.queue.forEach((q, i) => {
    const row = node("div", "queue-item");
    row.append(node("span", "queue-text", `${i + 1}. ${q.prompt}`));
    const take = node("button", "queue-btn", "取回");
    take.type = "button";
    take.title = "放回输入框改一改（从队列里拿走）";
    take.addEventListener("click", () => takeBackQueued(i));
    const del = node("button", "queue-btn del", "删除");
    del.type = "button";
    del.title = "从队列里删掉这条";
    del.addEventListener("click", () => {
      state.queue.splice(i, 1);
      saveDraft();
      renderQueue();
    });
    row.append(take, del);
    el.queue.append(row);
  });
}

// takeBackQueued 把排队里的某条取回输入框：文字和附图都还回去，队列里拿走这一条。
// 输入框里已经有字就接在后面，别把正在写的东西顶掉
function takeBackQueued(i) {
  const item = state.queue[i];
  if (!item) return;
  state.queue.splice(i, 1);
  const cur = el.input.value.trim();
  el.input.value = cur ? `${cur}\n${item.prompt}` : item.prompt;
  if (item.images && item.images.length) {
    state.images = state.images.concat(item.images);
  }
  saveDraft();
  renderQueue();
  renderAttachments();
  resizeInput();
  el.input.focus();
}

/* ---------------- 待办清单与后台任务 ---------------- */

// loadRuntime 拉这条会话的待办清单和后台任务。
// 这两样在 CLI 上是输入框上方常驻的一块，浏览器这边按会话轮询（一行几 KB，不用 SSE 折腾）
async function loadRuntime() {
  if (!state.sessionId) {
    renderRuntime(null);
    return;
  }
  const id = state.sessionId;
  try {
    const data = await api(`/api/sessions/${id}/runtime`);
    // 拉的过程中切了会话：这份是上一条的，别摆上去
    if (id !== state.sessionId) return;
    renderRuntime(data);
  } catch (err) {
    // 会话刚被删掉之类，不打扰用户
  }
}

// renderRuntime 把待办清单和后台任务摆进输入框上方那块。两样都没内容就整块不占地方
function renderRuntime(rt) {
  const todos = (rt && rt.todos) || [];
  const tasks = (rt && rt.tasks) || [];
  const total = (rt && rt.todo_total) || todos.length;
  const done = (rt && rt.todo_done) || 0;
  // 全做完了就收成一行：清单全绿还占着六七行，看着像活还没干完——
  // 实测就是这一点让人以为"待办没清掉"。想看还是能点开，
  // 展开状态记在 state 里，不然每几秒一次的轮询会把它折回去
  const allDone = todos.length > 0 && done >= total;

  el.todos.hidden = todos.length === 0;
  el.todos.replaceChildren();
  if (todos.length) {
    const title = node(
      "div",
      "runtime-title" + (allDone ? " all-done" : ""),
      allDone ? `✓ 待办 ${done}/${total} 全部完成` : `待办 ${done}/${total} 完成`,
    );
    if (allDone) {
      const open = state.todosExpanded;
      title.classList.add("toggle");
      title.title = open ? "收起" : "展开看看做了哪些";
      title.addEventListener("click", () => {
        state.todosExpanded = !state.todosExpanded;
        renderRuntime(rt);
      });
    }
    el.todos.append(title);
    if (!allDone || state.todosExpanded) {
      todos.forEach((t, i) => {
        const mark = t.status === "completed" ? "✓" : t.status === "in_progress" ? "▸" : "·";
        el.todos.append(node("div", `todo-item ${t.status || "pending"}`, `${mark} ${i + 1}. ${t.content}`));
      });
    }
  }

  el.tasks.hidden = tasks.length === 0;
  el.tasks.replaceChildren();
  if (tasks.length) {
    const running = rt.running_tasks || 0;
    el.tasks.append(
      node("div", "runtime-title", running > 0 ? `后台任务 ${running} 个运行中` : `后台任务 ${tasks.length} 个`),
    );
    tasks.forEach((t) => {
      const row = node("div", "task-item" + (t.running ? " running" : ""));
      row.append(node("span", "task-id", t.id));
      row.append(node("span", "task-status", [t.status, t.elapsed].filter(Boolean).join(" · ")));
      row.append(node("span", "task-cmd", t.command || ""));
      el.tasks.append(row);
    });
  }

  el.runtime.hidden = el.todos.hidden && el.tasks.hidden;
}

/* ---------------- 顶部信息 ---------------- */

function applyMeta(meta) {
  // 胶囊是"标签 + 值"两段，这里只换值那一段，标签和样式留给 HTML/CSS
  el.badgeModelName.textContent = meta.model || "未配置";
  el.badgeModel.title = `当前模型 ${meta.model || "（未配置）"}，点开改配置（也可以敲 /config）`;
  el.badgeEffortLevel.textContent = meta.effort || "默认";
  // 默认档不发 reasoning_effort，标一下黄，一眼看出现在是不是特意调过
  el.badgeEffort.classList.toggle("on", (meta.effort || "默认") !== "默认");
  // 精简显示：开着时只显示正文
  state.quiet = meta.quiet !== false;
  el.badgeQuietMode.textContent = state.quiet ? "精简" : "详细";
  el.badgeQuiet.classList.toggle("on", !state.quiet);
  // 这一枚是真开关，读屏软件按 aria-pressed 播报"已按下/未按下"才对得上
  el.badgeQuiet.setAttribute("aria-pressed", state.quiet ? "true" : "false");
  el.mode.value = meta.mode;
}

// applyUsage 摆上下文占用。三类来源各用一种颜色（和终端界面同一套语义）：
// 系统提示词紫、工具定义橙、对话消息绿——哪一块在涨扫一眼就看出来，
// 不用逐个读数字。分项是本地按比例估算的，所以只给百分比，不假装精确到个位。
function applyUsage(u) {
  el.usage.hidden = false;
  const pct = Math.max(0, Math.min(100, u.used_percent || 0));
  el.usageFill.style.width = `${pct}%`;
  el.usageFill.className = "progress-bar" + (pct >= 90 ? " hot" : pct >= 70 ? " warn" : "");
  const fmt = (n) => (n >= 1024 ? `${(n / 1024).toFixed(n < 10240 ? 1 : 0)}k` : String(n));
  // 分项百分比按占 prompt 的比例算，和 CLI 的 ContextUsage.Percent 一个口径
  const share = (n) => (u.prompt_tokens > 0 ? Math.round((n * 100) / u.prompt_tokens) : 0);

  const frag = document.createDocumentFragment();
  const sep = () => frag.append(node("span", "u-sep", " · "));
  let first = true;
  const push = (children) => {
    if (!first) sep();
    first = false;
    children.forEach((c) => frag.append(c));
  };

  const head = [node("span", null, "上下文 "), node("span", "u-total", fmt(u.prompt_tokens))];
  if (u.context_limit) head.push(node("span", null, ` / ${fmt(u.context_limit)}`));
  // 用超过八成就变红，和 CLI 一个阈值：免得一路跑到爆窗口才发现
  if (u.context_limit) {
    head.push(node("span", null, " · 已用 "));
    head.push(node("span", pct >= 80 ? "u-over" : "u-total", `${pct}%`));
  }
  push(head);

  if (u.completion_tokens) {
    push([node("span", "u-dim", `本轮回复 ${fmt(u.completion_tokens)}`)]);
  }
  if (u.prompt_tokens) {
    push([node("span", "u-system", `系统 ${fmt(u.system_tokens)} ${share(u.system_tokens)}%`)]);
    push([node("span", "u-tool", `工具 ${fmt(u.tool_tokens)} ${share(u.tool_tokens)}%`)]);
    push([node("span", "u-msg", `对话 ${fmt(u.message_tokens)} ${share(u.message_tokens)}%`)]);
  }
  el.usageText.replaceChildren(frag);
}

/* ---------------- 工作区 ---------------- */

// currentWorkspace 是列表里跟 state.workspaceId 对上的那个
function currentWorkspace() {
  return state.workspaces.find((w) => w.id === state.workspaceId) || null;
}

function renderWorkspaces() {
  const cur = currentWorkspace();
  el.wsName.textContent = cur ? cur.name : "选择工作区";
  el.wsPath.textContent = cur ? cur.path || cur.id : "";
  el.workdir.textContent = cur ? cur.path || "" : "";

  el.wsMenu.replaceChildren();
  state.workspaces.forEach((ws) => {
    const wrap = node("li", "ws-item");
    const item = node("button", "dropdown-item" + (ws.id === state.workspaceId ? " current" : ""));
    item.type = "button";
    item.append(node("span", "ws-item-name", ws.name));
    if (ws.path) item.append(node("span", "ws-item-path", ws.path));
    item.addEventListener("click", () => {
      bootstrap.Dropdown.getOrCreateInstance(el.wsToggle).hide();
      if (ws.id !== state.workspaceId) selectWorkspace(ws.id);
    });
    // 删工作区走右键菜单（和工作区那一行一样，行上不挂 ✕）。
    // 两种删法差别很大，确认框里并排放，别让人手一滑就删了文件
    wrap.addEventListener("contextmenu", (e) => {
      e.preventDefault();
      bootstrap.Dropdown.getOrCreateInstance(el.wsToggle).hide();
      openContextMenu(e, [
        {
          label: "删除工作区…",
          danger: true,
          onClick: () => deleteWorkspace(ws),
        },
      ]);
    });
    wrap.append(item);
    el.wsMenu.append(wrap);
  });
  const divider = node("li");
  divider.append(node("hr", "dropdown-divider"));
  el.wsMenu.append(divider);
  const addWrap = node("li");
  const add = node("button", "dropdown-item", "＋ 添加工作区…");
  add.type = "button";
  add.addEventListener("click", () => {
    bootstrap.Dropdown.getOrCreateInstance(el.wsToggle).hide();
    openWorkspaceDialog();
  });
  addWrap.append(add);
  el.wsMenu.append(addWrap);
}

async function loadWorkspaces() {
  try {
    state.workspaces = (await api("/api/workspaces")) || [];
  } catch (err) {
    console.warn("拉工作区列表失败", err);
    state.workspaces = [];
  }
  // 选中的那个可能已经不在列表里（目录被删了）：退回服务端标的默认工作区
  const known = state.workspaces.some((w) => w.id === state.workspaceId);
  if (!known) {
    const fallback = state.workspaces.find((w) => w.default) || state.workspaces[0];
    state.workspaceId = fallback ? fallback.id : null;
  }
  renderWorkspaces();
}

async function selectWorkspace(id) {
  if (!id) return;
  state.workspaceId = id;
  localStorage.setItem("agent-dev.workspace", id);
  renderWorkspaces();
  // 切工作区就换了整套会话：先把当前会话的草稿存回去，再断开旧订阅
  saveDraft();
  if (state.source) {
    state.source.close();
    state.source = null;
  }
  state.sessionId = null;
  state.turn = null;
  state.calls.clear();
  hideApproval();
  hideSuggest();
  setRunning(false);
  loadDraft(null);
  renderRuntime(null);
  // 主动断的，不用弹"连接断了"
  setConn("idle");
  el.title.textContent = "新会话";
  renderEmpty();

  const infos = await loadSessions();
  const saved = localStorage.getItem(`agent-dev.session.${id}`);
  // 回到上次打开的那条；它不在了（删了、或者换了机器）就用最近的一条；
  // 一条会话都没有，才开一条新的——不然每切一次工作区就凭空多一条空会话
  if (saved && (infos || []).some((info) => info.id === saved)) {
    await openSession(saved);
    return;
  }
  localStorage.removeItem(`agent-dev.session.${id}`);
  if (infos && infos.length > 0) {
    await openSession(infos[0].id);
    return;
  }
  await newSession();
}

// deleteWorkspace 删一个工作区，两种删法：
//   - 仅移除展示：会话目录搬到会话根目录下的 .trash 里，列表里看不见了，文件还在
//   - 连同文件删除：会话目录整个删掉，里面的会话记录真没了
// 两种都不动工作目录本身（那是你的代码），只动 .agent-dev/sessions/<id>
async function deleteWorkspace(ws) {
  const label = ws.path || ws.name || ws.id;
  const answer = await askConfirm({
    title: "删除工作区",
    text: `「${label}」里存着这个目录的会话记录。\n\n` +
      "仅移除展示：记录搬到回收目录（.trash），列表里不再出现，想找回来还能手工搬回去。\n" +
      "连同文件删除：会话记录整个删掉，找不回来。\n\n" +
      "两种都不会动工作目录里的代码。",
    okLabel: "连同文件删除",
    altLabel: "仅移除展示",
  });
  if (!answer) return;

  const purge = answer === "ok";
  try {
    await api(`/api/workspaces/${encodeURIComponent(ws.id)}${purge ? "?purge=1" : ""}`, {
      method: "DELETE",
    });
  } catch (err) {
    notice(`删不掉工作区：${err.message}`, true);
    return;
  }
  notice(`工作区「${ws.name || ws.id}」已${purge ? "连同会话文件删除" : "从列表里移除（记录在 .trash）"}`);

  // 删的是当前这个工作区：换到剩下第一个；一个都不剩就提示没得用了
  if (ws.id === state.workspaceId) {
    if (state.source) {
      state.source.close();
      state.source = null;
    }
    state.sessionId = null;
    await loadWorkspaces();
    if (!state.workspaceId) {
      el.sessions.replaceChildren(node("div", "session-meta", "还没有工作区"));
      renderEmpty();
      setConn("idle");
      return;
    }
    await selectWorkspace(state.workspaceId);
    return;
  }
  await loadWorkspaces();
}

function openWorkspaceDialog() {
  el.wsError.hidden = true;
  el.wsNewFolder.hidden = true;
  el.wsNewName.value = "";
  const cur = currentWorkspace();
  el.wsInput.value = cur && cur.path ? cur.path : "";
  el.wsDialog.showModal();
  // 从当前工作区开始浏览；没有就从根（盘符）列表起步
  loadDirs(el.wsInput.value);
}

function showWorkspaceError(msg) {
  el.wsError.textContent = msg;
  el.wsError.hidden = false;
}

// loadDirs 列一个目录。每次请求带个序号：连点两个目录时，先发的那次可能后回来，
// 不能让它把新目录顶掉
async function loadDirs(path) {
  const seq = ++state.fsSeq;
  state.fsBusy = true;
  renderDirs();
  try {
    const data = await api(`/api/fs/list?path=${encodeURIComponent(path || "")}`);
    if (seq !== state.fsSeq) return;
    state.fs = data;
    el.wsInput.value = data.path || "";
    el.wsServer.textContent = data.work_dir ? `服务端 · ${data.work_dir}` : "";
    el.wsError.hidden = true;
  } catch (err) {
    if (seq === state.fsSeq) {
      showWorkspaceError(err.message);
      // 列表还是上一个目录的内容，地址栏也退回去，别让"选择/新建"作用在看不见的目录上
      el.wsInput.value = state.fs.path || "";
    }
  } finally {
    if (seq === state.fsSeq) {
      state.fsBusy = false;
      renderDirs();
    }
  }
}

function renderDirs() {
  const fs = state.fs;
  el.wsList.replaceChildren();
  // 根列表那一层没有"当前目录"，选择/新建都无从谈起；
  // 盘符根的上一级是盘符列表
  el.wsUp.disabled = state.fsBusy || fs.roots;
  el.wsAdd.disabled = state.fsBusy || fs.roots;
  el.wsNew.disabled = state.fsBusy || fs.roots;
  if (state.fsBusy) {
    el.wsList.append(node("div", "ws-dir-note", "读取目录中…"));
    return;
  }
  if (!fs.roots && fs.parent) {
    el.wsList.append(dirRow("..", fs.parent, { icon: "↰", hint: "上一级" }));
  }
  fs.entries.forEach((entry) => {
    el.wsList.append(dirRow(entry.name, entry.path, { dim: entry.name.startsWith(".") }));
  });
  if (!fs.entries.length) {
    el.wsList.append(node("div", "ws-dir-note", fs.roots ? "没有可浏览的盘符" : "（没有子目录）"));
  }
}

function dirRow(name, path, opts = {}) {
  const row = node("button", "ws-dir" + (opts.dim ? " dim" : ""));
  row.type = "button";
  row.append(node("span", "ws-dir-icon", opts.icon || "📁"));
  row.append(node("span", "ws-dir-name", name));
  if (opts.hint) row.append(node("span", "ws-dir-hint", opts.hint));
  row.addEventListener("click", () => loadDirs(path));
  return row;
}

async function addWorkspace() {
  // 地址栏是当前目的的准：用户可能刚改了路径还没回车
  const path = el.wsInput.value.trim();
  if (!path) {
    showWorkspaceError("先进入一个要添加的目录");
    return;
  }
  el.wsAdd.disabled = true;
  try {
    const info = await api("/api/workspaces", {
      method: "POST",
      body: JSON.stringify({ path }),
    });
    el.wsDialog.close();
    await loadWorkspaces();
    await selectWorkspace(info.id);
  } catch (err) {
    showWorkspaceError(err.message);
  } finally {
    renderDirs();
  }
}

// newFolder 展开新建文件夹那一行。根列表模式下没有父目录，不让建
function newFolder() {
  if (state.fs.roots) {
    showWorkspaceError("先进入一个盘符，再新建文件夹");
    return;
  }
  el.wsError.hidden = true;
  el.wsNewFolder.hidden = false;
  el.wsNewName.value = "";
  el.wsNewName.focus();
}

async function createFolder() {
  const name = el.wsNewName.value.trim();
  if (!name || state.fs.roots) return;
  const parent = state.fs.path;
  el.wsNewName.disabled = true;
  try {
    await api("/api/fs/mkdir", {
      method: "POST",
      body: JSON.stringify({ path: parent, name }),
    });
    el.wsNewFolder.hidden = true;
    await loadDirs(parent);
  } catch (err) {
    showWorkspaceError(err.message);
  } finally {
    el.wsNewName.disabled = false;
  }
}

/* ---------------- 会话列表 ---------------- */

// 会话状态的文案和圆点样式类。服务端给 running / waiting / done / idle
const STATUS_TEXT = {
  running: "进行中",
  waiting: "等待提问",
  done: "完成",
  idle: "还没聊过",
};

// loadSessions 重画侧栏的会话列表，并把这份列表返回给调用方
// （删完会话要靠它决定接着打开哪一条）
async function loadSessions() {
  if (!state.workspaceId) {
    el.sessions.replaceChildren(node("div", "session-meta", "还没有工作区"));
    return [];
  }
  let infos = [];
  try {
    infos = await api(`/api/sessions?ws=${encodeURIComponent(state.workspaceId)}`);
  } catch (err) {
    console.warn("拉会话列表失败", err);
    return [];
  }
  el.sessions.replaceChildren();
  (infos || []).forEach((info) => {
    el.sessions.append(sessionRow(info));
  });
  if (!infos || infos.length === 0) {
    el.sessions.append(node("div", "session-meta", "这个工作区还没有会话"));
  }
  return infos || [];
}

// sessionRow 是列表里的一条：状态圆点 + 标题 + 时间。
// 删除走右键菜单，行上不再挂删除按钮——每行一颗 ✕ 太吵，而且贴着标题容易误点。
// 右键（或键盘 Shift+F10 / 菜单键，浏览器会把它也报成 contextmenu）弹出来
function sessionRow(info) {
  const row = node("div", "session" + (info.id === state.sessionId ? " active" : ""));
  // 选中态靠它来挪（markActiveSession），切会话时就不用重画整个列表
  row.dataset.id = info.id;
  const status = info.status || "idle";
  // 记下标题：点这条会话时标题得立刻变，不能等 snapshot 回来
  if (info.title) state.sessionTitles.set(info.id, info.title);

  const main = node("button", "session-main");
  main.type = "button";
  main.title = STATUS_TEXT[status] || "";
  const dot = node("span", `status ${status}`);
  dot.title = STATUS_TEXT[status] || "";
  const body = node("span", "session-body");
  const title = node("span", "session-title", info.title || info.id);
  body.append(title, node("span", "session-meta", info.mod_time || ""));
  main.append(dot, body);
  main.addEventListener("click", () => openSession(info.id));

  row.addEventListener("contextmenu", (e) => {
    e.preventDefault();
    openContextMenu(e, [
      {
        label: "删除会话…",
        danger: true,
        onClick: () => deleteSession(info.id, info.title || info.id),
      },
    ]);
  });

  row.append(main);
  return row;
}

// clearSessionView 把界面退回"手上没有会话"的样子：断开订阅、清掉当前这一轮的痕迹，
// 历史和输入区都清空。删掉当前会话时用，和切工作区时那一段是同一件事
function clearSessionView() {
  if (state.source) {
    state.source.close();
    state.source = null;
  }
  state.sessionId = null;
  state.turn = null;
  state.calls.clear();
  // 历史分页回到默认：不然切到别的工作区，第一条会话还带着上一条的 limit
  state.historyLimit = HISTORY_PAGE;
  hideApproval();
  hideSuggest();
  setRunning(false);
  // 主动断的，不用弹"连接断了"
  setConn("idle");
  state.status = "idle";
  renderEmpty();
  renderRuntime(null);
  el.title.textContent = "新会话";
  loadDraft(null);
}

// deleteSession 删一条会话：连磁盘文件一起删（这个界面上的"删除"就是真删），
// 说清楚删了之后 /resume 也找不回来。
//
// 删的是当前这条时，接着打开列表里剩下的第一条；一条都不剩就停在空状态 ——
// 绝不在这里自动建新会话：删一条凭空多一条，列表里会攒一堆没人用过的空会话
async function deleteSession(id, label) {
  const answer = await askConfirm({
    title: "删除会话",
    text: `会话「${label}」会连同磁盘上的记录一起删掉，删了就找不回来了。正在跑的话要先中断。`,
    okLabel: "删除",
  });
  if (answer !== "ok") return;

  const wasCurrent = id === state.sessionId;
  // 删的是当前这条：先把界面从它上面摘下来，别让残留的事件流把对话又填回来
  if (wasCurrent) clearSessionView();

  try {
    await api(`/api/sessions/${id}?purge=1`, { method: "DELETE" });
  } catch (err) {
    notice(`删不掉：${err.message}`, true);
    await loadSessions();
    if (wasCurrent) await reopenAfterDelete();
    return;
  }
  // 删成功了：这条会话的草稿没用了
  state.drafts.delete(id);

  const rest = await loadSessions();
  if (!wasCurrent) return;
  if (rest && rest.length > 0) {
    // 接着看下一条：列表按最近改过排，第一条就是最该回去的那条
    await openSession(rest[0].id);
    return;
  }
  notice("这个工作区已经没有会话了：点左上角「＋ 新会话」开始，或者直接写也行");
}

// reopenAfterDelete 是"删失败了"之后的补救：既然这条还在，就把它重新打开，
// 别让界面停在一个已经不属于任何会话的空壳上
async function reopenAfterDelete() {
  const infos = await loadSessions();
  if (infos && infos.length > 0) await openSession(infos[0].id);
}

/* ---------------- 历史渲染 ---------------- */

function userBubble(text, images) {
  const wrap = node("div", "msg msg-user");
  if (images && images.length) {
    const box = node("div", "msg-images");
    images.forEach((src) => {
      const img = node("img");
      img.src = src;
      img.alt = "附图";
      box.append(img);
    });
    wrap.append(box);
  }
  if (text) wrap.append(node("div", "bubble", text));
  return wrap;
}

function reasoningBlock(text) {
  const details = node("details", "reasoning");
  details.open = false;
  details.append(node("summary", null, "思考过程"), node("div", null, text));
  return details;
}

// toolIcon 是工具调用前面那个小图标。以前用的是实心圆点，一行行堆起来很吵；
// 现在成功是勾、失败是叉、还没结果是一个细圆环。样式在 style.css 的 .tool-icon
function toolIcon(state) {
  const span = node("span", `tool-icon ${state}`);
  // 文字部分留空：勾和叉由 CSS 画，圆环也是（转圈靠 CSS 动画）
  span.title = state === "ok" ? "调用成功" : state === "fail" ? "调用失败" : "调用中";
  return span;
}

// toolElement 画一次工具调用。精简显示（state.quiet）下只留一行标题：
// 名字、意图、状态都在，输出正文和失败原因都不摆出来，也不可展开——
// 和终端里 /quiet 开着时"只留一行标题"是一回事
function toolElement(c) {
  const box = node("div", "tool");
  const head = node("div", "tool-head");
  const kind = c.status === "调用成功" ? "ok" : c.status === "调用失败" ? "fail" : "pending";
  const name = node("span", "tool-name", c.name);
  const intent = node("span", "tool-intent", c.intent || c.args || "");
  const status = node("span", `tool-status ${kind === "ok" ? "ok" : kind === "fail" ? "fail" : ""}`, c.status);
  head.append(toolIcon(kind), name, intent, status);
  box.append(head);
  if (state.quiet) return box;

  const body = node("div", "tool-body");
  if (c.fail_msg) body.append(node("div", "tool-fail", `原因: ${c.fail_msg}`));
  if (c.output) {
    const pre = node("pre");
    pre.textContent = c.output;
    body.append(pre);
  }
  head.addEventListener("click", () => box.classList.toggle("open"));
  box.append(body);
  return box;
}

// textBlock 把一段 markdown 正文包成一个"块"：有边界、右上角一个复制按钮。
//
// 复制的是 markdown 原文，不是渲染后的文字 —— 拿到的是模型真正说的话，
// 粘到别处还能重新排版。返回的 render 用来原地换内容（流式输出时每 90ms 调一次），
// 它只清空 body，复制按钮在 body 外面，所以重排不会把按钮冲掉
function textBlock(initial) {
  let src = initial == null ? "" : String(initial);
  const box = node("div", "text-block");
  const bar = node("div", "text-block-bar");
  const body = node("div", "stream md");
  const copy = node("button", "md-copy", "复制");
  copy.type = "button";
  copy.title = "复制这段原文";
  copy.addEventListener("click", () => Markdown.copyText(copy, src));
  bar.append(copy);
  box.append(bar, body);
  const render = (next) => {
    src = next == null ? "" : String(next);
    Markdown.into(body, src);
    // 还没吐出内容时（模型上来先调工具那段时间）整块藏起来：
    // 一个空框上挂一枚"复制"看着像坏了
    const empty = src.trim() === "";
    box.hidden = empty;
    bar.hidden = empty;
  };
  render(initial);
  return { box, body, render };
}

function streamBlock(text) {
  return textBlock(text).box;
}

function assistantBlock(m) {
  const wrap = node("div", "msg");
  // 精简显示下思考整段不摆：它只是过程，看正文就够了
  if (m.reasoning && !state.quiet) wrap.append(reasoningBlock(m.reasoning));
  if (m.content) wrap.append(streamBlock(m.content));
  if (m.tool_calls && m.tool_calls.length) {
    const tools = node("div", "tools");
    m.tool_calls.forEach((c) => tools.append(toolElement(c)));
    wrap.append(tools);
  }
  return wrap;
}

function renderHistory(snapshot) {
  el.chat.replaceChildren();
  // 快照里带着上次请求的上下文占用：刷新页面后 usage 事件不会补发，
  // 只有靠它才能把那一条复原。没有（还没聊过）就把整块收起来，
  // 不然会留着上一条会话的用量，看着像这条已经用掉那么多
  if (snapshot.usage) {
    applyUsage(snapshot.usage);
  } else {
    el.usage.hidden = true;
  }
  if (!snapshot.messages || snapshot.messages.length === 0) {
    renderEmpty();
    el.title.textContent = snapshot.title || snapshot.id;
    return;
  }
  // 只加载了尾部一段时，最上面放一个"加载更早"的入口。
  // 不画这个的话，用户往上翻到顶就以为会话只有这么点内容了。
  // 不显示条数：服务端算的是原始消息数，而这里一条 assistant 块可能合并了
  // 好几次工具调用，两个数不是一个单位，给出数字反而会骗人
  if (snapshot.has_more) {
    const bar = node("button", "load-earlier", "加载更早的内容");
    bar.type = "button";
    bar.addEventListener("click", async () => {
      bar.disabled = true;
      bar.textContent = "加载中…";
      await loadEarlier();
    });
    el.chat.append(bar);
  }
  snapshot.messages.forEach((m) => {
    if (m.role === "user") {
      el.chat.append(userBubble(m.content, m.images));
    } else if (m.role === "assistant") {
      el.chat.append(assistantBlock(m));
    } else if (m.role === "summary") {
      el.chat.append(node("div", "notice", m.content));
    }
  });
  el.title.textContent = snapshot.title || snapshot.id;
  scrollToBottom();
}

/* ---------------- 一轮的 DOM ---------------- */

function newTurn() {
  const root = node("div", "msg");
  // streamText 是这一轮攒下来的正文原文，pendingRender / lastRender 是节流用的：
  // 见 scheduleStreamRender
  const turn = {
    root,
    reasoning: null,
    reasoningBody: null,
    stream: null,
    // 正文块的外层（带复制按钮那层）和它的重排函数；stream 指的是里面的 body
    streamBox: null,
    streamRender: null,
    streamText: "",
    pendingRender: null,
    lastRender: 0,
    tools: null,
  };
  el.chat.append(root);
  // 空的历史提示在第一条消息进来时让位
  const empty = el.chat.querySelector(".empty");
  if (empty) empty.remove();
  return turn;
}

function ensureTurn() {
  if (!state.turn) state.turn = newTurn();
  return state.turn;
}

function appendReasoning(text) {
  // 精简显示下思考整段不摆（连折叠块都不建），正文该出现在哪还是哪
  if (state.quiet) return;
  const turn = ensureTurn();
  if (!turn.reasoning) {
    turn.reasoning = node("details", "reasoning");
    turn.reasoning.open = true;
    turn.reasoningBody = node("div");
    turn.reasoning.append(node("summary", null, "思考过程"), turn.reasoningBody);
    // 思考永远排在正文和工具之前
    turn.root.prepend(turn.reasoning);
  }
  turn.reasoningBody.textContent += text;
}

// 流式正文的重排间隔。模型是一小块一小块吐字的，几十毫秒就来一条；
// 每来一条就把整段 markdown 重新解析一遍，回答一长（几千字）就明显卡了。
// 攒着按时间合并：大约每秒重排十来次，看起来还是"打字机"效果
const STREAM_RENDER_MS = 90;

function appendContent(text) {
  const turn = ensureTurn();
  if (!turn.stream) {
    // 正文块自己带复制按钮；turn.stream 留着指向里面那块 body，
    // 重排（Markdown.into）和光标都只动 body，不动外面那层
    const block = textBlock("");
    turn.streamBox = block.box;
    turn.stream = block.body;
    turn.streamRender = block.render;
    turn.stream.classList.add("cursor");
    // 正文排在思考之后、工具之前
    const anchor = turn.tools || null;
    turn.root.insertBefore(turn.streamBox, anchor);
    turn.streamText = "";
  }
  turn.streamText = (turn.streamText || "") + text;
  scheduleStreamRender(turn);
  scrollToBottom();
}

// scheduleStreamRender 攒够一个间隔再重排；没到点就挂一个定时器，
// 到点把这段时间攒下的全部渲染出来
function scheduleStreamRender(turn) {
  if (!turn) return;
  const now = Date.now();
  if (!turn.pendingRender && now - (turn.lastRender || 0) >= STREAM_RENDER_MS) {
    renderStream(turn);
    return;
  }
  if (turn.pendingRender) return;
  turn.pendingRender = setTimeout(() => {
    turn.pendingRender = null;
    renderStream(turn);
  }, STREAM_RENDER_MS);
}

// renderStream 把攒下来的正文整段重排一遍。光标（▌）挂在最后一个块里，
// 这样它贴着最后一行字，而不是掉到下面单独一行
function renderStream(turn) {
  if (!turn || !turn.stream) return;
  turn.lastRender = Date.now();
  // 走块自己的 render：它同时把原文记在闭包里，复制按钮复制的是最新那份
  turn.streamRender(turn.streamText || "");
  if (turn.stream.classList.contains("cursor")) placeCaret(turn.stream);
}

function placeCaret(container) {
  const caret = node("span", "caret");
  const last = container.lastElementChild;
  if (last && /^(P|LI|H1|H2|H3|H4|H5|H6|BLOCKQUOTE|TD|TH)$/.test(last.tagName)) {
    last.append(caret);
    return;
  }
  container.append(caret);
}

// endContent 这一轮说完了：把还攒着的补渲染掉，收掉光标
function endContent() {
  const turn = state.turn;
  if (!turn || !turn.stream) return;
  if (turn.pendingRender) {
    clearTimeout(turn.pendingRender);
    turn.pendingRender = null;
  }
  renderStream(turn);
  turn.stream.classList.remove("cursor");
  const caret = turn.stream.querySelector(".caret");
  if (caret) caret.remove();
}

function addToolCall(ev) {
  const turn = ensureTurn();
  if (!turn.tools) {
    turn.tools = node("div", "tools");
    turn.root.append(turn.tools);
  }
  // 精简显示下不展开：只有一行标题在转圈，输出不摆出来
  const box = node("div", state.quiet ? "tool" : "tool open");
  const head = node("div", "tool-head");
  const icon = toolIcon("running");
  const name = node("span", "tool-name", ev.tool);
  const intent = node("span", "tool-intent", ev.intent || ev.args || "");
  const status = node("span", "tool-status", ev.status || "调用中");
  head.append(icon, name, intent, status);
  const body = node("div", "tool-body");
  if (!state.quiet) {
    head.addEventListener("click", () => box.classList.toggle("open"));
    box.append(head, body);
  } else {
    box.append(head);
  }
  turn.tools.append(box);
  if (ev.call_id) state.calls.set(ev.call_id, { box, icon, status, body });
  scrollToBottom();
}

function finishToolCall(ev) {
  const item = ev.call_id ? state.calls.get(ev.call_id) : null;
  if (!item) {
    // 结果比调用先到（重连之后、或者调用那条被丢了）：补一个，信息不全但别丢内容
    addToolCall(ev);
    return finishToolCall(ev);
  }
  state.calls.delete(ev.call_id);
  const failed = ev.status === "调用失败";
  // 图标跟着结果换：失败是叉、成功是勾
  item.icon.className = `tool-icon ${failed ? "fail" : "ok"}`;
  item.icon.title = failed ? "调用失败" : "调用成功";
  item.status.textContent = [ev.status, ev.elapsed].filter(Boolean).join(" · ");
  item.status.className = `tool-status ${failed ? "fail" : "ok"}`;
  // 精简显示下输出不写进 DOM（body 也没挂在框里），失败原因只在图标和状态上看颜色
  if (!state.quiet) {
    if (ev.fail_cause) item.body.append(node("div", "tool-fail", `原因: ${ev.fail_cause}`));
    if (ev.text) {
      const pre = node("pre");
      pre.textContent = ev.text;
      item.body.append(pre);
    }
  }
  item.box.classList.remove("open");
}

/* ---------------- 审批 ---------------- */

function showApproval(a) {
  state.approval = a;
  el.approvalKind.textContent = a.kind === "run_command" ? "执行命令" : "改文件";
  el.approvalTool.textContent = a.tool || "";
  el.approvalTarget.textContent = a.target || "";
  el.approvalDetail.textContent = a.detail || "";
  el.approvalDiff.replaceChildren();
  el.approvalFeedback.value = "";
  if (a.diff && ((a.diff.removed && a.diff.removed.length) || (a.diff.added && a.diff.added.length))) {
    el.approvalDiff.hidden = false;
    (a.diff.removed || []).forEach((line) => el.approvalDiff.append(node("div", "del", `- ${line}`)));
    (a.diff.added || []).forEach((line) => el.approvalDiff.append(node("div", "add", `+ ${line}`)));
    if (a.diff.truncated) el.approvalDiff.append(node("div", "cut", "…预览只显示开头几行"));
  } else {
    el.approvalDiff.hidden = true;
  }
  if (!el.approval.open) el.approval.showModal();
  el.approvalFeedback.focus();
}

function hideApproval() {
  state.approval = null;
  if (el.approval.open) el.approval.close();
}

async function answerApproval(answer) {
  const a = state.approval;
  if (!a || !state.sessionId) return;
  hideApproval();
  try {
    await api(`/api/sessions/${state.sessionId}/approve`, {
      method: "POST",
      body: JSON.stringify({ id: a.id, ...answer }),
    });
    // 答完立刻说清楚接下来会怎样，和终端那三句一致
    if (!answer.allow) setStatus("已拒绝，等模型继续…");
    else if (answer.always) setStatus(`已允许，本回合不再问 ${a.tool}…`);
    else setStatus(`已允许，执行 ${a.tool}…`);
  } catch (err) {
    // 这一轮已经被中断了、或者别处已经答过：说一声就好，不用拦着
    notice(`确认没送出去：${err.message}`, true);
  }
}

/* ---------------- 事件 ---------------- */

function notice(text, isError) {
  const line = node("div", `notice${isError ? " err" : ""}`, text);
  el.chat.append(line);
  scrollToBottom();
}

function handleEvent(ev) {
  switch (ev.kind) {
    case "ping":
      // 心跳只用来证明这条连接还活着，收到的时刻在 onmessage 里已经记过了
      break;
    case "meta":
      // 顶栏那几项都是全局开关：别的标签页（或 /mode、/quiet 命令）改过之后，
      // 这边得当场跟着变——下拉框停在旧值上，看着就像"切了没生效"
      if (ev.meta) {
        const wasQuiet = state.quiet;
        applyMeta(ev.meta);
        // 显示方式变了：已经摆出来的思考和工具输出要换个样子，重画一遍历史
        if (ev.meta.quiet !== wasQuiet) refresh();
      }
      break;
    case "started":
      // 先把上一轮的正文收尾（补渲染攒着的那点、去掉光标），再换新槽位
      endContent();
      state.turn = null;
      setStatus("连接模型…");
      // 附图跟着这条一起挂进聊天记录：发出去之后就只剩这一份，输入区里的预览已经清掉了
      if (ev.text || (ev.images && ev.images.length)) el.chat.append(userBubble(ev.text, ev.images));
      scrollToBottom();
      // 这一轮开始了：列表上的圆点要转起来
      loadSessions();
      break;
    case "step":
      // 新一轮请求模型：换一个槽位，上一轮的工具已经画完了
      state.turn = null;
      // 第 2 轮起说明模型又回来接着干了（中间夹着一堆工具调用），说出来更清楚
      if (ev.step > 1) setStatus(`第 ${ev.step} 轮`);
      break;
    case "reasoning":
      appendReasoning(ev.text || "");
      // 精简显示下思考内容不摆，但这句"思考中…"照旧要有：
      // 否则模型闷头想的时候界面上什么都没有，分不清是在想还是卡住了
      setStatus("思考中…");
      break;
    case "content":
      appendContent(ev.text || "");
      setStatus("回答中…");
      break;
    case "answer_end":
      endContent();
      break;
    case "tool_call":
      addToolCall(ev);
      setStatus(`执行 ${ev.tool}…`);
      break;
    case "tool_result":
      finishToolCall(ev);
      // 工具跑完可能刚列了待办、起了后台任务，这两块立刻刷一次
      loadRuntime();
      setStatus(state.calls.size > 0 ? `还有 ${state.calls.size} 个工具在跑…` : "等模型继续…");
      break;
    case "usage":
      if (ev.usage) applyUsage(ev.usage);
      break;
    case "approval":
      if (ev.approval) {
        showApproval(ev.approval);
        // 模型正停在这儿等人点头，先把状态说清楚
        setStatus("等待确认…");
        // 有确认要答：列表上这条会话该亮黄点
        loadSessions();
      }
      break;
    case "retry":
      notice(ev.text || "正在重试…");
      setStatus("连接失败，正在重试…");
      break;
    case "compact":
      notice(ev.text || "上下文压缩");
      break;
    case "title":
      el.title.textContent = ev.text || el.title.textContent;
      loadSessions();
      break;
    case "done":
      endContent();
      state.turn = null;
      setRunning(false);
      loadRuntime();
      // 正常跑完才接队里那条，和 TUI 一致：中断或出错时留着不发
      if (state.queue.length > 0) {
        const next = state.queue.shift();
        saveDraft();
        renderQueue();
        setRunning(true);
        setStatus("连接模型…");
        sendPrompt(next.prompt, next.images);
      } else {
        loadSessions();
      }
      break;
    case "error":
      endContent();
      state.turn = null;
      setRunning(false);
      notice(ev.text || "这一轮失败了", true);
      loadSessions();
      loadRuntime();
      break;
    case "cancelled":
      endContent();
      state.turn = null;
      setRunning(false);
      notice("已中断");
      loadSessions();
      loadRuntime();
      break;
    default:
      break;
  }
}

/* ---------------- 连接 ---------------- */

// subscribe 建一条 SSE 连接。reconnect 说明这是断线之后重建的（不是刚打开会话）：
// 那种情况下服务端可能有段时间没人收事件，连上要重拉一次历史补齐
function subscribe(reconnect = false) {
  if (state.source) {
    state.source.close();
    state.source = null;
  }
  if (!state.sessionId) {
    setConn("idle");
    return;
  }
  setConn("connecting");
  const source = new EventSource(`/api/sessions/${state.sessionId}/events`);
  state.source = source;
  state.lastSeen = Date.now();
  // connectedBefore 记这条 source 之前有没有连通过：EventSource 自己重连时
  // onopen 会再来一次，那也算重连
  let connectedBefore = false;
  source.onopen = async () => {
    state.lastSeen = Date.now();
    const again = connectedBefore || reconnect;
    connectedBefore = true;
    setConn("online");
    state.everConnected = true;
    if (!again) return; // 首次连上不用做事：openSession 已经把历史拉过了
    // 重拉会清空整个对话区，所以"重新连上"这句得等它画完再说，不然会被冲掉
    await refresh();
    notice("已重新连上服务端");
  };
  source.onerror = () => {
    // EventSource 自己会重连：CONNECTING 是"正在重试"，CLOSED 是它放弃了
    const wasOnline = state.conn === "online";
    if (source.readyState === EventSource.CLOSED) setConn("offline");
    else setConn("connecting", "重连中");
    if (wasOnline) notice("与服务端的连接断了，正在重连…", true);
  };
  source.onmessage = (msg) => {
    // 任何一条消息（含心跳）都说明连接是通的
    state.lastSeen = Date.now();
    if (state.conn !== "online") setConn("online");
    if (!msg.data) return;
    let ev;
    try {
      ev = JSON.parse(msg.data);
    } catch {
      return;
    }
    handleEvent(ev);
  };
}

async function refresh() {
  if (!state.sessionId) return;
  const id = state.sessionId;
  try {
    const limit = state.historyLimit || 0;
    const q = limit > 0 ? `?limit=${limit}` : "";
    const snapshot = await api(`/api/sessions/${id}${q}`);
    // 拉的过程中又切走了：这份是上一条的，别摆到新会话上
    if (id !== state.sessionId) return;
    renderHistory(snapshot);
    setRunning(snapshot.running);
    // 刚补齐历史（重连、切会话回来），不知道这一轮具体走到哪一步：先挂一句"运行中…"，
    // 下一条事件到了会换成准确的（思考中 / 回答中 / 执行某个工具）
    if (snapshot.running && !state.statusText) setStatus("运行中…");
    state.status = snapshot.status || (snapshot.running ? "running" : "idle");
    el.title.textContent = snapshot.title || snapshot.id;
  } catch (err) {
    if (id !== state.sessionId) return;
    notice(`拉会话失败：${err.message}`, true);
  }
}

// loadEarlier 往上翻一页历史：把 limit 加大再拉一次。
// 拉回来的是从尾部往前数的更多条，整个重画——历史不长的时候这样最省事，
// 也不用管"插到哪一行前面"的偏移问题（插进去会让滚动位置跳）
async function loadEarlier() {
  if (!state.sessionId) return;
  const id = state.sessionId;
  const cur = state.historyLimit || 0;
  state.historyLimit = cur === 0 ? HISTORY_PAGE * 3 : cur * 2;
  const before = el.chat.scrollHeight - el.chat.scrollTop;
  await refresh();
  // 等的时候切到别的会话去了：这份滚动位置不属于它，别去动
  if (id !== state.sessionId) return;
  // 重画之后把视口拉回原来的位置，看着就是"上面多出了一些旧内容"，不是跳走了
  el.chat.scrollTop = el.chat.scrollHeight - before;
}

// resumeQueueIfIdle 接上"切走期间跑完了"的排队：那一轮的 done 事件没推到这个页面，
// 队列就停在那儿没人接。只有在上一轮正常结束（status 是 done）时才接着发——
// 中断或出错的留着，发不发由人决定，和 CLI 一致
function resumeQueueIfIdle() {
  if (state.running || state.queue.length === 0 || state.status !== "done") return;
  const next = state.queue.shift();
  saveDraft();
  renderQueue();
  setRunning(true);
  setStatus("连接模型…");
  sendPrompt(next.prompt, next.images);
}

// openSession 切到某条会话。
//
// 关键顺序：先把界面切过去（标题、选中态、占位、SSE 连接），再去拉数据。
// 以前是 await refresh() 之后才做别的，所以点一下要等"1.5MB 下载 + 160ms 渲染"
// 才有任何变化——那就是"切会话非常延迟、没有丝滑感"的来源。
// 现在点下去当场就有反馈：标题换成目标会话的、聊天区出现占位、连接同步建立，
// 数据到了再填进去。
async function openSession(id) {
  if (!id) return;
  // 切走之前把这一份草稿存到原会话名下，切回来还能接着写
  if (state.sessionId && state.sessionId !== id) saveDraft();

  state.sessionId = id;
  if (state.workspaceId) localStorage.setItem(`agent-dev.session.${state.workspaceId}`, id);
  state.calls.clear();
  state.turn = null;
  setStatus("");
  hideApproval();
  hideSuggest();
  loadDraft(id);

  // —— 从这里开始全部是同步的，点下去立刻就有反馈 ——
  markActiveSession(id);
  el.title.textContent = sessionTitle(id) || id;
  showHistoryPlaceholder();
  // 这一条的先要这么多，往上翻时再加
  state.historyLimit = HISTORY_PAGE;

  // SSE 不等 snapshot：它和拉历史是两件独立的事，并行着做
  subscribe();
  // runtime（待办/后台任务）也并行，它和历史上互不依赖
  loadRuntime();
  try {
    await refresh();
  } catch {
    // refresh 自己会把错误画出来，这里不用再喊一遍
  }
  // 切走期间那一轮跑完了的话，排队那几条接着发（上面 refresh 拿到了最新状态）
  resumeQueueIfIdle();
  // 列表的状态圆点可能变了（别的标签页在跑），静默刷新，不阻塞切换
  loadSessions();
}

// markActiveSession 只挪选中态，不重画整个列表。
// 切会话时列表内容基本没变，重画一遍既没必要又会让侧栏闪一下
function markActiveSession(id) {
  el.sessions.querySelectorAll(".session").forEach((row) => {
    const active = row.dataset.id === id;
    row.classList.toggle("active", active);
  });
}

// sessionTitle 从已经拿到的列表里取这条会话的标题，取不到就用 id。
// 为的是"标题立刻变"，不用等 snapshot 回来
function sessionTitle(id) {
  const known = state.sessionTitles && state.sessionTitles.get(id);
  return known || "";
}

// showHistoryPlaceholder 在聊天区放一个"正在加载历史"。它不是空状态：
// 用户刚点了一条会话，看到"加载中"才确定点上了
function showHistoryPlaceholder() {
  el.chat.replaceChildren();
  const box = node("div", "empty history-loading");
  box.append(node("div", null, "正在加载历史…"));
  el.chat.append(box);
}

async function newSession() {
  if (!state.workspaceId) return;
  try {
    const snapshot = await api("/api/sessions", {
      method: "POST",
      body: JSON.stringify({ workspace: state.workspaceId }),
    });
    await openSession(snapshot.id);
    el.input.focus();
  } catch (err) {
    notice(`开新会话失败：${err.message}`, true);
  }
}

/* ---------------- 发送 ---------------- */

function takeImages() {
  const images = state.images.map((i) => ({ media_type: i.mediaType, data: i.data }));
  state.images = [];
  renderAttachments();
  return images;
}

function sendPrompt(prompt, images) {
  if (!state.sessionId) return;
  api(`/api/sessions/${state.sessionId}/messages`, {
    method: "POST",
    body: JSON.stringify({ prompt, images: images || [] }),
  }).catch((err) => {
    notice(`发不出去：${err.message}`, true);
    setRunning(false);
  });
}

// ensureSession 保证手上有条会话：没有就先开一条。
// 只在真要发东西的时候调（见 submit），所以不会在列表里留下没人用过的空会话
async function ensureSession() {
  if (state.sessionId) return true;
  if (!state.workspaceId) {
    notice("还没有工作区，先加一个", true);
    return false;
  }
  try {
    const snapshot = await api("/api/sessions", {
      method: "POST",
      body: JSON.stringify({ workspace: state.workspaceId }),
    });
    await openSession(snapshot.id);
    return true;
  } catch (err) {
    notice(`开新会话失败：${err.message}`, true);
    return false;
  }
}

async function submit() {
  const prompt = el.input.value.trim();
  if (!prompt && state.images.length === 0) return;

  hideSuggest();

  // 手上还没有会话（比如刚把最后一条删掉）：先开一条再发，别逼用户多点一次"新会话"。
  // openSession 会把输入框换成新会话的草稿（空的），所以先把手上的内容留下来还回去
  if (!state.sessionId) {
    const draftText = el.input.value;
    const draftImages = state.images;
    if (!(await ensureSession())) return;
    el.input.value = draftText;
    state.images = draftImages;
    renderAttachments();
    resizeInput();
  }

  // 打全的一条内置命令就地执行，不发给模型（和终端界面一致）。
  // 技能名（/技能名 ...）不算命令：那是"点名用某个技能"，服务端会照常当提问处理
  const first = prompt.split(/\s+/)[0];
  if (isBuiltinCommand(first)) {
    el.input.value = "";
    resizeInput();
    runCommand(prompt);
    return;
  }

  const images = takeImages();
  el.input.value = "";
  resizeInput();

  if (state.running) {
    // 这一轮还在跑：排进队里，结束之后自动接上（和 TUI 的排队是一回事）
    state.queue.push({ prompt: prompt || `看看这 ${images.length} 张图`, images });
    saveDraft();
    renderQueue();
    return;
  }
  setRunning(true);
  setStatus("连接模型…");
  sendPrompt(prompt, images);
}

// isBuiltinCommand 判断这一段是不是内置命令名。命令表来自服务端，
// 前端不写死一份，加了新命令这里自动认识
function isBuiltinCommand(field) {
  const lower = field.toLowerCase();
  return state.completions.commands.some((c) => c.name.toLowerCase() === lower);
}

/* ---------------- 附图 ---------------- */

const MAX_IMAGE_BYTES = 5 << 20;
const MAX_ATTACHMENTS = 4;

function renderAttachments() {
  // 附图也跟着草稿走：切会话再切回来，攒着的图还在
  saveDraft();
  el.attachments.replaceChildren();
  el.attachments.hidden = state.images.length === 0;
  state.images.forEach((item, i) => {
    const box = node("div", "attachment");
    const img = node("img");
    img.src = item.url;
    img.alt = "附图";
    const del = node("button", null, "✕");
    del.title = "去掉这张";
    del.addEventListener("click", () => {
      state.images.splice(i, 1);
      renderAttachments();
    });
    box.append(img, del);
    el.attachments.append(box);
  });
}

function addImageFile(file) {
  if (!file.type.startsWith("image/")) return;
  if (file.size > MAX_IMAGE_BYTES) {
    notice(`“${file.name}”太大（${Math.round(file.size / 1024)} KB），单张最多 5 MB`, true);
    return;
  }
  if (state.images.length >= MAX_ATTACHMENTS) {
    notice(`一条提问最多带 ${MAX_ATTACHMENTS} 张图`, true);
    return;
  }
  const reader = new FileReader();
  reader.onload = () => {
    const url = String(reader.result || "");
    const comma = url.indexOf(",");
    if (comma < 0) return;
    state.images.push({
      mediaType: file.type,
      data: url.slice(comma + 1),
      url,
    });
    renderAttachments();
  };
  reader.readAsDataURL(file);
}

/* ---------------- 命令补全 ---------------- */

// 命令表和技能名从服务端拉一次：和终端界面用的是同一张表（src/command），
// 所以两边认的命令一模一样。之后在本地做前缀匹配，打字不用等网络
async function loadCompletions() {
  try {
    const data = await api("/api/commands");
    state.completions = {
      commands: (data && data.commands) || [],
      skills: (data && data.skills) || [],
    };
  } catch (err) {
    console.warn("拉命令表失败", err);
  }
}

// commandPrefix 判断这会儿该不该补全：只有第一段、还没打空格、以 / 开头
function commandPrefix(value) {
  const s = value.replace(/^[ \t]+/, "");
  if (!s.startsWith("/") || /[ \t\n]/.test(s)) return "";
  return s;
}

// suggestList 是当前输入下的候选：内置命令排前面，技能接在后面
function suggestList(value) {
  const prefix = commandPrefix(value);
  if (!prefix) return [];
  const lower = prefix.toLowerCase();
  const all = [...state.completions.commands, ...state.completions.skills];
  const out = all.filter((c) => c.name.toLowerCase().startsWith(lower));
  // 已经打全了就不再提示
  if (out.length === 1 && out[0].name.toLowerCase() === lower) return [];
  return out;
}

function hideSuggest() {
  state.suggest = [];
  state.suggestIdx = -1;
  el.suggest.hidden = true;
  el.suggest.replaceChildren();
}

// refreshSuggest 在每次输入变化时重算候选。当前选中的那一项尽量跟着走，
// 但候选变了就退回第一项（和终端界面的做法一致）
function refreshSuggest() {
  const list = suggestList(el.input.value);
  if (list.length === 0) {
    hideSuggest();
    return;
  }
  const prev = state.suggest[state.suggestIdx] && state.suggest[state.suggestIdx].name;
  state.suggest = list;
  const keep = prev ? list.findIndex((c) => c.name === prev) : -1;
  state.suggestIdx = keep >= 0 ? keep : 0;
  renderSuggest();
}

function renderSuggest() {
  el.suggest.replaceChildren();
  if (state.suggest.length === 0) {
    el.suggest.hidden = true;
    return;
  }
  el.suggest.hidden = false;
  state.suggest.forEach((item, i) => {
    const row = node("div", `suggest-row${i === state.suggestIdx ? " active" : ""}`);
    row.append(node("span", "suggest-name", item.name), node("span", "suggest-desc", item.desc || ""));
    // 鼠标也能用：点一下就等于选中它再补全
    row.addEventListener("mousedown", (e) => {
      e.preventDefault(); // 别把焦点从输入框抢走
      state.suggestIdx = i;
      acceptSuggest();
    });
    el.suggest.append(row);
  });
}

// acceptSuggest 把选中的那条填进输入框，末尾留一个空格方便直接接参数
function acceptSuggest() {
  const item = state.suggest[state.suggestIdx];
  if (!item) return;
  el.input.value = `${item.name} `;
  hideSuggest();
  resizeInput();
  el.input.focus();
}

function moveSuggest(delta) {
  if (state.suggest.length === 0) return;
  const n = state.suggest.length;
  state.suggestIdx = ((state.suggestIdx + delta) % n + n) % n;
  renderSuggest();
}

/* ---------------- 本地命令 ---------------- */

// runCommand 把一条 /命令 交给服务端执行。命令改的东西（模型、模式、思考强度）
// 会随回包带回来，这里直接刷顶栏
async function runCommand(text) {
  if (!state.sessionId) return;
  commandEcho(text);
  try {
    const reply = await api(`/api/sessions/${state.sessionId}/command`, {
      method: "POST",
      body: JSON.stringify({ command: text }),
    });
    if (reply.meta) applyMeta(reply.meta);
    switch (reply.kind) {
      case "config":
        // /model add 回的是"新增一套"，其余（/config）是改当前那套
        await openConfigDialog(reply.config === "new" ? "new" : "edit");
        return;
      case "confirm":
        // 删除这类不可回退的操作：弹确认框，点了确定才真做
        await runConfirm(reply);
        return;
      case "session":
        await openSession(reply.session);
        loadSessions();
        return;
      default:
        if (reply.text) commandOutput(reply.text);
    }
    // /quiet 改的是"历史怎么显示"，已经画出来的那块得按新规矩重画一遍
    if (text.startsWith("/quiet")) await refresh();
  } catch (err) {
    notice(`命令没跑成：${err.message}`, true);
  }
}

// commandEcho 是敲了命令之后立刻回显的那一行：命令本身要留在对话里，
// 不然翻回来看见一堆列表，不知道当时打的是什么
function commandEcho(text) {
  const empty = el.chat.querySelector(".empty");
  if (empty) empty.remove();
  el.chat.append(node("div", "cmd-line", `❯ ${text}`));
  scrollToBottom();
}

// commandOutput 摆命令的结果：等宽字体，纯文本
function commandOutput(text) {
  const pre = node("pre", "cmd-out");
  pre.textContent = text;
  el.chat.append(pre);
  scrollToBottom();
}
/* ---------------- 输入框 ---------------- */

function resizeInput() {
  el.input.style.height = "auto";
  el.input.style.height = `${Math.min(el.input.scrollHeight, 220)}px`;
}

async function loadMeta() {
  try {
    applyMeta(await api("/api/meta"));
  } catch (err) {
    console.warn("拉配置失败", err);
  }
}

/* ---------------- 精简显示 ---------------- */

// toggleQuiet 切显示方式。配置项是服务端那份（Quiet），走 /api/quiet，
// 所以不需要会话 id，也不管在终端还是浏览器里改，下次起来都还是这个设置。
// 改完把历史重画一遍：已经摆出来的思考和工具输出要跟着换个样子
async function toggleQuiet(want) {
  try {
    applyMeta(await api("/api/quiet", { method: "POST", body: JSON.stringify({ quiet: want }) }));
  } catch (err) {
    notice(`切不了显示方式：${err.message}`, true);
    return;
  }
  // 重画历史（会按新的 state.quiet 走）；重画会把聊天区清空，所以先把提示打出来
  await refresh();
  notice(want ? "已切到精简显示：只显示正文" : "已切到详细显示：思考和工具输出点开能看");
}


// openConfigDialog 打开模型配置表单：值从服务端取（密钥只告诉"配过没有"）。
// mode 是 "new" 时拉一张空白表单（新增一套），否则改当前那套
async function openConfigDialog(mode = "edit") {
  let view;
  try {
    view = await api(mode === "new" ? "/api/config?new=1" : "/api/config");
  } catch (err) {
    notice(`打不开配置表单：${err.message}`, true);
    return;
  }
  applyConfigView(view);
  showConfigError("");
  el.cfgModels.hidden = true;
  el.cfgModels.replaceChildren();
  if (!el.cfgDialog.open) el.cfgDialog.showModal();
  el.cfgName.focus();
}

// applyConfigView 把服务端给的表单值摆进表单
function applyConfigView(view) {
  state.configDefaults = view.default_urls || {};
  // 各类型的档位表：切类型时下拉要跟着换（DeepSeek 认的比另两家多）
  state.configEfforts = view.effort_levels || {};
  state.configMode = view.mode || "edit";
  state.configEditLabel = view.edit_label || "";
  fillSelect(el.cfgType, view.types || [], (t) => [t, t], view.type);
  fillEfforts(view.efforts || [], view.effort);
  el.cfgName.value = view.name || "";
  el.cfgUrl.value = view.api_url || "";
  el.cfgUrl.placeholder = "留空用该类型内置的地址";
  el.cfgKey.value = "";
  el.cfgKey.placeholder = view.api_key_set ? "已配置，留空表示不改" : "填上密钥";
  el.cfgModel.value = view.model || "";
  el.cfgLimit.value = view.context_limit || "";
  el.cfgTitle.textContent = view.name ? `当前这套：${view.name}` : "还没有配置模型";
  // 新增模式下没有"这一套"可删，也不该再开一个新增表单
  el.cfgDelete.hidden = state.configMode !== "edit";
  el.cfgNew.hidden = state.configMode === "new";
  const models = view.models || [];
  // 路径单独一行（它常很长），"有几套、能切到哪套"由上面那排按钮说，不再重复一遍
  el.cfgPath.textContent = view.cfg_path || "";
  renderConfigSwitch(view, models);
}

// fillEfforts 填思考强度下拉。档位是显示名，提交时要换成档位本身（"默认" 就是空串）。
// keep 是当前选中的那个显示名；它不在这份档位里（切了类型）就落回"默认"，
// 不然下拉会停在空处、保存时提交一个这类型不认的值
function fillEfforts(labels, keep) {
  const options = labels.map((label) => ({ label, value: label === "默认" ? "" : label }));
  let want = keep === "默认" || keep === undefined || keep === null ? "" : keep;
  if (!options.some((o) => o.value === want)) want = "";
  fillSelect(el.cfgEffort, options, (o) => [o.label, o.value], want);
}

// renderConfigSwitch 把"已经配好的几套"摆成一排按钮：当前那套高亮且点不动，
// 点别的就直接切过去。只有一套、或者在新增模式时整块藏起来——
// 新增时"切到哪套"和"正在填一套新的"混在一起，容易让人以为保存会写进被点的那套
function renderConfigSwitch(view, models) {
  el.cfgSwitch.replaceChildren();
  el.cfgSwitch.hidden = true;
  if (state.configMode !== "edit" || models.length < 2) return;
  const current = (state.configEditLabel || "").trim();
  el.cfgSwitch.hidden = false;
  el.cfgSwitch.append(node("div", "dialog-detail", `已配好 ${models.length} 套，点一下就切过去（下一轮生效）`));
  const row = node("div", "cfg-model-list");
  models.forEach((name) => {
    const cur = name === current;
    const btn = node("button", `btn btn-sm ${cur ? "btn-primary" : "btn-outline-secondary"}`, name);
    btn.type = "button";
    btn.disabled = cur;
    btn.title = cur ? "现在用的就是这套" : `切到 ${name}`;
    btn.addEventListener("click", () => switchModel(name));
    row.append(btn);
  });
  el.cfgSwitch.append(row);
}

// switchModel 切到另一套已经配好的模型：写回配置由服务端做，回来只刷顶栏和表单。
// 切完重新拉一遍表单，是因为上面那些输入框得换成新那套的值（不然看着像没切）
async function switchModel(name) {
  try {
    const meta = await api("/api/model", { method: "POST", body: JSON.stringify({ model: name }) });
    if (meta && meta.model) applyMeta(meta);
    await openConfigDialog("edit");
    notice(`已切到 ${name}`);
  } catch (err) {
    showConfigError(`切换失败：${err.message}`);
  }
}

// fillSelect 把一组可选项填进下拉框，并选中 want 对应的那个
function fillSelect(select, items, pair, want) {
  select.replaceChildren();
  items.forEach((item) => {
    const [label, value] = pair(item);
    const opt = node("option", null, label);
    opt.value = value;
    select.append(opt);
  });
  if (want !== undefined && want !== null) select.value = want;
}

function showConfigError(msg) {
  el.cfgError.textContent = msg || "";
  el.cfgError.hidden = !msg;
}

// fetchModelNames 按表单里的地址和密钥从接口拉模型列表，拉回来摆成一排按钮让人点
async function fetchModelNames() {
  const url = el.cfgUrl.value.trim();
  const key = el.cfgKey.value.trim();
  el.cfgFetch.disabled = true;
  el.cfgFetch.textContent = "拉取中…";
  showConfigError("");
  try {
    const params = new URLSearchParams({ url, key });
    const data = await api(`/api/config/models?${params.toString()}`);
    const names = (data && data.models) || [];
    el.cfgModels.replaceChildren();
    if (names.length === 0) {
      el.cfgModels.hidden = true;
      showConfigError("接口没返回任何模型名，可以直接手填");
      return;
    }
    el.cfgModels.hidden = false;
    el.cfgModels.append(node("div", "dialog-detail", `共 ${names.length} 个，点一个填进模型名`));
    const row = node("div", "cfg-model-list");
    names.forEach((name) => {
      const btn = node("button", "btn btn-outline-secondary btn-sm", name);
      btn.type = "button";
      btn.addEventListener("click", () => {
        el.cfgModel.value = name;
        el.cfgModels.hidden = true;
      });
      row.append(btn);
    });
    el.cfgModels.append(row);
  } catch (err) {
    showConfigError(`拉不到模型列表：${err.message}`);
  } finally {
    el.cfgFetch.disabled = false;
    el.cfgFetch.textContent = "拉列表";
  }
}

// saveConfigForm 把表单交上去。密钥留空就是"不改"——前端拿不到明文，只能这么表达。
// 新增模式下 new 为 true、edit_label 为空；改现有那套时带着正在改的那套的名字
async function saveConfigForm() {
  el.cfgSave.disabled = true;
  showConfigError("");
  try {
    const data = await api("/api/config", {
      method: "POST",
      body: JSON.stringify({
        name: el.cfgName.value.trim(),
        edit_label: state.configEditLabel || "",
        new: state.configMode === "new",
        type: el.cfgType.value,
        api_url: el.cfgUrl.value.trim(),
        api_key: el.cfgKey.value.trim(),
        model: el.cfgModel.value.trim(),
        context_limit: el.cfgLimit.value.trim(),
        effort: el.cfgEffort.value,
      }),
    });
    if (data && data.meta) applyMeta(data.meta);
    if (data && data.config) applyConfigView(data.config);
    el.cfgDialog.close();
    notice("模型配置已保存，下一轮就用新的");
  } catch (err) {
    showConfigError(err.message);
  } finally {
    el.cfgSave.disabled = false;
  }
}

// runConfirm 弹确认框：用户点了确定才真的做那件事。
// 现在只有删模型走这条路，动作名形如 "del-model:名字"
async function runConfirm(reply) {
  const answer = await askConfirm({
    title: "确认",
    text: reply.text || "确定要这么做吗？",
    okLabel: "确定",
  });
  if (answer !== "ok") return;
  const action = reply.confirm || "";
  if (!action.startsWith("del-model:")) return;
  const name = action.slice("del-model:".length);
  try {
    const data = await api(`/api/config/models?name=${encodeURIComponent(name)}`, { method: "DELETE" });
    if (data && data.meta) applyMeta(data.meta);
    notice(`已删掉 ${name}`);
  } catch (err) {
    notice(`删不掉：${err.message}`, true);
  }
}

// deleteCurrentModel 是配置表单里的「删除这套」：先弹确认框，点了确定才真删
async function deleteCurrentModel() {
  const name = (state.configEditLabel || el.cfgName.value || "").trim();
  if (!name) {
    showConfigError("还没配过模型，没有能删的");
    return;
  }
  const answer = await askConfirm({
    title: "删除模型配置",
    text: `删掉 ${name}？`,
    okLabel: "删掉",
  });
  if (answer !== "ok") return;
  try {
    const data = await api(`/api/config/models?name=${encodeURIComponent(name)}`, { method: "DELETE" });
    if (data && data.meta) applyMeta(data.meta);
    el.cfgDialog.close();
    notice(`已删掉 ${name}`);
  } catch (err) {
    showConfigError(`删不掉：${err.message}`);
  }
}
function bindEvents() {
  el.composer.addEventListener("submit", (e) => {
    e.preventDefault();
    submit();
  });
  el.input.addEventListener("keydown", (e) => {
    // 候选开着的时候键先归候选：↑↓ 换、Tab / Enter 补全、Esc 收起
    if (state.suggest.length > 0 && !e.isComposing) {
      switch (e.key) {
        case "ArrowDown":
          e.preventDefault();
          moveSuggest(1);
          return;
        case "ArrowUp":
          e.preventDefault();
          moveSuggest(-1);
          return;
        case "Tab":
          e.preventDefault();
          acceptSuggest();
          return;
        case "Escape":
          e.preventDefault();
          hideSuggest();
          return;
        case "Enter":
          if (!e.shiftKey) {
            // 命令还没打完，回车先当成补全，别把半截 "/co" 发出去
            e.preventDefault();
            acceptSuggest();
            return;
          }
          break;
        default:
          break;
      }
    }
    // Enter 发送、Shift+Enter 换行。拼音输入法选词时的 Enter 不算
    if (e.key === "Enter" && !e.shiftKey && !e.isComposing) {
      e.preventDefault();
      submit();
      return;
    }
    // Esc 清空附图：和 CLI 一样，攒错了按一下就撤
    if (e.key === "Escape" && state.images.length > 0) {
      e.preventDefault();
      state.images = [];
      renderAttachments();
    }
  });
  el.input.addEventListener("input", () => {
    resizeInput();
    refreshSuggest();
    saveDraft();
  });
  el.input.addEventListener("blur", () => {
    // 点到别处就收起候选：留着会挡住下面的按钮
    setTimeout(hideSuggest, 120);
  });
  el.input.addEventListener("paste", (e) => {
    const items = (e.clipboardData && e.clipboardData.items) || [];
    let took = false;
    for (const item of items) {
      if (item.kind === "file" && item.type.startsWith("image/")) {
        const file = item.getAsFile();
        if (file) {
          addImageFile(file);
          took = true;
        }
      }
    }
    if (took) e.preventDefault();
  });
  el.file.addEventListener("change", () => {
    Array.from(el.file.files || []).forEach(addImageFile);
    el.file.value = "";
  });
  el.cancel.addEventListener("click", async () => {
    if (!state.sessionId) return;
    setStatus("正在中断…");
    try {
      await api(`/api/sessions/${state.sessionId}/cancel`, { method: "POST" });
    } catch (err) {
      notice(`中断不了：${err.message}`, true);
    }
  });
  el.mode.addEventListener("change", async () => {
    try {
      applyMeta(await api("/api/mode", { method: "POST", body: JSON.stringify({ mode: el.mode.value }) }));
      notice(`工作模式已切到「${el.mode.options[el.mode.selectedIndex].text}」`);
    } catch (err) {
      notice(`切模式失败：${err.message}`, true);
      loadMeta();
    }
  });
  $("new-session").addEventListener("click", newSession);

  // 顶栏那两枚徽标点开就是配置表单：模型、思考强度都在里面改，
  // 省得为了换个档位去敲 /config
  for (const badge of [el.badgeModel, el.badgeEffort]) {
    // title 和光标由 HTML/CSS 给（模型那枚的 title 里带当前模型名，在 applyMeta 里更新）
    badge.addEventListener("click", openConfigDialog);
  }
  el.badgeQuiet.addEventListener("click", () => toggleQuiet(!state.quiet));

  // /config 表单：切类型时地址跟着换成该类型的内置地址（手填过的地址不动），
  // 其余就是拉列表、保存、取消
  el.cfgType.addEventListener("change", () => {
    const defaults = state.configDefaults || {};
    const cur = el.cfgUrl.value.trim();
    const known = Object.values(defaults);
    // 空着、或者还是某个类型的内置地址 → 换成新类型的；手填过的一律不动
    if (!cur || known.includes(cur)) {
      el.cfgUrl.value = defaults[el.cfgType.value] || "";
    }
    // 档位表也按类型换：DeepSeek 有 none/xhigh/ultra/max，另两家没有。
    // 原来那档新类型不认就落回"默认"（fillEfforts 里处理）
    const levels = state.configEfforts || {};
    fillEfforts(levels[el.cfgType.value] || [], el.cfgEffort.value);
  });
  el.cfgFetch.addEventListener("click", fetchModelNames);
  el.cfgSave.addEventListener("click", saveConfigForm);
  el.cfgNew.addEventListener("click", () => openConfigDialog("new"));
  el.cfgDelete.addEventListener("click", deleteCurrentModel);
  el.cfgCancel.addEventListener("click", () => el.cfgDialog.close());
  el.cfgDialog.addEventListener("cancel", (e) => {
    // Esc 关掉表单只是放弃这次修改，没有副作用，放它过去
    e.preventDefault();
    el.cfgDialog.close();
  });

  // 审批弹窗：Esc 不直接关掉，免得手滑按一下就当成拒绝
  el.approval.addEventListener("cancel", (e) => e.preventDefault());
  el.approvalAllow.addEventListener("click", () => answerApproval({ allow: true }));
  el.approvalAlways.addEventListener("click", () => answerApproval({ allow: true, always: true }));
  el.approvalDeny.addEventListener("click", () =>
    answerApproval({ allow: false, feedback: el.approvalFeedback.value }),
  );
  el.approvalFeedback.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !e.isComposing) {
      e.preventDefault();
      answerApproval({ allow: false, feedback: el.approvalFeedback.value });
    }
  });

  // 添加工作区：选择器里的浏览、跳转、新建和选择
  el.wsCancel.addEventListener("click", () => el.wsDialog.close());
  el.wsAdd.addEventListener("click", addWorkspace);
  el.wsUp.addEventListener("click", () => loadDirs(state.fs.parent || ""));
  el.wsRoots.addEventListener("click", () => loadDirs(""));
  el.wsHome.addEventListener("click", () => {
    if (state.fs.home) loadDirs(state.fs.home);
  });
  el.wsNew.addEventListener("click", newFolder);
  el.wsInput.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !e.isComposing) {
      e.preventDefault();
      loadDirs(el.wsInput.value.trim());
    }
  });
  el.wsNewName.addEventListener("keydown", (e) => {
    if (e.key === "Enter" && !e.isComposing) {
      e.preventDefault();
      createFolder();
    } else if (e.key === "Escape") {
      // 只收起这一行，别把整个弹窗关了
      e.preventDefault();
      el.wsNewFolder.hidden = true;
    }
  });
}

// blockContextMenu 禁掉浏览器默认的右键菜单。这个页面是个应用，不是网页：
// 右键弹出来的"另存为 / 查看源代码 / 检查"除了打断操作没别的用。
//
// 输入框和可编辑区例外——那里右键粘贴最顺手，禁了等于跟人作对。
// 想复制工具输出、回答里的文字：选中之后按 Ctrl+C，浏览器自带的复制不受影响。
//
// 挂在 document 上而不是各个元素上，所以只在启动时绑一次（见 boot）。
// 新长出来的东西（流式回答、工具结果）自动也在范围内。
function blockContextMenu() {
  document.addEventListener("contextmenu", (e) => {
    const t = e.target;
    if (t && (t.tagName === "INPUT" || t.tagName === "TEXTAREA" || t.isContentEditable)) {
      return;
    }
    e.preventDefault();
  });
}

// startRuntimePoll 定时刷待办清单和后台任务。后台任务是外面起的东西，
// 它自己跑完不会发事件过来，只能靠定时看一眼；页面在后台时跳过，省点开销
function startRuntimePoll() {
  setInterval(() => {
    if (document.hidden) return;
    if (state.sessionId) loadRuntime();
  }, 2000);
}

async function boot() {
  bindEvents();
  blockContextMenu();
  await loadMeta();
  // 命令表要赶在用户第一次打 / 之前拿到，不然候选是空的（拉失败也不影响别的）
  await loadCompletions();
  resizeInput();
  startWatchdog();
  startRuntimePoll();

  await loadWorkspaces();
  // 上次选的工作区；没有就用服务端标的默认那个
  const savedWs = localStorage.getItem("agent-dev.workspace");
  const target = state.workspaces.find((w) => w.id === savedWs) || currentWorkspace();
  if (!target) {
    setConn("idle");
    el.chat.append(node("div", "notice err", "连不上服务端，或者还没有可用的工作区"));
    return;
  }
  await selectWorkspace(target.id);
}

boot();
