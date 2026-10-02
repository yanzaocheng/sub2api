// _mock-cdp.mjs —— 最小可用的假 CDP 端点，用来在没浏览器时验证 cdp.mjs 的协议管路。
// 只实现 cdp.mjs 用到的那几个方法：Network.enable / getResponseBody / Page.enable /
// Page.navigate / Runtime.evaluate，并按脚本吐一串 Network 事件。
import http from 'node:http';
import crypto from 'node:crypto';

const PORT = Number(process.argv[2] || 19222);
const WS_PATH = '/devtools/page/MOCK';
const GUID = '258EAFA5-E914-47DA-95CA-C5AB0DC85B11';
const accept = (k) => crypto.createHash('sha1').update(k + GUID).digest('base64');

// ---- WebSocket 帧编解码（够用就行，不做分片） ----
function decode(buf) {
  const out = []; let off = 0;
  while (off + 2 <= buf.length) {
    const b0 = buf[off], b1 = buf[off + 1];
    const opcode = b0 & 0x0f;
    const masked = (b1 & 0x80) !== 0;
    let len = b1 & 0x7f, p = off + 2;
    if (len === 126) { len = buf.readUInt16BE(p); p += 2; }
    else if (len === 127) { len = Number(buf.readBigUInt64BE(p)); p += 8; }
    let mask = null;
    if (masked) { mask = buf.subarray(p, p + 4); p += 4; }
    if (p + len > buf.length) break;
    const payload = Buffer.from(buf.subarray(p, p + len));
    if (mask) for (let i = 0; i < payload.length; i++) payload[i] ^= mask[i % 4];
    out.push({ opcode, text: payload.toString('utf8') });
    off = p + len;
  }
  return out;
}
function encode(str) {
  const p = Buffer.from(str, 'utf8'), n = p.length;
  let h;
  if (n < 126) h = Buffer.from([0x81, n]);
  else if (n < 65536) { h = Buffer.alloc(4); h[0] = 0x81; h[1] = 126; h.writeUInt16BE(n, 2); }
  else { h = Buffer.alloc(10); h[0] = 0x81; h[1] = 127; h.writeBigUInt64BE(BigInt(n), 2); }
  return Buffer.concat([h, p]);
}

// ---- 脚本化的假流量 ----
const T0 = Date.now() / 1000;
const BODIES = {
  r1: JSON.stringify([{ uuid: 'conv-1', name: 'Mock conversation' }]),
  r2: '',                                        // RUM 的响应体是空的
  r3: JSON.stringify({ ok: true, tier: 'pro' }),
};
const EVENTS = [
  { at: 100, msg: { method: 'Network.requestWillBeSent', params: { requestId: 'r1', type: 'XHR', timestamp: T0, wallTime: T0, request: { url: 'https://claude.ai/api/organizations/8f3a1b2c-1111-2222-3333-444455556666/chat_conversations_v2?limit=30&offset=0&consistency=eventual', method: 'GET', headers: { accept: '*/*', cookie: 'sessionKey=sk-ant-sid01-MOCKSECRET1234567890' } } } } },
  { at: 140, msg: { method: 'Network.responseReceived', params: { requestId: 'r1', response: { status: 200, statusText: 'OK', mimeType: 'application/json', headers: { 'content-type': 'application/json' } } } } },
  { at: 180, msg: { method: 'Network.loadingFinished', params: { requestId: 'r1', timestamp: T0 + 0.08 } } },

  { at: 200, msg: { method: 'Network.requestWillBeSent', params: { requestId: 'r3', type: 'Fetch', timestamp: T0 + 0.1, wallTime: T0 + 0.1, request: { url: 'https://claude.ai/api/organizations/8f3a1b2c-1111-2222-3333-444455556666/current_user_access', method: 'POST', headers: { 'content-type': 'application/json', authorization: 'Bearer sk-ant-oat01-MOCKTOKEN' }, postData: JSON.stringify({ org: '8f3a1b2c', include_limits: true }) } } } },
  { at: 240, msg: { method: 'Network.responseReceived', params: { requestId: 'r3', response: { status: 200, statusText: 'OK', mimeType: 'application/json', headers: { 'content-type': 'application/json' } } } } },
  { at: 280, msg: { method: 'Network.loadingFinished', params: { requestId: 'r3', timestamp: T0 + 0.15 } } },

  { at: 300, msg: { method: 'Network.requestWillBeSent', params: { requestId: 'r2', type: 'XHR', timestamp: T0 + 0.2, wallTime: T0 + 0.2, request: { url: 'https://browser-intake-datadoghq.com/api/v2/rum?ddsource=browser&dd-api-key=pub71869dceb5b70dbMOCK&batch_time=1789372268828', method: 'POST', headers: { 'content-type': 'text/plain' }, postData: 'x'.repeat(200) } } } },
  { at: 340, msg: { method: 'Network.responseReceived', params: { requestId: 'r2', response: { status: 202, statusText: 'Accepted', mimeType: 'text/plain', headers: {} } } } },
  { at: 380, msg: { method: 'Network.loadingFinished', params: { requestId: 'r2', timestamp: T0 + 0.25 } } },

  // 一个会被 capture 默认跳过的图片请求
  { at: 400, msg: { method: 'Network.requestWillBeSent', params: { requestId: 'r4', type: 'Image', timestamp: T0 + 0.3, wallTime: T0 + 0.3, request: { url: 'https://c.statcounter.com/time_spent.gif?bk=99dfa2e716&r=277713112&ft=Asia/Shanghai', method: 'GET', headers: {} } } } },
  { at: 440, msg: { method: 'Network.responseReceived', params: { requestId: 'r4', response: { status: 200, statusText: 'OK', mimeType: 'image/gif', headers: {} } } } },
  { at: 460, msg: { method: 'Network.loadingFinished', params: { requestId: 'r4', timestamp: T0 + 0.32 } } },

  // 一个失败请求
  { at: 500, msg: { method: 'Network.requestWillBeSent', params: { requestId: 'r5', type: 'XHR', timestamp: T0 + 0.4, wallTime: T0 + 0.4, request: { url: 'https://claude.ai/api/organizations/8f3a1b2c-1111-2222-3333-444455556666/exposure-eligible', method: 'GET', headers: {} } } } },
  { at: 540, msg: { method: 'Network.loadingFailed', params: { requestId: 'r5', errorText: 'net::ERR_BLOCKED_BY_CLIENT' } } },
];

