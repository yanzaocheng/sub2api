// 从沙箱内尝试连到 loopback 上正在监听的端口 —— 这决定了我能不能用 CDP 控制浏览器
import net from 'node:net';

const port = Number(process.argv[2] || 38821);
const s = net.connect({ host: '127.0.0.1', port }, () => {
  console.log(`OUTBOUND_LOOPBACK: OPEN  (127.0.0.1:${port} 可连)`);
  s.destroy();
  process.exit(0);
});
s.on('error', (e) => {
  console.log(`OUTBOUND_LOOPBACK: ${e.code}  (127.0.0.1:${port})`);
  process.exit(1);
});
setTimeout(() => { console.log('OUTBOUND_LOOPBACK: TIMEOUT'); s.destroy(); process.exit(2); }, 5000);
