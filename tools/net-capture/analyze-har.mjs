#!/usr/bin/env node
/**
 * analyze-har.mjs —— 把 Chrome/Firefox 导出的 HAR 文件解析成"人能看懂的请求地图"
 *
 * 用法:
 *   node analyze-har.mjs claude.ai.har
 *   node analyze-har.mjs claude.ai.har --md report.md --json report.json
 *   node analyze-har.mjs claude.ai.har --no-redact      # 默认会打码 cookie/authorization 等敏感值
 *   node analyze-har.mjs claude.ai.har --host claude.ai # 只看某个 host
 *
 * 它做什么:
 *   1. 按 host / 资源类型分桶
 *   2. 把 xhr|fetch 归并成"端点"（UUID、纯数字、长 token 会被归一化成占位符）
 *   3. 识别第三方遥测（Datadog RUM、Sentry、GA、Segment、统计像素等）
 *   4. 列出每个端点发出去/收回来的 header 名（值默认打码）
 *   5. 提取 POST body 的顶层字段名
 *   6. 标出查询串里的不透明标识符（长 base64/hex 串）
 *
 * 设计原则: 只读、不联网、不执行任何东西，纯文本分析。
 */

import fs from 'node:fs';
import path from 'node:path';

// ---------------------------------------------------------------- CLI 解析

const argv = process.argv.slice(2);
if (argv.length === 0 || argv.includes('-h') || argv.includes('--help')) {
  console.log(`用法: node analyze-har.mjs <file.har> [--md out.md] [--json out.json] [--host H] [--no-redact]`);
  process.exit(argv.length === 0 ? 1 : 0);
}

const opt = { file: null, md: null, json: null, host: null, redact: true };
for (let i = 0; i < argv.length; i++) {
  const a = argv[i];
  if (a === '--md') opt.md = argv[++i];
  else if (a === '--json') opt.json = argv[++i];
  else if (a === '--host') opt.host = argv[++i];
  else if (a === '--no-redact') opt.redact = false;
  else if (!opt.file) opt.file = a;
}
if (!opt.file) { console.error('缺少 HAR 文件路径'); process.exit(1); }

// ---------------------------------------------------------------- 常量表

/** 已知的第三方遥测/统计服务。命中即标为 telemetry。 */
const TELEMETRY_RULES = [
  [/datadoghq\.com|\/rum\?ddsource=|ddsource=browser/i, 'Datadog RUM / Browser Logs'],
  [/browser-intake-datadoghq|logs\.datadoghq/i, 'Datadog intake'],
  [/sentry\.io|ingest\.sentry/i, 'Sentry'],
  [/google-analytics\.com|\/g\/collect|googletagmanager/i, 'Google Analytics / GTM'],
  [/segment\.(io|com)|api\.segment/i, 'Segment'],
  [/mixpanel\.com/i, 'Mixpanel'],
  [/amplitude\.com/i, 'Amplitude'],
  [/posthog\.com/i, 'PostHog'],
  [/stats\.?g\.?doubleclick|doubleclick\.net/i, 'DoubleClick'],
  [/hotjar|fullstory|heap\.io|clarity\.ms/i, '行为录制'],
  [/\.gif\?|time_spent|statcounter/i, '统计像素 / 停留时长'],
  [/cloudflareinsights\.com|beacon\.min\.js/i, 'Cloudflare Web Analytics'],
  [/intercom|drift\.com|zendesk|salesforce/i, '客服组件'],
  [/stripe\.com\/v\d|js\.stripe/i, 'Stripe'],
  [/launchdarkly|optimizely|split\.io|statsig/i, '特性开关 / A-B 实验'],
];

/** 常见的"不透明标识符"查询参数名 */
const OP_ID_PARAMS = /^(ns7c|bk|r|v|cs|ft|dd-api-key|dd-cookie|batch_time|device_id|session_id|anon_id|distinct_id|client_id|visitor_id|_ga|gid|sid|uid|cid)$/i;

const SENSITIVE_HEADER = /cookie|authorization|api[-_]?key|token|secret|session|csrf|xsrf|set-cookie/i;

// ---------------------------------------------------------------- 工具

const enc = (s) => encodeURIComponent(String(s ?? ''));
const uniq = (arr) => [...new Set(arr)];