const server = http.createServer((req, res) => {
  if (req.url.startsWith('/json/version')) {
    res.writeHead(200, { 'content-type': 'application/json' });
    return res.end(JSON.stringify({
      Browser: 'Chrome/153.0.8010.36 (MOCK)', 'Protocol-Version': '1.3',
      'User-Agent': 'Mozilla/5.0 (Windows NT 10.0; Win64; x64) Chrome/153.0.0.0',
      webSocketDebuggerUrl: `ws://127.0.0.1:${PORT}${WS_PATH}`,
    }));
  }
  if (req.url.startsWith('/json/list')) {
    res.writeHead(200, { 'content-type': 'application/json' });
    return res.end(JSON.stringify([{
      id: 'MOCK', type: 'page', title: 'Mock claude.ai', url: 'https://claude.ai/chat/mock',
      webSocketDebuggerUrl: `ws://127.0.0.1:${PORT}${WS_PATH}`,
    }]));
  }
  res.writeHead(404); res.end();
});

server.on('upgrade', (req, socket, head) => {
  const key = req.headers['sec-websocket-key'];
  if (!key || !req.url.startsWith(WS_PATH)) { socket.destroy(); return; }
  socket.write(
    'HTTP/1.1 101 Switching Protocols\r\n' +
    'Upgrade: websocket\r\nConnection: Upgrade\r\n' +
    `Sec-WebSocket-Accept: ${accept(key)}\r\n\r\n`
  );
  if (head?.length) handle(decode(head));

  socket.on('data', (buf) => handle(decode(buf)));

  function handle(frames) {
    for (const f of frames) {
      if (f.opcode === 8) { socket.destroy(); return; }
      if (f.opcode !== 1) continue;
      let m; try { m = JSON.parse(f.text); } catch { continue; }
      if (m.method === 'Network.getResponseBody') {
        const body = BODIES[m.params.requestId] ?? '';
        socket.write(encode(JSON.stringify({ id: m.id, result: { body, base64Encoded: false } })));
        continue;
      }
      if (m.method === 'Runtime.evaluate') {
        socket.write(encode(JSON.stringify({ id: m.id, result: { result: { type: 'string', value: 'MOCK_EVAL_OK' } } })));
        continue;
      }
      // 其余命令一律 ACK
      socket.write(encode(JSON.stringify({ id: m.id, result: {} })));
      if (m.method === 'Network.enable') {
        for (const e of EVENTS) setTimeout(() => {
          if (!socket.destroyed) socket.write(encode(JSON.stringify(e.msg)));
        }, e.at);
      }
    }
  }
});

server.listen(PORT, '127.0.0.1', () => console.log(`MOCK_CDP_UP http://127.0.0.1:${PORT}`));
