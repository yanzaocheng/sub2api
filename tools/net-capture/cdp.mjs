#!/usr/bin/env node
/**
 * cdp.mjs —— 通过 Chrome DevTools Protocol 驱动你自己启动的浏览器
 *
 * 前提：先用 launch-chrome.ps1 启动一个带 --remote-debugging-port 的 Chrome。
 * 沙箱里我起不了 Chrome（Windows 上 Chrome 多进程 IPC 走命名管道，被禁），
 * 但 loopback TCP 是通的，所以我能连上你起的那个。
 *
 * 用法：
 *   node cdp.mjs status                       CDP 通不通
 *   node cdp.mjs tabs                         列出页面标签
 *   node cdp.mjs nav https://claude.ai/       导航到某页
 *   node cdp.mjs eval "document.title"        在页面里执行 JS
 *   node cdp.mjs capture --seconds 90 --out reports\claude.har
 *   node cdp.mjs raw <substr>                 dump 匹配到的请求/响应全文
 *
 * capture 产出的是**标准 HAR 1.2**，所以 analyze-har.mjs 可以直接吃：
 *   node analyze-har.mjs reports\claude.har --md reports\claude.md
 */

import fs from 'node:fs';
import path from 'node:path';

const CDP = process.env.CDP_URL || 'http://127.0.0.1:9222';

// ------------------------------------------------------------------ CLI

const argv = process.argv.slice(2);
const cmd = argv[0];
const flag = (name, def = null) => {
  const i = argv.indexOf('--' + name);
  return i >= 0 && argv[i + 1] && !argv[i + 1].startsWith('--') ? argv[i + 1] : def;
};
const has = (name) => argv.includes('--' + name);

if (!cmd) {
  console.log(`用法:
  node cdp.mjs status
  node cdp.mjs tabs
  node cdp.mjs nav <url>
  node cdp.mjs eval "<js>"
  node cdp.mjs capture [--seconds N] [--out file.har] [--match substr] [--all] [--max-body N]
  node cdp.mjs raw <substr>

环境变量 CDP_URL 可覆盖端点，默认 ${CDP}`);
  process.exit(1);
}

const die = (msg) => { console.error(msg); process.exit(1); };

// ------------------------------------------------------------------ CDP 基础

async function httpJson(p) {
  try {
    const r = await fetch(`${CDP}${p}`, { signal: AbortSignal.timeout(4000) });
    if (!r.ok) return null;
    return await r.json();
  } catch { return null; }
}

const listTargets = () => httpJson('/json/list');

class Session {
  constructor(ws) { this.ws = ws; this.seq = 0; this.pending = new Map(); this.handlers = new Map(); }

  static connect(wsUrl) {
    return new Promise((resolve, reject) => {
      const ws = new WebSocket(wsUrl);
      const to = setTimeout(() => reject(new Error('WebSocket 连接超时')), 8000);
      ws.addEventListener('open', () => {
        clearTimeout(to);
        const s = new Session(ws);
        ws.addEventListener('message', (ev) => s._onMessage(ev.data));
        ws.addEventListener('close', () => s._onClose());
        resolve(s);
      });
      ws.addEventListener('error', () => { clearTimeout(to); reject(new Error('WebSocket 连接失败')); });
    });
  }

  _onMessage(data) {
    let msg; try { msg = JSON.parse(data); } catch { return; }
    if (msg.id != null) {
      const p = this.pending.get(msg.id);
      if (!p) return;
      this.pending.delete(msg.id);
      msg.error ? p.reject(new Error(msg.error.message)) : p.resolve(msg.result);
      return;
    }
    for (const fn of this.handlers.get(msg.method) || []) {
      try { fn(msg.params); } catch (e) { console.error('handler error:', e.message); }
    }
  }

  _onClose() { for (const p of this.pending.values()) p.reject(new Error('CDP 连接已关闭')); this.pending.clear(); }

  send(method, params = {}) {
    const id = ++this.seq;
    return new Promise((resolve, reject) => {
      this.pending.set(id, { resolve, reject });
      this.ws.send(JSON.stringify({ id, method, params }));
      setTimeout(() => {
        if (this.pending.has(id)) { this.pending.delete(id); reject(new Error(`${method} 超时`)); }
      }, 30000);
    });
  }

  on(method, fn) {
    if (!this.handlers.has(method)) this.handlers.set(method, []);
    this.handlers.get(method).push(fn);
  }

  close() { try { this.ws.close(); } catch { } }
}

/** 找到要操作的页面：优先 URL 含 match 的，否则第一个真实页面 */
async function pickPage(match) {
  const targets = await listTargets();
  if (!targets) die(`连不上 CDP (${CDP})。先在你自己的终端里跑:\n  .\\launch-chrome.ps1`);
  const pages = targets.filter((t) => t.type === 'page' && t.url && !t.url.startsWith('devtools://'));
  if (pages.length === 0) die('没有可用的页面标签。');
  if (match) {
    const hit = pages.find((p) => p.url.includes(match));
    if (!hit) {
      console.error(`没有 URL 含 "${match}" 的标签。现有：`);
      for (const p of pages) console.error(`  ${p.url}`);
      process.exit(1);
    }
    return hit;
  }
  return pages[0];
}

