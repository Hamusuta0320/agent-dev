// agent-dev 的浏览器界面：把模型输出的 markdown 排版出来。
//
// 这个文件只做一件事：一段 markdown 文本 → 一串 DOM 节点。
//
// 为什么自己写：项目不引外部前端库（static/vendor 里只有 Bootstrap，且不能引 CDN），
// 而现成的 markdown 库基本都吐 HTML 字符串，要上屏就得 innerHTML —— 那等于把
// 模型输出（以及它读到的仓库文件内容）当 HTML 执行，是 app.js 开头那条原则明确禁止的。
// 所以这里全程 createElement / createTextNode，一个 innerHTML 都没有：
// 文本里写 <script>alert(1)</script> 只会原样显示成文字。
//
// 支持的语法（够模型输出用了，不追求 CommonMark 全兼容）：
//   块级：围栏代码块（``` / ~~~，带语言标签）、ATX 标题、水平线、引用、有序/无序列表
//         （可嵌套）、表格（含对齐）、段落（软换行保留成 <br>）
//   行内：行内代码、粗体、斜体、删除线、链接、裸链接、反斜杠转义
"use strict";

const Markdown = (() => {
  // 反斜杠后面跟这些字符时表示"这是字面量"（和 markdown 的约定一致）
  const ESCAPABLE = "\\`*_{}[]()#+-.!~>|";

  function el(tag, className) {
    const n = document.createElement(tag);
    if (className) n.className = className;
    return n;
  }

  function text(s) {
    return document.createTextNode(s);
  }

  /* ---------------- 对外入口 ---------------- */

  // fragment 返回一个 DocumentFragment，调用方直接 append 进容器
  function fragment(src) {
    const frag = document.createDocumentFragment();
    const lines = String(src == null ? "" : src).replace(/\r\n?/g, "\n").split("\n");
    blocks(frag, lines);
    return frag;
  }

  // into 把 markdown 渲染进一个已有的元素（先清空）
  function into(target, src) {
    if (!target) return;
    target.replaceChildren(fragment(src));
  }

  /* ---------------- 块级 ---------------- */

  function blocks(out, lines) {
    let i = 0;
    while (i < lines.length) {
      const line = lines[i];
      if (!line.trim()) {
        i++;
        continue;
      }

      const fence = fenceOpen(line);
      if (fence) {
        i = codeBlock(out, lines, i, fence);
        continue;
      }

      const h = /^\s{0,3}(#{1,6})\s+(.*?)\s*#*\s*$/.exec(line);
      if (h) {
        const node = el("h" + h[1].length);
        inline(node, h[2]);
        out.append(node);
        i++;
        continue;
      }

      if (/^\s{0,3}([-*_])(?:\s*\1){2,}\s*$/.test(line)) {
        out.append(el("hr"));
        i++;
        continue;
      }

      if (/^\s{0,3}>/.test(line)) {
        const body = [];
        while (i < lines.length && /^\s{0,3}>/.test(lines[i])) {
          body.push(lines[i].replace(/^\s{0,3}>\s?/, ""));
          i++;
        }
        const quote = el("blockquote");
        blocks(quote, body);
        // 空引用也要有个占位，不然渲染出来是空的
        if (!quote.firstChild) quote.append(text(""));
        out.append(quote);
        continue;
      }

      if (isTableStart(lines, i)) {
        i = table(out, lines, i);
        continue;
      }

      if (listMarker(line)) {
        i = list(out, lines, i);
        continue;
      }

      i = paragraph(out, lines, i);
    }
  }

  function fenceOpen(line) {
    const m = /^\s{0,3}(`{3,}|~{3,})\s*([^\s`]*)\s*$/.exec(line);
    if (!m) return null;
    return { marker: m[1], lang: m[2] };
  }

  // codeBlock 收一整段围栏代码：内容原样（不解析任何标记），带语言标签和复制按钮
  function codeBlock(out, lines, start, fence) {
    const body = [];
    let i = start + 1;
    while (i < lines.length && !isFenceClose(lines[i], fence.marker)) {
      body.push(lines[i]);
      i++;
    }
    if (i < lines.length) i++; // 吃掉闭合那一行
    out.append(codeBlockEl(body.join("\n"), fence.lang));
    return i;
  }

  function isFenceClose(line, marker) {
    const close = marker[0] === "`" ? "`" : "~";
    const re = new RegExp("^\\s{0,3}" + close + "{" + marker.length + ",}\\s*$");
    return re.test(line);
  }

  function codeBlockEl(code, lang) {
    const box = el("div", "md-pre");
    const bar = el("div", "md-pre-bar");
    const tag = el("span", "md-lang");
    if (lang) {
      tag.textContent = lang;
      bar.append(tag);
    }
    const copy = el("button", "md-copy", "复制");
    copy.type = "button";
    copy.addEventListener("click", () => copyText(copy, code));
    bar.append(copy);
    const pre = el("pre");
    const codeEl = el("code");
    codeEl.textContent = code;
    pre.append(codeEl);
    box.append(bar, pre);
    return box;
  }

  // copyText 把文本放进剪贴板，并给按钮一句反馈。剪贴板 API 只在安全上下文
  // （https / localhost）里有，拿不到就退回"按 Ctrl+C"——别让人以为点了没反应。
  // 正文块和代码块共用这一份，两处的反馈行为才不会各走一套
  function copyText(button, content) {
    const done = (ok) => {
      button.textContent = ok ? "已复制" : "按 Ctrl+C";
      setTimeout(() => {
        button.textContent = "复制";
      }, 1400);
    };
    if (navigator.clipboard && navigator.clipboard.writeText) {
      navigator.clipboard.writeText(content).then(() => done(true), () => done(false));
      return;
    }
    done(false);
  }

  function isTableStart(lines, i) {
    if (i + 1 >= lines.length) return false;
    if (!lines[i].includes("|")) return false;
    // 分隔行：只由 | - : 空格组成，且至少有一个 -
    return /^\s*\|?[\s:|-]*-[\s:|-]*$/.test(lines[i + 1]) && lines[i + 1].includes("|");
  }

  function splitRow(line) {
    let s = line.trim();
    if (s.startsWith("|")) s = s.slice(1);
    if (s.endsWith("|")) s = s.slice(0, -1);
    return s.split("|").map((cell) => cell.trim());
  }

  function table(out, lines, start) {
    const head = splitRow(lines[start]);
    const aligns = splitRow(lines[start + 1]).map((spec) => {
      const left = spec.startsWith(":");
      const right = spec.endsWith(":");
      if (left && right) return "center";
      if (right) return "right";
      if (left) return "left";
      return "";
    });
    const tableEl = el("table");
    const thead = el("thead");
    const headRow = el("tr");
    head.forEach((cell, k) => {
      const th = el("th");
      if (aligns[k]) th.style.textAlign = aligns[k];
      inline(th, cell);
      headRow.append(th);
    });
    thead.append(headRow);
    tableEl.append(thead);

    const tbody = el("tbody");
    let i = start + 2;
    while (i < lines.length && lines[i].trim() && lines[i].includes("|")) {
      const cells = splitRow(lines[i]);
      const row = el("tr");
      // 补齐/截断到表头宽度，缺的格子留空
      for (let k = 0; k < head.length; k++) {
        const td = el("td");
        if (aligns[k]) td.style.textAlign = aligns[k];
        inline(td, cells[k] == null ? "" : cells[k]);
        row.append(td);
      }
      tbody.append(row);
      i++;
    }
    tableEl.append(tbody);
    out.append(tableEl);
    return i;
  }

  function listMarker(line) {
    const m = /^([ \t]*)([-*+]|\d{1,9}[.)])\s+(.*)$/.exec(line);
    if (!m) return null;
    const indent = m[1].replace(/\t/g, "    ").length;
    const ordered = /\d/.test(m[2]);
    return {
      indent,
      ordered,
      start: ordered ? parseInt(m[2], 10) : 1,
      body: m[3],
    };
  }

  // list 收一整段列表。缩进更深的项目连同它的续行一起塞进上一项的正文里，
  // 递归渲染时就自然成了嵌套列表
  function list(out, lines, start) {
    const first = listMarker(lines[start]);
    const listEl = el(first.ordered ? "ol" : "ul");
    if (first.ordered && first.start !== 1) listEl.setAttribute("start", String(first.start));
    const base = first.indent;
    let i = start;

    while (i < lines.length) {
      const mk = listMarker(lines[i]);
      if (!mk || mk.indent !== base || mk.ordered !== first.ordered) break;

      const body = [mk.body];
      i++;
      while (i < lines.length) {
        const next = lines[i];
        const nested = listMarker(next);
        if (nested) {
          if (nested.indent > base) {
            body.push(next);
            i++;
            continue;
          }
          break;
        }
        if (!next.trim()) {
          // 空行：往后看还是不是同一层的项，是就结束当前项（项之间空行很常见）
          let j = i;
          while (j < lines.length && !lines[j].trim()) j++;
          if (j >= lines.length) {
            i = j;
            break;
          }
          const after = listMarker(lines[j]);
          if (after && after.indent >= base) {
            i = j;
            break;
          }
          break;
        }
        // 别的块（标题、围栏、引用、表格）开始，说明列表结束了
        if (/^\s{0,3}(#{1,6}\s|>|```|~~~)/.test(next)) break;
        // 续行：折行属于同一项
        body.push(next.trim());
        i++;
      }

      const li = el("li");
      blocks(li, body);
      listEl.append(li);
    }

    out.append(listEl);
    return i;
  }

  function startsBlock(line) {
    return (
      !!fenceOpen(line) ||
      /^\s{0,3}#{1,6}\s/.test(line) ||
      /^\s{0,3}>/.test(line) ||
      /^\s{0,3}([-*_])(?:\s*\1){2,}\s*$/.test(line) ||
      !!listMarker(line)
    );
  }

  function paragraph(out, lines, start) {
    const body = [];
    let i = start;
    while (i < lines.length) {
      const line = lines[i];
      if (!line.trim()) break;
      if (i > start && (startsBlock(line) || isTableStart(lines, i))) break;
      body.push(line.trim());
      i++;
    }
    const p = el("p");
    body.forEach((line, k) => {
      // markdown 里单换行是软换行：保留成 <br>，模型写的分行才不会被吃掉
      if (k > 0) p.append(el("br"));
      inline(p, line);
    });
    out.append(p);
    return i;
  }

  /* ---------------- 行内 ---------------- */

  const URL_OK = /^(https?:\/\/|mailto:)/i;

  // safeUrl 只放行 http / https / mailto 和页内锚点。
  // javascript: 之类的当普通文字显示，不做成能点的链接
  function safeUrl(raw) {
    const url = String(raw || "").trim();
    if (!url) return "";
    if (url.startsWith("#")) return url;
    return URL_OK.test(url) ? url : "";
  }

  function runLen(src, at, ch) {
    let n = 0;
    while (src[at + n] === ch) n++;
    return n;
  }

  // findRun 从 from 开始找连续 n 个 ch，返回起点（找不到返回 -1）
  function findRun(src, from, ch, n) {
    const needle = ch.repeat(n);
    const at = src.indexOf(needle, from);
    if (at < 0) return -1;
    // 后面不能再跟同样的字符，否则那是更长的围栏
    if (src[at + n] === ch) return findRun(src, at + 1, ch, n);
    return at;
  }

  // stripCodePad 按 markdown 的规矩去掉行内代码首尾各一个空格（` a ` → `a`）
  function stripCodePad(s) {
    if (s.length > 2 && s.startsWith(" ") && s.endsWith(" ") && s.trim()) {
      return s.slice(1, -1);
    }
    return s;
  }

  // emClose 找单个 * 或 _ 的闭合位置。找不到返回 -1：
  // 开闭紧贴的不能是空白，词中间的下划线不算强调（snake_case 不能被拆成斜体）
  function emClose(src, start, mark) {
    if (/\s/.test(src[start + 1] || "")) return -1;
    if (mark === "_" && /[\w]/.test(src[start - 1] || "")) return -1;
    for (let i = start + 1; i < src.length; i++) {
      if (src[i] === "\\") {
        i++;
        continue;
      }
      if (src[i] !== mark) continue;
      if (src[i + 1] === mark) {
        i++; // 那是粗体的闭合，跳过
        continue;
      }
      if (/\s/.test(src[i - 1] || "")) continue;
      if (i === start + 1) return -1;
      return i;
    }
    return -1;
  }

  function link(a, href, label) {
    a.setAttribute("href", href);
    // 新窗口打开：这个页面本身是个应用，跳走就把界面丢了
    a.setAttribute("target", "_blank");
    a.setAttribute("rel", "noopener noreferrer");
    inline(a, label);
  }

  function inline(parent, src) {
    const s = String(src == null ? "" : src);
    let i = 0;
    let buf = "";
    const flush = () => {
      if (buf) {
        parent.append(text(buf));
        buf = "";
      }
    };

    while (i < s.length) {
      const ch = s[i];

      // 反斜杠转义
      if (ch === "\\" && i + 1 < s.length && ESCAPABLE.includes(s[i + 1])) {
        buf += s[i + 1];
        i += 2;
        continue;
      }

      // 行内代码：内容原样，里面的 * _ ` 都不再解析
      if (ch === "`") {
        const n = runLen(s, i, "`");
        const close = findRun(s, i + n, "`", n);
        if (close > 0) {
          flush();
          const code = el("code");
          code.textContent = stripCodePad(s.slice(i + n, close));
          parent.append(code);
          i = close + n;
          continue;
        }
      }

      // 粗体
      if (s.startsWith("**", i) || s.startsWith("__", i)) {
        const mark = s.slice(i, i + 2);
        const close = s.indexOf(mark, i + 2);
        if (close > i + 2) {
          flush();
          const strong = el("strong");
          inline(strong, s.slice(i + 2, close));
          parent.append(strong);
          i = close + 2;
          continue;
        }
      }

      // 删除线
      if (s.startsWith("~~", i)) {
        const close = s.indexOf("~~", i + 2);
        if (close > i + 2) {
          flush();
          const strike = el("del");
          inline(strike, s.slice(i + 2, close));
          parent.append(strike);
          i = close + 2;
          continue;
        }
      }

      // 斜体
      if (ch === "*" || ch === "_") {
        const close = emClose(s, i, ch);
        if (close > 0) {
          flush();
          const em = el("em");
          inline(em, s.slice(i + 1, close));
          parent.append(em);
          i = close + 1;
          continue;
        }
      }

      // 链接 [文字](地址 "标题")
      if (ch === "[") {
        const m = /^\[([^\]]*)\]\(\s*([^)\s]+)(?:\s+"[^"]*")?\s*\)/.exec(s.slice(i));
        if (m) {
          flush();
          const href = safeUrl(m[2]);
          if (href) {
            const a = el("a");
            link(a, href, m[1] || m[2]);
            parent.append(a);
          } else {
            // 不安全的协议：原样当文字，不给点击
            buf += m[0];
          }
          i += m[0].length;
          continue;
        }
      }

      // 裸链接
      if (s.startsWith("http://", i) || s.startsWith("https://", i)) {
        const m = /^https?:\/\/[^\s<>()]+/.exec(s.slice(i));
        if (m) {
          flush();
          // 句末的标点不属于地址
          const url = m[0].replace(/[.,;:!?]+$/, "");
          const a = el("a");
          a.setAttribute("href", url);
          a.setAttribute("target", "_blank");
          a.setAttribute("rel", "noopener noreferrer");
          a.textContent = url;
          parent.append(a);
          i += url.length;
          continue;
        }
      }

      buf += ch;
      i++;
    }
    flush();
  }

  return { fragment, into, copyText };
})();
