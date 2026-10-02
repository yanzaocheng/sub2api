// 沙箱能力探测：loopback TCP + 子进程 spawn 是否可用
import http from 'node:http';
import net from 'node:net';
import { spawn } from 'node:child_process';

const out = [];
const log = (k, v) => { out.push(`${k}: ${v}`); console.log(`${k}: ${v}`); };

// 1) 能不能监听 loopback 并自连
await new Promise((res) => {
  const srv = http.createServer((q, r) => r.end('ok'));
  srv.on('error', (e) => { log('LOOPBACK_LISTEN', 'FAIL ' + e.code); res(); });
  srv.listen(18080, '127.0.0.1', () => {
    http.get('http://127.0.0.1:18080/', (r) => {
      let d = ''; r.on('data', (c) => d += c);
      r.on('end', () => { log('LOOPBACK_HTTP', `OK ${r.statusCode} ${d}`); srv.close(); res(); });
    }).on('error', (e) => { log('LOOPBACK_HTTP', 'FAIL ' + e.code); srv.close(); res(); });
  });
});

// 2) 能不能连一个已知会拒绝的端口（区分"连不上"和"被拒"）
await new Promise((res) => {
  const s = net.connect({ host: '127.0.0.1', port: 9222 }, () => { log('CONNECT_9222', 'OPEN'); s.destroy(); res(); });
  s.on('error', (e) => { log('CONNECT_9222', e.code); res(); });
  setTimeout(() => { log('CONNECT_9222', 'TIMEOUT'); s.destroy(); res(); }, 3000);
});

// 3) 能不能 spawn 子进程并拿到它的 stdout
await new Promise((res) => {
  const c = spawn(process.execPath, ['-e', 'console.log("child-alive")'], { stdio: ['ignore', 'pipe', 'pipe'] });
  let o = '', e = '';
  c.stdout.on('data', (d) => o += d);
  c.stderr.on('data', (d) => e += d);
  c.on('error', (err) => { log('SPAWN_PIPE', 'FAIL ' + err.code); res(); });
  c.on('close', (code) => { log('SPAWN_PIPE', `exit=${code} stdout=${JSON.stringify(o.trim())} stderr=${JSON.stringify(e.trim().slice(0, 120))}`); res(); });
  setTimeout(() => { log('SPAWN_PIPE', 'TIMEOUT'); res(); }, 8000);
});

console.log('\n--- raw ---');
console.log(out.join('\n'));