// ------------------------------------------------------------------ 命令

async function cmdStatus() {
  const v = await httpJson('/json/version');
  if (!v) die(`CDP 不可用: ${CDP}\n先跑 .\\launch-chrome.ps1`);
  console.log('CDP 可用 ✓');
  console.log('  Browser :', v.Browser);
  console.log('  Protocol:', v['Protocol-Version']);
  console.log('  UA      :', v['User-Agent']);
  const t = await listTargets();
  console.log('  页面数  :', t.filter((x) => x.type === 'page').length);
}

async function cmdTabs() {
  const targets = await listTargets();
  if (!targets) die(`CDP 不可用: ${CDP}`);
  const pages = targets.filter((t) => t.type === 'page');
  pages.forEach((p, i) => console.log(`[${i}] ${p.title || '(无标题)'}\n     ${p.url}\n     id=${p.id}`));
}

async function cmdNav(url) {
  if (!url) die('用法: node cdp.mjs nav <url>');
  const page = await pickPage(flag('match', 'claude.ai'));
  const s = await Session.connect(page.webSocketDebuggerUrl);
  await s.send('Page.enable');
  await s.send('Page.navigate', { url });
  console.log('已导航到', url);
  s.close();
}

async function cmdEval(expr) {
  if (!expr) die('用法: node cdp.mjs eval "<js>"');
  const page = await pickPage(flag('match', 'claude.ai'));
  const s = await Session.connect(page.webSocketDebuggerUrl);
  const r = await s.send('Runtime.evaluate', {
    expression: expr, returnByValue: true, awaitPromise: true, userGesture: true,
  });
  s.close();
  if (r.exceptionDetails) die('页面抛错: ' + (r.exceptionDetails.exception?.description || r.exceptionDetails.text));
  const v = r.result?.value;
  console.log(typeof v === 'string' ? v : JSON.stringify(v, null, 2));
}

async function cmdRaw(substr) {
  if (!substr) die('用法: node cdp.mjs raw <url 子串>');
  const page = await pickPage(flag('match', 'claude.ai'));
  const s = await Session.connect(page.webSocketDebuggerUrl);
  const hits = [];
  s.on('Network.requestWillBeSent', (p) => { if (p.request.url.includes(substr)) hits.push({ req: p, res: null, body: null }); });
  s.on('Network.responseReceived', (p) => {
    const h = hits.find((x) => x.req.requestId === p.requestId); if (h) h.res = p;
  });
  s.on('Network.loadingFinished', async (p) => {
    const h = hits.find((x) => x.req.requestId === p.requestId); if (!h) return;
    try { h.body = (await s.send('Network.getResponseBody', { requestId: p.requestId })).body; } catch { }
  });
  await s.send('Network.enable', { maxPostDataSize: 1 << 20 });
  console.log(`监听 ${flag('seconds', '25')} 秒，等 URL 含 "${substr}" 的请求...`);
  await new Promise((r) => setTimeout(r, Number(flag('seconds', 25)) * 1000));
  s.close();
  if (!hits.length) return console.log('没抓到匹配的请求。');
  for (const h of hits) {
    console.log('\n' + '='.repeat(70));
    console.log(h.req.request.method, h.req.request.url);
    console.log('-- 请求头 --'); console.log(JSON.stringify(h.req.request.headers, null, 2));
    if (h.req.request.postData) { console.log('-- 请求体 --'); console.log(h.req.request.postData.slice(0, 4000)); }
    if (h.res) { console.log('-- 响应头 --'); console.log(JSON.stringify(h.res.response.headers, null, 2)); }
    if (h.body) { console.log('-- 响应体 --'); console.log(h.body.slice(0, 4000)); }
  }
}

/** 把 CDP 的 headers 对象转成 HAR 的 [{name,value}] */
const toHarHeaders = (h) => Object.entries(h || {}).map(([name, value]) => ({ name, value: String(value) }));

const SKIP_TYPES = new Set(['Image', 'Font', 'Media', 'Manifest', 'Prefetch', 'SignedExchange']);