/** 把 URL path 里的高熵片段替换成占位符，让同类请求能归并到一起。 */
function normalizePath(p) {
  return p
    .replace(/[0-9a-f]{8}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{4}-[0-9a-f]{12}/gi, '{uuid}')
    .replace(/\b[0-9a-f]{16,}\b/gi, '{hex}')
    .replace(/\/\d+(?=\/|$)/g, '/{n}')
    .replace(/\b[A-Za-z0-9_-]{24,}\b/g, '{tok}');
}

/** 敏感 header 的值打码，但保留"长度+前缀"这种可比较的形态信息。 */
function maskValue(name, value) {
  if (!opt.redact) return value;
  if (!SENSITIVE_HEADER.test(name)) {
    // dd-api-key 是公开的浏览器端 token，但也一并打码，避免误传
    if (/dd-api-key/i.test(name)) return maskValue(name, value);
    return value;
  }
  if (!value) return value;
  if (value.length <= 12) return '<redacted:len=' + value.length + '>';
  return value.slice(0, 6) + '…<redacted:len=' + value.length + '>';
}

function safeJsonParse(s) { try { return JSON.parse(s); } catch { return null; } }

/** 尝试从 body 文本里抽出顶层字段名。 */
function bodyKeys(text) {
  if (!text) return null;
  const j = safeJsonParse(text);
  if (j && typeof j === 'object' && !Array.isArray(j)) return Object.keys(j);
  if (j && Array.isArray(j)) return [`[array len=${j.length}]`];
  return [`<非 JSON, ${text.length} 字节>`];
}

function classify(entry) {
  const rt = entry._resourceType;
  if (rt) return rt;
  const mime = entry.response?.content?.mimeType || '';
  const url = entry.request?.url || '';
  if (/json/.test(mime)) return 'xhr';
  if (/javascript|ecmascript/.test(mime)) return 'script';
  if (/css/.test(mime)) return 'stylesheet';
  if (/image/.test(mime)) return 'image';
  if (/font/.test(mime)) return 'font';
  if (/html/.test(mime)) return 'document';
  if (/\/api\/|graphql/.test(url)) return 'xhr';
  return 'other';
}

function telemetryLabel(url) {
  for (const [re, label] of TELEMETRY_RULES) if (re.test(url)) return label;
  return null;
}

const isApiLike = (c) => c === 'xhr' || c === 'fetch' || c === 'other';

// ---------------------------------------------------------------- 主流程

const stat = fs.statSync(opt.file);
const sizeMB = stat.size / 1048576;
if (sizeMB > 200) {
  console.warn(`⚠️  HAR 有 ${sizeMB.toFixed(0)} MB，JSON.parse 会很吃内存。`);
  console.warn(`   若报 "JavaScript heap out of memory"，换更大的堆重跑：`);
  console.warn(`   node --max-old-space-size=8192 analyze-har.mjs "${opt.file}"`);
  console.warn(`   或回 DevTools 先筛选 Domain / 去掉 image、font 再导出。\n`);
}
const raw = fs.readFileSync(opt.file, 'utf8');
const har = safeJsonParse(raw);
if (!har?.log?.entries) {
  console.error('这不是合法的 HAR（缺少 log.entries）。');
  console.error('提示：DevTools 要选 "Save all as HAR with content"，且导出文件应为 .har。');
  process.exit(1);
}

let entries = har.log.entries;
if (opt.host) entries = entries.filter((e) => {
  try { return new URL(e.request.url).hostname.includes(opt.host); } catch { return false; }
});

// --- 分桶
const byHost = new Map();      // host -> count
const byType = new Map();      // resourceType -> count
const endpoints = new Map();   // key -> {method,host,path,count,statuses,reqHeaders,reqKeys,params,bytes,telemetry,samples}
const telemetry = new Map();   // label -> {count,hosts,paths}
const idParams = new Map();    // param name -> Set(values)
const assets = new Map();      // host -> Map(path -> count)
const failures = [];           // 失败的请求（CDP 捕获的 _failed）

