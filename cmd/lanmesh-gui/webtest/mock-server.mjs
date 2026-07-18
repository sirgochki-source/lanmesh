import { createServer } from 'node:http';
import { readFile } from 'node:fs/promises';
import { fileURLToPath } from 'node:url';
import { dirname, join, extname, normalize } from 'node:path';
import { SCENARIOS, DEFAULT } from './scenarios.mjs';

const __dir = dirname(fileURLToPath(import.meta.url));
const WEB = join(__dir, '..', 'web');
const MIME = { '.html': 'text/html; charset=utf-8', '.css': 'text/css; charset=utf-8',
  '.js': 'text/javascript; charset=utf-8', '.mjs': 'text/javascript; charset=utf-8', '.svg': 'image/svg+xml' };

let current = process.argv[2] && SCENARIOS[process.argv[2]] ? process.argv[2] : DEFAULT;
// Небольшой джиттер RTT между опросами — чтобы sparkline «оживал».
const jitter = (s) => { for (const n of s.networks) for (const p of n.peers)
  if (p.rttMs >= 0) p.rttMs = Math.max(1, +(p.rttMs + (Math.sin(Date.now() / 700 + p.rttMs) * 6)).toFixed(1)); return s; };

const json = (res, obj, code = 200) => { res.writeHead(code, { 'content-type': 'application/json; charset=utf-8' }); res.end(JSON.stringify(obj)); };

export function makeServer() {
  return createServer(async (req, res) => {
    const url = new URL(req.url, 'http://x');
    const p = url.pathname;
    if (p === '/__scenario') { const n = url.searchParams.get('name'); if (SCENARIOS[n]) current = n; return json(res, { ok: true, current }); }
    if (p === '/dev') { const links = Object.keys(SCENARIOS).map(n =>
        `<a href="/__scenario?name=${n}" onclick="fetch(this.href).then(()=>location='/');return false">${n}</a>`).join(' · ');
      res.writeHead(200, { 'content-type': 'text/html; charset=utf-8' });
      return res.end(`<body style="background:#08090c;color:#ccc;font:16px system-ui;padding:2rem">Сценарии: ${links}<p>Текущий: <b>${current}</b></p>`); }
    if (p === '/api/state') return json(res, jitter(SCENARIOS[current]()));
    if (p === '/api/diagnose') return json(res, { natType: 'full-cone', vpn: false, egressUDP: true });
    if (p === '/api/settings') return json(res, { custom: false, signalCount: 2, hasRelay: true });
    if (p === '/api/invite') return json(res, { link: 'lanmesh://join?net=myteam&pass=secret&sig=https://s1&relay=r:1' });
    // Отдельно от общего POST-заглушечника ниже: renderSettings() (Task 13) показывает код
    // диагностики только если ответ несёт tag — без него кнопка «Отправить диагностику»
    // в харнессе всегда выглядела бы как ошибка.
    if (p === '/api/senddiag' && req.method === 'POST') return json(res, { ok: true, tag: 'DEV12345' });
    if (req.method === 'POST') return json(res, { ok: true }); // addnetwork/leave/disconnect/sendlogs/settings
    // статика
    let rel = normalize(p === '/' ? '/index.html' : p).replace(/^(\.\.[/\\])+/, '');
    try { const data = await readFile(join(WEB, rel)); res.writeHead(200, { 'content-type': MIME[extname(rel)] || 'application/octet-stream' }); res.end(data); }
    catch { res.writeHead(404); res.end('not found'); }
  });
}
if (import.meta.url === `file://${process.argv[1]}` || process.argv[1]?.endsWith('mock-server.mjs')) {
  // Слушаем строго loopback: dev-инструмент, доступ только с этой машины,
  // и Windows-брандмауэр не спрашивает про доступ из общих сетей.
  makeServer().listen(8788, '127.0.0.1', () => console.log('mock: http://127.0.0.1:8788  (меню: /dev)'));
}