async function cmdCapture() {
  const seconds = Number(flag('seconds', 60));
  const out = flag('out', path.join('reports', `cdp-${Date.now()}.har`));
  const match = flag('match', 'claude.ai');
  const maxBody = Number(flag('max-body', 512 * 1024));
  const all = has('all');

  const page = await pickPage(match);
  console.log(`附着: ${page.title || '(无标题)'}\n      ${page.url}`);

  const s = await Session.connect(page.webSocketDebuggerUrl);
  const recs = new Map();
  let kept = 0, dropped = 0;

  s.on('Network.requestWillBeSent', (p) => {
    // 静态二进制资源默认丢弃，但**带查询串的保留** —— 统计像素 / 打点就是
    // 1x1 gif 加一堆参数，恰恰是最需要看到的东西，不能一刀切按类型丢。
    const hasQuery = p.request.url.includes('?');
    if (!all && SKIP_TYPES.has(p.type) && !hasQuery) { dropped++; return; }
    kept++;
    recs.set(p.requestId, {
      id: p.requestId, type: p.type, t0: p.timestamp, wall: p.wallTime,
      req: p.request, res: null, body: null, b64: false, truncated: false, failed: null, t1: null,
    });
  });
  s.on('Network.responseReceived', (p) => { const r = recs.get(p.requestId); if (r) r.res = p.response; });
  s.on('Network.loadingFinished', async (p) => {
    const r = recs.get(p.requestId); if (!r) return;
    r.t1 = p.timestamp;
    try {
      const b = await s.send('Network.getResponseBody', { requestId: p.requestId });
      const text = b.base64Encoded ? Buffer.from(b.body, 'base64').toString('utf8') : b.body;
      if (text.length > maxBody) { r.body = text.slice(0, maxBody); r.truncated = true; }
      else r.body = text;
      r.b64 = b.base64Encoded;
    } catch { /* body 可能已被丢弃（跳转/流式/太大） */ }
  });
  s.on('Network.loadingFailed', (p) => { const r = recs.get(p.requestId); if (r) r.failed = p.errorText; });

  await s.send('Network.enable', { maxPostDataSize: 1 << 20 });
  console.log(`录制中... ${seconds}s（按 Ctrl+C 可提前结束并落盘）`);

  let stopped = false;
  const finish = () => {
    if (stopped) return; stopped = true;
    s.close();
    const entries = [];
    for (const r of recs.values()) {
      if (!r.res && !r.failed) continue;
      // 体积大的图片/字体/媒体再丢一次（打点像素只有几十字节，会活下来）
      if (!all && SKIP_TYPES.has(r.type) && (r.body?.length || 0) > 65536) { dropped++; continue; }
      const dur = r.t1 ? Math.round((r.t1 - r.t0) * 1000) : 0;
      entries.push({
        startedDateTime: new Date((r.wall || Date.now() / 1000) * 1000).toISOString(),
        time: dur,
        _resourceType: (r.type || 'other').toLowerCase(),
        _truncated: r.truncated || undefined,
        _failed: r.failed || undefined,
        request: {
          method: r.req.method, url: r.req.url,
          httpVersion: 'HTTP/1.1',
          headers: toHarHeaders(r.req.headers),
          queryString: [...new URL(r.req.url).searchParams].map(([name, value]) => ({ name, value })),
          cookies: [],
          headersSize: -1,
          bodySize: r.req.postData ? r.req.postData.length : 0,
          postData: r.req.postData ? { mimeType: (r.req.headers || {})['content-type'] || '', text: r.req.postData } : undefined,
        },
        response: {
          status: r.res?.status ?? 0,
          statusText: r.res?.statusText || (r.failed ? 'FAILED' : ''),
          httpVersion: r.res?.protocol || 'HTTP/1.1',
          headers: toHarHeaders(r.res?.headers),
          cookies: [],
          content: {
            size: r.body ? r.body.length : 0,
            mimeType: r.res?.mimeType || '',
            text: r.body ?? undefined,
          },
          redirectURL: r.res?.headers?.location || '',
          headersSize: -1,
          bodySize: r.body ? r.body.length : -1,
        },
        cache: {},
        timings: { send: 0, wait: dur, receive: 0 },
      });
    }

    const har = {
      log: {
        version: '1.2',
        creator: { name: 'sub2api-cdp', version: '1.0' },
        pages: [{ startedDateTime: new Date().toISOString(), id: 'page_1', title: page.title || '', pageTimings: {} }],
        entries,
      },
    };
    fs.mkdirSync(path.dirname(path.resolve(out)), { recursive: true });
    fs.writeFileSync(out, JSON.stringify(har, null, 1), 'utf8');
    console.log(`\n抓到 ${kept} 个请求（跳过 ${dropped} 个图片/字体/媒体），落盘 ${entries.length} 条`);
    console.log(`HAR: ${path.resolve(out)}`);
    console.log(`\n下一步:\n  node analyze-har.mjs "${out}" --md reports\\cdp.md`);
    process.exit(0);
  };

  process.on('SIGINT', finish);
  setTimeout(finish, seconds * 1000);
}

// ------------------------------------------------------------------ 分发

try {
  if (cmd === 'status') await cmdStatus();
  else if (cmd === 'tabs') await cmdTabs();
  else if (cmd === 'nav') await cmdNav(argv[1]);
  else if (cmd === 'eval') await cmdEval(argv[1]);
  else if (cmd === 'raw') await cmdRaw(argv[1]);
  else if (cmd === 'capture') await cmdCapture();
  else die(`未知命令: ${cmd}`);
} catch (e) {
  die('错误: ' + e.message);
}