for (const e of entries) {
  const req = e.request || {};
  const res = e.response || {};
  let u; try { u = new URL(req.url); } catch { continue; }

  const type = classify(e);
  byHost.set(u.hostname, (byHost.get(u.hostname) || 0) + 1);
  byType.set(type, (byType.get(type) || 0) + 1);

  // 遥测
  const tel = telemetryLabel(req.url);
  if (tel) {
    const rec = telemetry.get(tel) || { count: 0, hosts: new Set(), paths: new Set() };
    rec.count++; rec.hosts.add(u.hostname); rec.paths.add(normalizePath(u.pathname));
    telemetry.set(tel, rec);
  }

  // 收集不透明标识符
  for (const [k, v] of u.searchParams) {
    if (OP_ID_PARAMS.test(k) || (v.length >= 20 && /^[A-Za-z0-9_\-=.]+$/.test(v))) {
      if (!idParams.has(k)) idParams.set(k, new Set());
      idParams.get(k).add(v);
    }
  }

  if (!isApiLike(type) && !tel) {
    // 纯静态资源：只统计数量与体积，不展开
    if (!assets.has(u.hostname)) assets.set(u.hostname, new Map());
    const m = assets.get(u.hostname);
    m.set(normalizePath(u.pathname), (m.get(normalizePath(u.pathname)) || 0) + 1);
    continue;
  }

  const key = `${req.method} ${u.hostname}${normalizePath(u.pathname)}`;
  const rec = endpoints.get(key) || {
    method: req.method, host: u.hostname, path: normalizePath(u.pathname),
    count: 0, statuses: new Map(), reqHeaders: new Map(), resHeaders: new Map(),
    reqKeys: new Set(), params: new Set(), bytes: 0, telemetry: tel, sample: req.url,
    mimes: new Set(),
  };
  rec.count++;
  if (e._failed) {
    rec.statuses.set(`FAILED(${e._failed})`, (rec.statuses.get(`FAILED(${e._failed})`) || 0) + 1);
    failures.push({ method: req.method, host: u.hostname, path: normalizePath(u.pathname), err: e._failed });
  } else {
    rec.statuses.set(res.status, (rec.statuses.get(res.status) || 0) + 1);
  }
  rec.bytes += res.content?.size || 0;
  if (res.content?.mimeType) rec.mimes.add(res.content.mimeType);
  for (const [k, v] of u.searchParams) rec.params.add(k);
  for (const h of req.headers || []) rec.reqHeaders.set(h.name, maskValue(h.name, h.value));
  for (const h of res.headers || []) rec.resHeaders.set(h.name, maskValue(h.name, h.value));
  for (const k of bodyKeys(req.postData?.text) || []) rec.reqKeys.add(k);
  endpoints.set(key, rec);
}

// ---------------------------------------------------------------- 输出

const L = [];
const p = (s = '') => L.push(s);
const fmtBytes = (n) => n > 1048576 ? (n / 1048576).toFixed(1) + ' MB' : n > 1024 ? (n / 1024).toFixed(1) + ' KB' : n + ' B';

p(`# HAR 分析报告`);
p();
p(`- 源文件: \`${path.basename(opt.file)}\``);
p(`- 抓取时间: ${har.log.entries[0]?.startedDateTime || '?'} → ${har.log.entries.at(-1)?.startedDateTime || '?'}`);
p(`- 条目数: **${entries.length}**${opt.host ? `（已过滤 host 包含 "${opt.host}"）` : ''}`);
p(`- Creator: ${har.log.creator?.name || '?'} ${har.log.creator?.version || ''}`);
p();

p(`## 1. Host 排行`);
p();
p(`| Host | 请求数 |`);
p(`|---|---:|`);
for (const [h, c] of [...byHost].sort((a, b) => b[1] - a[1])) p(`| ${h} | ${c} |`);
p();

p(`## 2. 资源类型分布`);
p();
p(`| 类型 | 数量 |`);
p(`|---|---:|`);
for (const [t, c] of [...byType].sort((a, b) => b[1] - a[1])) p(`| ${t} | ${c} |`);
p();

if (telemetry.size) {
  p(`## 3. 第三方遥测 / 统计（重点：这些不是产品功能）`);
  p();
  p(`| 服务 | 请求数 | Host | 路径 |`);
  p(`|---|---:|---|---|`);
  for (const [label, r] of [...telemetry].sort((a, b) => b[1].count - a[1].count)) {
    p(`| **${label}** | ${r.count} | ${[...r.hosts].join(', ')} | ${[...r.paths].join(', ')} |`);
  }
  p();
}

const apiLike = [...endpoints.values()].filter((e) => !e.telemetry)
  .sort((a, b) => b.count - a.count || b.bytes - a.bytes);

p(`## 4. 接口端点（xhr/fetch，已去掉第三方遥测）`);
p();
p(`共 ${apiLike.length} 个不同端点。`);
p();
p(`| # | 方法 | Host | 路径 | 次数 | 状态 | 响应体 | 查询参数 |`);
p(`|---:|---|---|---|---:|---|---:|---|`);
apiLike.forEach((e, i) => {
  p(`| ${i + 1} | ${e.method} | ${e.host} | \`${e.path}\` | ${e.count} | ${[...e.statuses.keys()].join('/')} | ${fmtBytes(e.bytes)} | ${[...e.params].join(', ') || '—'} |`);
});
p();

if (failures.length) {
  p(`## 4b. 失败的请求`);
  p();
  p(`| 方法 | Host | 路径 | 错误 |`);
  p(`|---|---|---|---|`);
  for (const f of failures) p(`| ${f.method} | ${f.host} | \`${f.path}\` | \`${f.err}\` |`);
  p();
}

p(`## 5. 端点细节（请求头 / 请求体字段）`);
p();
for (const e of apiLike) {
  p(`### \`${e.method} ${e.path}\``);
  p();
  p(`- 示例: \`${e.sample.replace(/([?&][^=]{0,24}=)[^&]{12,}/g, '$1<redacted>')}\``);
  p(`- 状态: ${[...e.statuses].map(([s, c]) => `${s}×${c}`).join(', ')}`);
  if (e.mimes.size) p(`- Content-Type: ${[...e.mimes].join(', ')}`);
  if (e.reqKeys.size) p(`- **POST body 顶层字段**: \`${[...e.reqKeys].join('`, `')}\``);
  p(`- 请求头: ${[...e.reqHeaders.keys()].join(', ')}`);
  const notable = [...e.reqHeaders].filter(([k]) => /^(x-|anthropic|authorization|content-type|origin|referer)$/i.test(k));
  if (notable.length) {
    p();
    p(`  | 请求头 | 值 |`);
    p(`  |---|---|`);
    for (const [k, v] of notable) p(`  | \`${k}\` | \`${String(v).slice(0, 120)}\` |`);
  }
  p();
}

if (idParams.size) {
  p(`## 6. 不透明标识符（建议逐个搞清楚它们从哪来、能不能改）`);
  p();
  p(`| 参数 | 出现次数 | 示例 |`);
  p(`|---|---:|---|`);
  for (const [k, set] of [...idParams].sort((a, b) => b[1].size - a[1].size)) {
    const sample = [...set][0] ?? '';
    p(`| \`${k}\` | ${set.size} | \`${opt.redact && sample.length > 16 ? sample.slice(0, 8) + '…<len=' + sample.length + '>' : sample.slice(0, 64)}\` |`);
  }
  p();
}

if (assets.size) {
  p(`## 7. 静态资源`);
  p();
  for (const [host, m] of [...assets].sort((a, b) => b[1].size - a[1].size)) {
    p(`### ${host} — ${[...m.values()].reduce((a, b) => a + b, 0)} 个请求 / ${m.size} 个唯一路径`);
    p();
    const top = [...m].sort((a, b) => b[1] - a[1]).slice(0, 40);
    for (const [pp, c] of top) p(`- \`${pp}\`${c > 1 ? ` ×${c}` : ''}`);
    if (m.size > top.length) p(`- … 其余 ${m.size - top.length} 个省略`);
    p();
  }
}

const md = L.join('\n');
if (opt.md) { fs.writeFileSync(opt.md, md, 'utf8'); console.log(`已写出 Markdown: ${opt.md}`); }

const summary = {
  source: path.basename(opt.file),
  entries: entries.length,
  hosts: Object.fromEntries([...byHost].sort((a, b) => b[1] - a[1])),
  resourceTypes: Object.fromEntries([...byType].sort((a, b) => b[1] - a[1])),
  telemetry: Object.fromEntries([...telemetry].map(([k, v]) => [k, { count: v.count, hosts: [...v.hosts] }])),
  endpoints: apiLike.map((e) => ({
    method: e.method, host: e.host, path: e.path, count: e.count,
    statuses: Object.fromEntries(e.statuses),
    requestHeaders: [...e.reqHeaders.keys()],
    requestBodyKeys: [...e.reqKeys],
    queryParams: [...e.params],
    responseBytes: e.bytes,
  })),
  opaqueParams: Object.fromEntries([...idParams].map(([k, v]) => [k, v.size])),
  failures,
};
if (opt.json) { fs.writeFileSync(opt.json, JSON.stringify(summary, null, 2), 'utf8'); console.log(`已写出 JSON: ${opt.json}`); }

if (!opt.md && !opt.json) console.log(md);
