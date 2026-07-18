# GUI Phase 1 — визуальный каркас (Command Glass). Implementation Plan

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** Собрать новый фронтенд GUI (тёмное «стекло», компактный и подробный режимы, список участников с живым пингом и качеством связи) как статические ассеты, проверяемые в браузере на mock-данных без Go.

**Architecture:** Ванильный ES-модульный фронтенд под WebView2 (Edge Chromium). Вся логика и рендер — чистые функции `(данные) → строка`, покрытые юнит-тестами через встроенный `node --test` (ноль зависимостей). Приложение (`app.js`) опрашивает `/api/state`, склеивает рендер в DOM и вешает обработчики. Визуал проверяется через Node-mock-сервер, отдающий фейковые сценарии.

**Tech Stack:** HTML/CSS/ES-модули (без сборки), инлайн-SVG для sparkline, Node v24 (`node --test`, `http`, `fetch`) только для dev/тестов.

## Global Constraints

- Целевой рантайм — **WebView2 (Edge Chromium)**: современный JS/CSS разрешён (ES-модули, `backdrop-filter`, `ResizeObserver`, container queries).
- **Ноль сборочного тулчейна и ноль runtime-зависимостей.** Никаких npm-пакетов во фронтенде; dev/тесты — только встроенные модули Node.
- Шиппинг-ассеты — строго в `cmd/lanmesh-gui/web/` (в P2 встраиваются `//go:embed web`). Dev/тесты — в `cmd/lanmesh-gui/webtest/` (НЕ встраиваются).
- Пороги пинга (сохранить точно): good `<60`, ok `<150`, bad `>=150` мс.
- Интервал опроса: `1300` мс.
- Санитизация имён (`dispName`) и поведение CSRF-guard переносятся без регресса. Имена участников/сетей рендерить ТОЛЬКО через `dispName`.
- Эндпоинты API 1:1 с текущими (`/api/state`, `/api/addnetwork`, `/api/leavenetwork`, `/api/disconnect`, `/api/sendlogs`, `/api/senddiag`, `/api/diagnose`, `/api/settings`, `/api/invite`).
- Токены дизайна (палитра «Command Glass») — из спеки `docs/superpowers/specs/2026-07-18-gui-redesign-design.md` §4.
- В P1 НЕТ правок Go. Виды «Карта» и «Трафик» — заглушки «скоро».

---

## File Structure

**Ассеты (встраиваемые, `cmd/lanmesh-gui/web/`):**
- `index.html` — оболочка (шапка, слоты режимов/видов), подключает `app.js` модулем.
- `app.css` — токены + все компоненты.
- `app.js` — бутстрап: опрос, оркестрация рендера, переключение режима, накопление истории RTT, тосты.
- `lib/sanitize.js` — `esc`, `dispName` (чистые).
- `lib/format.js` — `fmtRtt`, `rttClass`, `fmtUptime`, `fmtSeen`, `plural` (чистые).
- `lib/invite.js` — `parseInvite` (чистая).
- `lib/rtt-history.js` — класс `RttHistory` (чистый).
- `lib/quality.js` — `quality` (чистая).
- `lib/peerdiff.js` — `diffPeers` (чистая).
- `lib/sparkline.js` — `scalePoints`, `sparklineSVG` (чистые).
- `views/list.js` — чистые render-функции списка (компакт + подробный).
- `views/shell.js` — чистые render-функции шапки/рейла/оболочки.

**Dev/тесты (НЕ встраиваемые, `cmd/lanmesh-gui/webtest/`):**
- `scenarios.mjs` — карта фейковых состояний.
- `mock-server.mjs` — статик-сервер `web/` + фейковый API + переключатель сценариев.
- `test/*.test.mjs` — юнит-тесты на `node --test`.

Тесты импортируют модули из `../web/lib/...` и `../web/views/...` по относительным путям (это валидные ES-модули).

---

## Task 1: Каркас ассетов, mock-сервер и запуск тестов

**Files:**
- Create: `cmd/lanmesh-gui/web/index.html`
- Create: `cmd/lanmesh-gui/web/app.css`
- Create: `cmd/lanmesh-gui/webtest/scenarios.mjs`
- Create: `cmd/lanmesh-gui/webtest/mock-server.mjs`
- Test: `cmd/lanmesh-gui/webtest/test/mock.test.mjs`

**Interfaces:**
- Produces: `scenarios.mjs` экспортирует `SCENARIOS` — объект `{ [name]: () => stateObject }` и `DEFAULT = 'team3'`. Каждый `stateObject` — как `/api/state` в проде (поля `running, selfName, selfIP, selfEndpoint, stunVia, uptimeSec, error, sendLogs, networks[]`; сеть: `name, tag, signalError, signals[], peers[]`; пир: `name, vip, status, endpoint, lastSeenMs, rttMs, signals[]`).
- Produces: `mock-server.mjs` слушает `http://127.0.0.1:8788`, отдаёт статику из `../web`, обрабатывает все `/api/*`, хранит текущий сценарий в памяти; `GET /__scenario?name=x` меняет его; `GET /dev` — меню со ссылками на сценарии.

- [ ] **Step 1: Написать сценарии (`webtest/scenarios.mjs`)**

```js
// Фейковые снимки /api/state для проверки всех состояний UI.
const peer = (name, vip, status, rttMs, signals = [true]) => ({
  name, vip, status, endpoint: status === 'direct' ? '203.0.113.7:41234' : '',
  lastSeenMs: status === 'connecting' ? -1 : 800, rttMs, signals,
});
const net = (name, tag, peers, signals = [{ host: 's1', up: true }], signalError = '') =>
  ({ name, tag, signalError, signals, peers });
const base = (over = {}) => ({
  running: true, selfName: 'MY-PC', selfIP: '25.31.8.2', selfEndpoint: '203.0.113.9:5000',
  stunVia: 'stun1', uptimeSec: 8040, error: '', sendLogs: true, networks: [], ...over,
});

export const SCENARIOS = {
  disconnected: () => base({ running: false, selfIP: '', selfEndpoint: '', uptimeSec: 0, networks: [] }),
  empty: () => base({ networks: [net('myteam', 'a'.repeat(64), [])] }),
  team3: () => base({ networks: [net('myteam', 'a'.repeat(64), [
    peer('Мурад', '25.44.12.9', 'direct', 18),
    peer('Kolya', '25.44.7.3', 'direct', 42),
    peer('sara_pc', '25.44.31.7', 'relay', 128),
    peer('Dev_null', '25.44.9.1', 'connecting', -1),
  ])] }),
  multi: () => base({ networks: [
    net('myteam', 'a'.repeat(64), [peer('Мурад', '25.44.12.9', 'direct', 18), peer('Kolya', '25.44.7.3', 'direct', 42)]),
    net('dota', 'b'.repeat(64), [peer('gamer2000', '25.60.1.4', 'direct', 55)]),
  ] }),
  noext: () => base({ selfEndpoint: '', networks: [net('myteam', 'a'.repeat(64), [peer('Мурад', '25.44.12.9', 'connecting', -1)])] }),
  sigerr: () => base({ networks: [net('myteam', 'a'.repeat(64), [peer('Мурад', '25.44.12.9', 'direct', 18)],
    [{ host: 's1', up: false }], 'сигналка недоступна')] }),
  hostile: () => base({ networks: [net('myteam', 'a'.repeat(64), [
    // Имя с bidi-override + zero-width: sanitize обязан их вычистить.
    peer('a\u202Egnp.exe', '25.44.5.5', 'direct', 20),
  ])] }),
};
export const DEFAULT = 'team3';
```

- [ ] **Step 2: Написать mock-сервер (`webtest/mock-server.mjs`)**

```js
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
    if (req.method === 'POST') return json(res, { ok: true }); // addnetwork/leave/disconnect/sendlogs/senddiag/settings
    // статика
    let rel = normalize(p === '/' ? '/index.html' : p).replace(/^(\.\.[/\\])+/, '');
    try { const data = await readFile(join(WEB, rel)); res.writeHead(200, { 'content-type': MIME[extname(rel)] || 'application/octet-stream' }); res.end(data); }
    catch { res.writeHead(404); res.end('not found'); }
  });
}
if (import.meta.url === `file://${process.argv[1]}` || process.argv[1]?.endsWith('mock-server.mjs')) {
  makeServer().listen(8788, () => console.log('mock: http://127.0.0.1:8788  (меню: /dev)'));
}
```

- [ ] **Step 3: Минимальный `web/index.html` и пустой `web/app.css`**

`web/index.html`:
```html
<!doctype html><html lang="ru"><head>
<meta charset="utf-8"><meta name="viewport" content="width=device-width, initial-scale=1">
<title>lanmesh</title><link rel="stylesheet" href="app.css">
</head><body>
<div id="app">загрузка…</div>
<script type="module" src="app.js"></script>
</body></html>
```
`web/app.css`: `/* Command Glass — токены и компоненты (заполняется в Task 9). */` и минимальный `body{background:#08090c;color:#e8eaf0;font:14px system-ui;margin:0}`.
Создать также заглушку `web/app.js`: `document.getElementById('app').textContent = 'ok';`

- [ ] **Step 4: Написать интеграционный тест (`webtest/test/mock.test.mjs`)**

```js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { makeServer } from '../mock-server.mjs';

test('mock отдаёт /api/state и переключает сценарии', async () => {
  const srv = makeServer().listen(0);
  await new Promise(r => srv.once('listening', r));
  const port = srv.address().port;
  const base = `http://127.0.0.1:${port}`;
  const st = await (await fetch(`${base}/api/state`)).json();
  assert.equal(st.running, true);
  assert.ok(Array.isArray(st.networks));
  await fetch(`${base}/__scenario?name=disconnected`);
  const st2 = await (await fetch(`${base}/api/state`)).json();
  assert.equal(st2.running, false);
  await new Promise(r => srv.close(r));
});
```

- [ ] **Step 5: Прогнать тест — убедиться, что проходит**

Run: `node --test cmd/lanmesh-gui/webtest/test/`
Expected: PASS (1 test).

- [ ] **Step 6: Ручная проверка mock-харнесса**

Run: `node cmd/lanmesh-gui/webtest/mock-server.mjs`
Открыть `http://127.0.0.1:8788` → видно «ok»; `http://127.0.0.1:8788/dev` → список сценариев кликается.

- [ ] **Step 7: Commit**

```bash
git add cmd/lanmesh-gui/web cmd/lanmesh-gui/webtest
git commit -m "feat(gui): каркас фронтенд-ассетов + mock-харнесс и тесты"
```

---

## Task 2: sanitize.js — esc и dispName

**Files:**
- Create: `cmd/lanmesh-gui/web/lib/sanitize.js`
- Test: `cmd/lanmesh-gui/webtest/test/sanitize.test.mjs`

**Interfaces:**
- Produces: `esc(s): string` — HTML-экранирование `& < > " '`. `dispName(s): string` — сперва вычистка управляющих/bidi/zero-width диапазонов, затем `esc`.

- [ ] **Step 1: Написать падающий тест**

```js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { esc, dispName } from '../../web/lib/sanitize.js';

test('esc экранирует html-мета', () => {
  assert.equal(esc(`<b>&"'`), '&lt;b&gt;&amp;&quot;&#39;');
});
test('dispName вычищает bidi-override и zero-width, затем экранирует', () => {
  assert.equal(dispName('a\u202Eb'), 'ab');          // bidi-override удалён
  assert.equal(dispName('x\u200By'), 'xy');          // zero-width удалён
  assert.equal(dispName('<i>'), '&lt;i&gt;');        // html экранирован
});
```

- [ ] **Step 2: Прогнать — убедиться, что падает**

Run: `node --test cmd/lanmesh-gui/webtest/test/sanitize.test.mjs`
Expected: FAIL (модуль не найден / функции не определены).

- [ ] **Step 3: Реализовать (`web/lib/sanitize.js`)**

```js
const MAP = { '&': '&amp;', '<': '&lt;', '>': '&gt;', '"': '&quot;', "'": '&#39;' };
export function esc(s) { return String(s).replace(/[&<>"']/g, c => MAP[c]); }
// Убираем управляющие, zero-width и bidi-override/isolate + BOM: иначе именем
// можно визуально подделать другой узел (U+202E разворачивает текст).
export function dispName(s) {
  return esc(String(s).replace(/[\u0000-\u001F\u200B-\u200F\u202A-\u202E\u2066-\u2069\uFEFF]/g, ''));
}
```

- [ ] **Step 4: Прогнать — PASS**

Run: `node --test cmd/lanmesh-gui/webtest/test/sanitize.test.mjs`
Expected: PASS (2 tests).

- [ ] **Step 5: Commit**

```bash
git add cmd/lanmesh-gui/web/lib/sanitize.js cmd/lanmesh-gui/webtest/test/sanitize.test.mjs
git commit -m "feat(gui): sanitize.js (esc, dispName) с тестами"
```

---

## Task 3: format.js — форматтеры и пороги

**Files:**
- Create: `cmd/lanmesh-gui/web/lib/format.js`
- Test: `cmd/lanmesh-gui/webtest/test/format.test.mjs`

**Interfaces:**
- Produces: `fmtRtt(ms): string|null`; `rttClass(ms): 'rtt-good'|'rtt-ok'|'rtt-bad'`; `fmtUptime(sec): string`; `fmtSeen(ms): string`; `plural(n, one, few, many): string`.

- [ ] **Step 1: Написать падающий тест**

```js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { fmtRtt, rttClass, fmtUptime, fmtSeen, plural } from '../../web/lib/format.js';

test('fmtRtt', () => {
  assert.equal(fmtRtt(-1), null);
  assert.equal(fmtRtt(3.14), '3.1 мс');
  assert.equal(fmtRtt(42.6), '43 мс');
});
test('rttClass по порогам 60/150', () => {
  assert.equal(rttClass(18), 'rtt-good');
  assert.equal(rttClass(60), 'rtt-ok');
  assert.equal(rttClass(150), 'rtt-bad');
});
test('fmtUptime', () => {
  assert.equal(fmtUptime(45), '45 с');
  assert.equal(fmtUptime(8040), '2 ч 14 м');
});
test('plural (русское склонение)', () => {
  assert.equal(plural(1, 'сеть', 'сети', 'сетей'), 'сеть');
  assert.equal(plural(3, 'сеть', 'сети', 'сетей'), 'сети');
  assert.equal(plural(5, 'сеть', 'сети', 'сетей'), 'сетей');
});
```

- [ ] **Step 2: Прогнать — FAIL**

Run: `node --test cmd/lanmesh-gui/webtest/test/format.test.mjs`
Expected: FAIL.

- [ ] **Step 3: Реализовать (`web/lib/format.js`)** — перенос логики из текущего `index.html`

```js
export function fmtRtt(ms) {
  if (ms < 0) return null;
  if (ms < 10) return ms.toFixed(1) + ' мс';
  return Math.round(ms) + ' мс';
}
export function rttClass(ms) {            // пороги по ощущениям от игры
  if (ms < 60) return 'rtt-good';
  if (ms < 150) return 'rtt-ok';
  return 'rtt-bad';
}
export function fmtUptime(sec) {
  if (sec < 60) return sec + ' с';
  const m = Math.floor(sec / 60), h = Math.floor(m / 60);
  if (h > 0) return h + ' ч ' + (m % 60) + ' м';
  return m + ' м ' + (sec % 60) + ' с';
}
export function fmtSeen(ms) {
  if (ms < 0) return 'нет пакетов';
  if (ms < 2000) return 'только что';
  return Math.round(ms / 1000) + ' с назад';
}
export function plural(n, one, few, many) {
  const d = n % 10, dd = n % 100;
  if (d === 1 && dd !== 11) return one;
  if (d >= 2 && d <= 4 && (dd < 12 || dd > 14)) return few;
  return many;
}
```

- [ ] **Step 4: Прогнать — PASS**

Run: `node --test cmd/lanmesh-gui/webtest/test/format.test.mjs`
Expected: PASS (4 tests).

- [ ] **Step 5: Commit**

```bash
git add cmd/lanmesh-gui/web/lib/format.js cmd/lanmesh-gui/webtest/test/format.test.mjs
git commit -m "feat(gui): format.js (пинг/аптайм/склонения) с тестами"
```

---

## Task 4: invite.js — разбор ссылки-приглашения

**Files:**
- Create: `cmd/lanmesh-gui/web/lib/invite.js`
- Test: `cmd/lanmesh-gui/webtest/test/invite.test.mjs`

**Interfaces:**
- Produces: `parseInvite(text): { net, pass, sigs: string[], relay }` (значения = `null`/`[]`, если поля нет). Совместим с кодировкой `url.Values.Encode` (Go): `+` → пробел, затем `decodeURIComponent`; битая `%`-последовательность не роняет разбор.

- [ ] **Step 1: Написать падающий тест**

```js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { parseInvite } from '../../web/lib/invite.js';

test('parseInvite разбирает net/pass/sig/relay', () => {
  const r = parseInvite('lanmesh://join?net=my+team&pass=p%40ss&sig=https://s1&sig=https://s2&relay=r:1');
  assert.equal(r.net, 'my team');
  assert.equal(r.pass, 'p@ss');
  assert.deepEqual(r.sigs, ['https://s1', 'https://s2']);
  assert.equal(r.relay, 'r:1');
});
test('parseInvite не роняется на битой %-последовательности', () => {
  const r = parseInvite('net=ok&pass=%');
  assert.equal(r.net, 'ok');            // битое поле pass просто пропущено
});
```

- [ ] **Step 2: Прогнать — FAIL**

Run: `node --test cmd/lanmesh-gui/webtest/test/invite.test.mjs`
Expected: FAIL.

- [ ] **Step 3: Реализовать (`web/lib/invite.js`)** — перенос из `index.html`

```js
export function parseInvite(text) {
  text = (text || '').trim();
  const qi = text.indexOf('?');
  const q = qi >= 0 ? text.slice(qi + 1) : text;
  let net = null, pass = null, sigs = [], relay = null;
  for (const part of q.split('&')) {
    const eq = part.indexOf('=');
    if (eq < 0) continue;
    const k = part.slice(0, eq);
    let v;
    try { v = decodeURIComponent(part.slice(eq + 1).replace(/\+/g, ' ')); }
    catch (e) { continue; }               // битую %-последовательность игнорируем
    if (k === 'net') net = v;
    else if (k === 'pass') pass = v;
    else if (k === 'sig') sigs.push(v);
    else if (k === 'relay') relay = v;
  }
  return { net, pass, sigs, relay };
}
```

- [ ] **Step 4: Прогнать — PASS** · **Step 5: Commit**

Run: `node --test cmd/lanmesh-gui/webtest/test/invite.test.mjs` → PASS.
```bash
git add cmd/lanmesh-gui/web/lib/invite.js cmd/lanmesh-gui/webtest/test/invite.test.mjs
git commit -m "feat(gui): invite.js (разбор приглашения) с тестами"
```

---

## Task 5: rtt-history.js — история пинга для sparkline

**Files:**
- Create: `cmd/lanmesh-gui/web/lib/rtt-history.js`
- Test: `cmd/lanmesh-gui/webtest/test/rtt-history.test.mjs`

**Interfaces:**
- Produces: `class RttHistory { constructor(cap = 40); push(vip, rttMs); get(vip): number[]; prune(activeVips: string[]) }`. `push` игнорирует `rttMs < 0`. `get` возвращает массив старое→новое (копию). `prune` удаляет истории пиров не из списка активных.

- [ ] **Step 1: Написать падающий тест**

```js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { RttHistory } from '../../web/lib/rtt-history.js';

test('push/get кольцевого буфера', () => {
  const h = new RttHistory(3);
  h.push('a', 10); h.push('a', 20); h.push('a', 30); h.push('a', 40);
  assert.deepEqual(h.get('a'), [20, 30, 40]);   // ёмкость 3, старое вытеснено
  assert.deepEqual(h.get('zzz'), []);
});
test('push игнорирует отрицательный rtt', () => {
  const h = new RttHistory(); h.push('a', -1); h.push('a', 5);
  assert.deepEqual(h.get('a'), [5]);
});
test('prune убирает неактивных', () => {
  const h = new RttHistory(); h.push('a', 5); h.push('b', 5);
  h.prune(['a']);
  assert.deepEqual(h.get('b'), []);
  assert.deepEqual(h.get('a'), [5]);
});
```

- [ ] **Step 2: Прогнать — FAIL**

Run: `node --test cmd/lanmesh-gui/webtest/test/rtt-history.test.mjs`
Expected: FAIL.

- [ ] **Step 3: Реализовать (`web/lib/rtt-history.js`)**

```js
export class RttHistory {
  constructor(cap = 40) { this.cap = cap; this.map = new Map(); }
  push(vip, rttMs) {
    if (rttMs < 0) return;                       // «нет измерения» не пишем
    let arr = this.map.get(vip);
    if (!arr) { arr = []; this.map.set(vip, arr); }
    arr.push(rttMs);
    if (arr.length > this.cap) arr.shift();
  }
  get(vip) { return (this.map.get(vip) || []).slice(); }
  prune(activeVips) {
    const live = new Set(activeVips);
    for (const k of this.map.keys()) if (!live.has(k)) this.map.delete(k);
  }
}
```

- [ ] **Step 4: Прогнать — PASS** · **Step 5: Commit**

Run: `node --test cmd/lanmesh-gui/webtest/test/rtt-history.test.mjs` → PASS.
```bash
git add cmd/lanmesh-gui/web/lib/rtt-history.js cmd/lanmesh-gui/webtest/test/rtt-history.test.mjs
git commit -m "feat(gui): rtt-history.js (история пинга) с тестами"
```

---

## Task 6: quality.js — индикатор качества связи

**Files:**
- Create: `cmd/lanmesh-gui/web/lib/quality.js`
- Test: `cmd/lanmesh-gui/webtest/test/quality.test.mjs`

**Interfaces:**
- Produces: `quality(status, rttMs, history): { level: 'good'|'ok'|'bad'|'connecting', label: string }`. `history` — массив RTT (для оценки стабильности через разброс). Правила: `connecting` → connecting; `relay` → максимум `ok` (при `rttMs<150`), иначе `bad`; `direct` → `good` при `rttMs<60` И низком разбросе, `ok` при `<150` (или высокий разброс), иначе `bad`.

- [ ] **Step 1: Написать падающий тест**

```js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { quality } from '../../web/lib/quality.js';

test('connecting', () => assert.equal(quality('connecting', -1, []).level, 'connecting'));
test('direct низкий стабильный пинг → good', () =>
  assert.equal(quality('direct', 18, [17, 18, 19, 18]).level, 'good'));
test('direct низкий, но дёрганый пинг → ok', () =>
  assert.equal(quality('direct', 30, [5, 80, 10, 70]).level, 'ok'));   // высокий разброс
test('direct высокий пинг → bad', () =>
  assert.equal(quality('direct', 200, [200, 205]).level, 'bad'));
test('relay никогда не good', () =>
  assert.equal(quality('relay', 20, [20, 20]).level, 'ok'));
```

- [ ] **Step 2: Прогнать — FAIL**

Run: `node --test cmd/lanmesh-gui/webtest/test/quality.test.mjs`
Expected: FAIL.

- [ ] **Step 3: Реализовать (`web/lib/quality.js`)**

```js
// Разброс = стандартное отклонение по истории (если точек мало — 0).
function stdev(a) {
  if (a.length < 3) return 0;
  const m = a.reduce((s, x) => s + x, 0) / a.length;
  return Math.sqrt(a.reduce((s, x) => s + (x - m) ** 2, 0) / a.length);
}
const L = { good: 'отличное', ok: 'среднее', bad: 'плохое', relayok: 'через релей', connecting: 'подключение' };
export function quality(status, rttMs, history = []) {
  if (status === 'connecting' || rttMs < 0) return { level: 'connecting', label: L.connecting };
  const jittery = stdev(history) > 25;                 // мс — заметно «дёргается»
  if (status === 'relay') return rttMs < 150 ? { level: 'ok', label: L.relayok } : { level: 'bad', label: L.bad };
  // direct
  if (rttMs < 60 && !jittery) return { level: 'good', label: L.good };
  if (rttMs < 150) return { level: 'ok', label: L.ok };
  return { level: 'bad', label: L.bad };
}
```

- [ ] **Step 4: Прогнать — PASS** · **Step 5: Commit**

Run: `node --test cmd/lanmesh-gui/webtest/test/quality.test.mjs` → PASS.
```bash
git add cmd/lanmesh-gui/web/lib/quality.js cmd/lanmesh-gui/webtest/test/quality.test.mjs
git commit -m "feat(gui): quality.js (индикатор качества) с тестами"
```

---

## Task 7: peerdiff.js — вход/выход участников (для тостов)

**Files:**
- Create: `cmd/lanmesh-gui/web/lib/peerdiff.js`
- Test: `cmd/lanmesh-gui/webtest/test/peerdiff.test.mjs`

**Interfaces:**
- Produces: `diffPeers(prev, next): { joined: {vip,name}[], left: {vip,name}[] }`, где `prev`/`next` — массивы `{vip,name}`. Идентичность по `vip`.

- [ ] **Step 1: Написать падающий тест**

```js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { diffPeers } from '../../web/lib/peerdiff.js';

test('diffPeers находит вход и выход', () => {
  const prev = [{ vip: '1', name: 'A' }, { vip: '2', name: 'B' }];
  const next = [{ vip: '2', name: 'B' }, { vip: '3', name: 'C' }];
  const d = diffPeers(prev, next);
  assert.deepEqual(d.joined, [{ vip: '3', name: 'C' }]);
  assert.deepEqual(d.left, [{ vip: '1', name: 'A' }]);
});
test('нет изменений — пустые списки', () => {
  const a = [{ vip: '1', name: 'A' }];
  const d = diffPeers(a, a);
  assert.deepEqual(d, { joined: [], left: [] });
});
```

- [ ] **Step 2: Прогнать — FAIL**

Run: `node --test cmd/lanmesh-gui/webtest/test/peerdiff.test.mjs`
Expected: FAIL.

- [ ] **Step 3: Реализовать (`web/lib/peerdiff.js`)**

```js
export function diffPeers(prev, next) {
  const prevSet = new Set(prev.map(p => p.vip));
  const nextSet = new Set(next.map(p => p.vip));
  return {
    joined: next.filter(p => !prevSet.has(p.vip)).map(p => ({ vip: p.vip, name: p.name })),
    left: prev.filter(p => !nextSet.has(p.vip)).map(p => ({ vip: p.vip, name: p.name })),
  };
}
```

- [ ] **Step 4: Прогнать — PASS** · **Step 5: Commit**

Run: `node --test cmd/lanmesh-gui/webtest/test/peerdiff.test.mjs` → PASS.
```bash
git add cmd/lanmesh-gui/web/lib/peerdiff.js cmd/lanmesh-gui/webtest/test/peerdiff.test.mjs
git commit -m "feat(gui): peerdiff.js (вход/выход пиров) с тестами"
```

---

## Task 8: sparkline.js — SVG-график пинга

**Files:**
- Create: `cmd/lanmesh-gui/web/lib/sparkline.js`
- Test: `cmd/lanmesh-gui/webtest/test/sparkline.test.mjs`

**Interfaces:**
- Produces: `scalePoints(values, w, h): {x,y}[]` — масштабирует значения в область `w×h` (y инвертирован: больше значение → выше линия визуально не важна, важна валидность координат в диапазоне). `sparklineSVG(values, {w,h,stroke}): string` — валидный `<svg>` с `<polyline>`; при `values.length<2` возвращает пустую строку.

- [ ] **Step 1: Написать падающий тест**

```js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { scalePoints, sparklineSVG } from '../../web/lib/sparkline.js';

test('scalePoints кладёт точки в границы', () => {
  const pts = scalePoints([10, 20, 30], 40, 16);
  assert.equal(pts.length, 3);
  assert.equal(pts[0].x, 0);
  assert.equal(pts[2].x, 40);
  for (const p of pts) { assert.ok(p.y >= 0 && p.y <= 16); }
});
test('sparklineSVG возвращает svg с polyline', () => {
  const s = sparklineSVG([10, 20, 15], { w: 40, h: 16, stroke: '#46e6c0' });
  assert.match(s, /<svg[^>]*width="40"/);
  assert.match(s, /<polyline[^>]*points="/);
  assert.match(s, /#46e6c0/);
});
test('пустая строка при <2 точек', () => {
  assert.equal(sparklineSVG([5], { w: 40, h: 16, stroke: '#000' }), '');
});
```

- [ ] **Step 2: Прогнать — FAIL**

Run: `node --test cmd/lanmesh-gui/webtest/test/sparkline.test.mjs`
Expected: FAIL.

- [ ] **Step 3: Реализовать (`web/lib/sparkline.js`)**

```js
export function scalePoints(values, w, h) {
  const n = values.length;
  if (n === 0) return [];
  const min = Math.min(...values), max = Math.max(...values);
  const span = max - min || 1;
  const pad = 1;                                  // чтобы линия не липла к краям
  return values.map((v, i) => ({
    x: n === 1 ? 0 : +( (i / (n - 1)) * w ).toFixed(2),
    y: +( pad + (1 - (v - min) / span) * (h - 2 * pad) ).toFixed(2),
  }));
}
export function sparklineSVG(values, { w = 40, h = 16, stroke = '#46e6c0' } = {}) {
  if (!values || values.length < 2) return '';
  const pts = scalePoints(values, w, h).map(p => `${p.x},${p.y}`).join(' ');
  return `<svg class="spark" width="${w}" height="${h}" viewBox="0 0 ${w} ${h}" aria-hidden="true">`
    + `<polyline points="${pts}" fill="none" stroke="${stroke}" stroke-width="1.5" `
    + `stroke-linejoin="round" stroke-linecap="round"/></svg>`;
}
```

- [ ] **Step 4: Прогнать — PASS** · **Step 5: Commit**

Run: `node --test cmd/lanmesh-gui/webtest/test/sparkline.test.mjs` → PASS.
```bash
git add cmd/lanmesh-gui/web/lib/sparkline.js cmd/lanmesh-gui/webtest/test/sparkline.test.mjs
git commit -m "feat(gui): sparkline.js (SVG-график пинга) с тестами"
```

---

## Task 9: Оболочка, токены, шапка/рейл и переключение режима

**Files:**
- Modify: `cmd/lanmesh-gui/web/index.html`
- Modify: `cmd/lanmesh-gui/web/app.css` (токены §4 + оболочка/шапка/рейл/статус/pill/кнопки)
- Create: `cmd/lanmesh-gui/web/views/shell.js`
- Modify: `cmd/lanmesh-gui/web/app.js` (бутстрап: опрос, режим, монтаж оболочки)
- Test: `cmd/lanmesh-gui/webtest/test/shell.test.mjs`

**Interfaces:**
- Consumes: `dispName` (Task 2), `fmtUptime`/`plural` (Task 3).
- Produces: `renderHeader(state, mode): string`; `renderRail(state, activeView): string`; `statusPill(state): {cls, text}`; `pickMode(width): 'compact'|'detailed'` (порог 620px). `app.js` экспортирует поведение только через DOM (не тестируется юнитом).

- [ ] **Step 1: Написать падающий тест на чистые функции оболочки**

```js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { statusPill, pickMode, renderHeader } from '../../web/views/shell.js';

test('statusPill отражает состояние', () => {
  assert.equal(statusPill({ running: false, networks: [] }).cls, 'off');
  assert.equal(statusPill({ running: true, selfEndpoint: '', networks: [] }).cls, 'warn'); // нет внешнего адреса
  assert.equal(statusPill({ running: true, selfEndpoint: 'x', networks: [{ signalError: '' }] }).cls, 'on');
});
test('pickMode по ширине (порог 620)', () => {
  assert.equal(pickMode(400), 'compact');
  assert.equal(pickMode(900), 'detailed');
});
test('renderHeader экранирует и содержит бренд', () => {
  const h = renderHeader({ running: true, selfEndpoint: 'x', networks: [] }, 'compact');
  assert.match(h, /lan<b>mesh<\/b>/);
});
```

- [ ] **Step 2: Прогнать — FAIL**

Run: `node --test cmd/lanmesh-gui/webtest/test/shell.test.mjs`
Expected: FAIL.

- [ ] **Step 3: Реализовать `web/views/shell.js`**

```js
import { plural } from '../lib/format.js';

export function pickMode(width) { return width < 620 ? 'compact' : 'detailed'; }

export function statusPill(state) {
  if (!state.running) return { cls: 'off', text: 'не подключено' };
  const nets = state.networks || [];
  const noExt = !state.selfEndpoint;
  const anyBad = nets.some(n => n.signalError);
  if (noExt || anyBad) return { cls: 'warn', text: 'ограничено' };
  return { cls: 'on', text: nets.length + ' ' + plural(nets.length, 'сеть', 'сети', 'сетей') };
}

export function renderHeader(state, mode) {
  const p = statusPill(state);
  const expand = mode === 'compact'
    ? '<button class="iconbtn" data-act="expand" title="Развернуть">⤢</button>'
    : '<button class="iconbtn" data-act="collapse" title="Свернуть">⤡</button>';
  return `<div class="hd"><span class="wm">lan<b>mesh</b></span><span class="grow"></span>`
    + `<span class="pill ${p.cls}"><span class="pdot"></span>${p.text}</span>${expand}</div>`;
}

// Рейл подробного режима: список сетей + навигация видов.
export function renderRail(state, activeView) {
  const nets = (state.networks || []).map(n =>
    `<div class="netitem" data-view="list"><span class="pdot"></span>${escName(n.name)}`
    + `<span class="cnt">${(n.peers || []).length}</span></div>`).join('');
  const nav = [['list', '▤', 'Список'], ['map', '◎', 'Карта'], ['traffic', '▮', 'Трафик'], ['settings', '⚙', 'Настройки']]
    .map(([v, ic, t]) => `<div class="n ${v === activeView ? 'on' : ''}" data-view="${v}">${ic}&nbsp; ${t}</div>`).join('');
  return `<div class="rail"><div class="brand">lan<b>mesh</b></div>${nets}<div class="nav">${nav}</div></div>`;
}
import { dispName as escName } from '../lib/sanitize.js';
```

- [ ] **Step 4: Реализовать `web/index.html` (оболочка) и `web/app.js` (бутстрап)**

`index.html` `<body>`:
```html
<div id="root" data-mode="compact">
  <div id="toasts" class="toasts"></div>
  <aside id="rail"></aside>
  <main id="main"><header id="header"></header><div id="view"></div></main>
</div>
<script type="module" src="app.js"></script>
```
`app.js`:
```js
import { renderHeader, renderRail, pickMode } from './views/shell.js';

const POLL_MS = 1300;
let mode = localStorage.getItem('lm-mode') || pickMode(innerWidth);
let manual = localStorage.getItem('lm-mode') != null;
let activeView = 'list';
let lastState = { running: false, networks: [] };

function setMode(m) { mode = m; document.getElementById('root').dataset.mode = m; }
function render(state) {
  lastState = state;
  document.getElementById('header').innerHTML = renderHeader(state, mode);
  document.getElementById('rail').innerHTML = mode === 'detailed' ? renderRail(state, activeView) : '';
  // тело вида монтируется в Task 10/11 (renderView)
  if (window.renderView) document.getElementById('view').innerHTML = window.renderView(state, mode, activeView);
}
async function poll() {
  try { const r = await fetch('/api/state'); if (!r.ok) return; render(await r.json()); }
  catch (e) { /* переживём сбой */ }
}
// ⤢/⤡ и навигация
document.addEventListener('click', (e) => {
  const act = e.target.closest('[data-act]')?.dataset.act;
  if (act === 'expand') { manual = true; localStorage.setItem('lm-mode', 'detailed'); setMode('detailed'); render(lastState); return; }
  if (act === 'collapse') { manual = true; localStorage.setItem('lm-mode', 'compact'); setMode('compact'); render(lastState); return; }
  const v = e.target.closest('[data-view]')?.dataset.view;
  if (v) { activeView = v; render(lastState); }
});
// Отзывчивость: если пользователь не выбирал режим руками — следуем ширине окна.
new ResizeObserver(() => { if (!manual) setMode(pickMode(innerWidth)); render(lastState); }).observe(document.documentElement);

setMode(mode);
poll();
setInterval(poll, POLL_MS);
```

- [ ] **Step 5: Реализовать `web/app.css` — токены §4 + оболочка/шапка/рейл**

Внести полный набор токенов из спеки §4 в `:root`, затем классы: `#root[data-mode=compact]`/`[data-mode=detailed]` (грид/флекс раскладка, аврора-фон на `#root::before`), `.hd`, `.wm`, `.pill`(+`.on/.warn/.off`), `.pdot`, `.iconbtn`, `.rail`, `.brand`, `.netitem`, `.nav .n`, `.grow`, `.mono`, `.toasts`. Значения — из согласованных макетов `compact-v2`/`two-modes` (глубина стекла: `--glass`, `--glass-border`, `blur(16px) saturate(1.2)`, inset-хайлайт). Компактный режим прячет `#rail`; подробный — показывает рейл слева и ограничивает ширину `#main`.

- [ ] **Step 6: Прогнать юнит-тесты — PASS**

Run: `node --test cmd/lanmesh-gui/webtest/test/shell.test.mjs`
Expected: PASS (3 tests).

- [ ] **Step 7: Проверка в браузере (mock-харнесс)**

Run: `node cmd/lanmesh-gui/webtest/mock-server.mjs`
Открыть `http://127.0.0.1:8788`. Убедиться: тёмный стеклянный фон с аврора-свечением; шапка с брендом и статус-pill «1 сеть»; кнопка ⤢ переключает на подробный режим (появляется рейл слева, ⤡ возвращает); при сужении/расширении окна (без ручного клика — очистить `localStorage`) режим меняется по ширине. Через `/dev` → `disconnected`: pill «не подключено»; `noext`: pill «ограничено».

- [ ] **Step 8: Commit**

```bash
git add cmd/lanmesh-gui/web cmd/lanmesh-gui/webtest/test/shell.test.mjs
git commit -m "feat(gui): оболочка, токены Command Glass, шапка/рейл, переключение режима"
```

---

## Task 10: Компактный список участников

**Files:**
- Create: `cmd/lanmesh-gui/web/views/list.js`
- Modify: `cmd/lanmesh-gui/web/app.css` (компоненты строк/карточек/бейджей/сигнал-точек/warn-боксов)
- Modify: `cmd/lanmesh-gui/web/app.js` (назначить `window.renderView`)
- Test: `cmd/lanmesh-gui/webtest/test/list-compact.test.mjs`

**Interfaces:**
- Consumes: `dispName`, `esc` (Task 2); `fmtRtt`, `rttClass` (Task 3).
- Produces: `renderCompact(state): string`; `peerRowCompact(peer): string`; `netCardCompact(net): string`; `renderView(state, mode, view): string` (диспетчер: `compact` → `renderCompact`; подробный — заглушка до Task 11).

- [ ] **Step 1: Написать падающий тест**

```js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { peerRowCompact, renderCompact } from '../../web/views/list.js';

test('peerRowCompact: класс статуса, vip, пинг', () => {
  const s = peerRowCompact({ name: 'Мурад', vip: '25.44.12.9', status: 'direct', rttMs: 18, lastSeenMs: 800 });
  assert.match(s, /sdot direct/);
  assert.match(s, /25\.44\.12\.9/);
  assert.match(s, /18 мс/);
});
test('peerRowCompact санитизирует враждебное имя', () => {
  const s = peerRowCompact({ name: 'a\u202Eexe', vip: '1', status: 'direct', rttMs: 5 });
  assert.ok(!s.includes('\u202E'));            // bidi-override вычищен
});
test('renderCompact для пустой сети даёт подсказку', () => {
  const s = renderCompact({ running: true, selfEndpoint: 'x', networks: [{ name: 'myteam', tag: 't', signals: [], peers: [] }] });
  assert.match(s, /Пока никого|Позови/);
});
test('renderCompact показывает warn при отсутствии внешнего адреса', () => {
  const s = renderCompact({ running: true, selfEndpoint: '', networks: [{ name: 'n', tag: 't', signals: [], peers: [] }] });
  assert.match(s, /Внешний адрес неизвестен/);
});
```

- [ ] **Step 2: Прогнать — FAIL**

Run: `node --test cmd/lanmesh-gui/webtest/test/list-compact.test.mjs`
Expected: FAIL.

- [ ] **Step 3: Реализовать `web/views/list.js` (компактная часть)**

```js
import { dispName, esc } from '../lib/sanitize.js';
import { fmtRtt, rttClass } from '../lib/format.js';

const pngHtml = (peer) => {
  if (peer.status === 'connecting') return '<span class="png conn">подключение…</span>';
  const r = fmtRtt(peer.rttMs ?? -1);
  return r ? `<span class="png ${rttClass(peer.rttMs)}">${r}</span>` : '';
};
const sdotCls = (s) => s === 'direct' ? 'direct' : s === 'relay' ? 'relay' : 'conn';

export function peerRowCompact(peer) {
  return `<div class="row"><span class="sdot ${sdotCls(peer.status)}"></span>`
    + `<span class="nm">${dispName(peer.name || 'узел')}</span><span class="grow"></span>`
    + `<span class="ip mono">${esc(peer.vip)}</span>${pngHtml(peer)}</div>`;
}

export function netCardCompact(net) {
  const peers = (net.peers || []).slice().sort((a, b) => (a.vip < b.vip ? -1 : 1));
  const body = peers.length
    ? peers.map(peerRowCompact).join('')
    : `<div class="empty">Пока никого. Позови друга в сеть <b>${dispName(net.name)}</b> с тем же паролем "
      + "или пришли ссылку кнопкой «Пригласить».</div>`;
  const dot = net.signalError ? '🟡' : '🟢';
  return `<div class="netcard"><div class="netcard-hd"><span class="net-name">${dot} ${dispName(net.name)}</span>`
    + `<span class="cnt">· ${peers.length}</span><span class="grow"></span>`
    + `<button class="btn-ghost" data-invite="${esc(net.tag)}">⧉ Пригласить</button>`
    + `<button class="btn-ghost" data-leave="${esc(net.tag)}">Выйти</button></div>`
    + `<div class="rows">${body}</div></div>`;
}

export function renderCompact(state) {
  const nets = state.networks || [];
  const warn = state.running && !state.selfEndpoint
    ? '<div class="warnbox"><b>Внешний адрес неизвестен</b> — до тебя не достучатся. '
      + 'Обычно сеть режет исходящий UDP.</div>' : '';
  const self = state.running
    ? `<div class="self"><span><span class="k">твой IP</span><span class="v mono">${esc(state.selfIP || '—')}</span></span>`
      + `<span><span class="k">внешний</span><span class="v">${state.selfEndpoint ? 'определён' : 'не определён'}</span></span></div>` : '';
  const cards = nets.map(netCardCompact).join('');
  return warn + self + cards + addFormHtml(nets.length === 0);
}

// Форма добавления сети — раскрыта, пока сетей нет (поведение из старого UI).
export function addFormHtml(open) {
  return `<div class="netcard addcard"><div class="add-toggle" data-act="add-toggle">＋ Добавить сеть</div>`
    + `<div class="add-body" ${open ? '' : 'hidden'}>`
    + `<input id="f-invite" placeholder="lanmesh://join?net=…&pass=…" autocomplete="off">`
    + `<div class="frow"><input id="f-net" placeholder="имя сети" autocomplete="off">`
    + `<input id="f-pass" type="password" placeholder="пароль" autocomplete="off"></div>`
    + `<button class="btn-primary" data-act="add">Добавить сеть</button>`
    + `<div id="add-err" class="hint" hidden></div></div></div>`;
}

// Диспетчер видов. Подробный список приходит в Task 11.
export function renderView(state, mode, view) {
  if (mode === 'compact') return renderCompact(state);
  return window.renderDetailed ? window.renderDetailed(state, view) : renderCompact(state);
}
```

- [ ] **Step 4: Подключить диспетчер в `app.js`**

В конце `app.js` добавить:
```js
import { renderView } from './views/list.js';
window.renderView = renderView;
```

- [ ] **Step 5: Дописать компонентный CSS в `app.css`**

Классы (значения из макета `compact-v2`): `.self`(+`.k`/`.v`/`.v.up`), `.netcard`, `.netcard-hd`, `.net-name`, `.cnt`, `.rows`, `.row`(стеклянная, `blur(14px) saturate(1.2)`, inset-хайлайт), `.sdot`(+`.direct`/`.relay`/`.conn` со свечением по цвету), `.nm`, `.ip`, `.png`(+`.good`/`.ok`/`.bad`/`.conn`; классы `rtt-good/ok/bad` мапить на цвета через дополнительный класс или продублировать), `.empty`, `.warnbox`, `.btn-ghost`, `.btn-primary`, `.addcard`, `.add-toggle`, `.add-body`, `.frow`, `.hint`, `input`. Пинг красить по `rttClass`: добавить правила `.png.rtt-good{color:var(--accent-2)} .png.rtt-ok{color:var(--ping-ok)} .png.rtt-bad{color:var(--ping-bad)}` и обновить `pngHtml`, чтобы класс был `png ${rttClass(...)}` (уже так).

- [ ] **Step 6: Прогнать юнит-тесты — PASS**

Run: `node --test cmd/lanmesh-gui/webtest/test/list-compact.test.mjs`
Expected: PASS (4 tests).

- [ ] **Step 7: Проверка в браузере — все состояния**

Run: `node cmd/lanmesh-gui/webtest/mock-server.mjs` → `http://127.0.0.1:8788`, режим компактный.
Через `/dev` проверить: `team3` (4 участника, точки-статусы мятн/син/жёлт, пинги окрашены по порогам); `multi` (две карточки сетей); `empty` (подсказка «Позови друга»); `noext` (warn-бокс сверху); `sigerr` (жёлтая точка сети); `hostile` (имя показано БЕЗ разворота текста — санитизация сработала); `disconnected` (нет self-строки/карточек).

- [ ] **Step 8: Commit**

```bash
git add cmd/lanmesh-gui/web cmd/lanmesh-gui/webtest/test/list-compact.test.mjs
git commit -m "feat(gui): компактный список участников (карточки, статусы, пинг)"
```

---

## Task 11: Подробный режим — Sidebar Dashboard со списком

**Files:**
- Modify: `cmd/lanmesh-gui/web/views/list.js` (`renderDetailed`, `peerRowDetailed`, `qualityTile`)
- Modify: `cmd/lanmesh-gui/web/app.css` (`.dmain`, `.tiles`, `.tile`, `.drow`, `.av`, `.bars`, `.badge`)
- Modify: `cmd/lanmesh-gui/web/app.js` (передать `histories` в рендер — подготовка к Task 12)
- Test: `cmd/lanmesh-gui/webtest/test/list-detailed.test.mjs`

**Interfaces:**
- Consumes: `dispName`, `esc`; `fmtRtt`, `rttClass`; `quality` (Task 6).
- Produces: `renderDetailed(state, view, histories): string`; `peerRowDetailed(peer, history): string`; заглушки видов `map`/`traffic`/`settings` («скоро»). Экспонируется как `window.renderDetailed`.

- [ ] **Step 1: Написать падающий тест**

```js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { peerRowDetailed, renderDetailed } from '../../web/views/list.js';

test('peerRowDetailed: аватар-инициал, бейдж статуса, пинг', () => {
  const s = peerRowDetailed({ name: 'Мурад', vip: '25.44.12.9', status: 'direct', rttMs: 18, signals: [true] }, []);
  assert.match(s, /badge direct/);
  assert.match(s, /25\.44\.12\.9/);
  assert.match(s, />М</);                         // инициал аватара
});
test('renderDetailed map/traffic — заглушки «скоро»', () => {
  const st = { running: true, selfEndpoint: 'x', networks: [{ name: 'n', tag: 't', signals: [], peers: [] }] };
  assert.match(renderDetailed(st, 'map', {}), /скоро/i);
  assert.match(renderDetailed(st, 'traffic', {}), /скоро/i);
});
test('renderDetailed list содержит плитку качества', () => {
  const st = { running: true, selfEndpoint: 'x', networks: [{ name: 'n', tag: 't', signals: [],
    peers: [{ name: 'A', vip: '1', status: 'direct', rttMs: 18, signals: [true] }] }] };
  assert.match(renderDetailed(st, 'list', {}), /Качество/);
});
```

- [ ] **Step 2: Прогнать — FAIL**

Run: `node --test cmd/lanmesh-gui/webtest/test/list-detailed.test.mjs`
Expected: FAIL.

- [ ] **Step 3: Реализовать `renderDetailed` в `web/views/list.js`**

```js
import { quality } from '../lib/quality.js';
import { sparklineSVG } from '../lib/sparkline.js';   // используется в Task 12

const initial = (name) => dispName((name || 'у').trim().charAt(0).toUpperCase());
const avClass = (vip) => 'av' + (['m', 'k', 's', 'd'][(parseInt(vip.replace(/\D/g, '').slice(-2) || '0', 10)) % 4]);
const barsHtml = (signals) => {
  const on = (signals || []).filter(Boolean).length, total = Math.max(1, (signals || []).length);
  return '<span class="bars">' + Array.from({ length: 4 }, (_, i) =>
    `<i class="${i < Math.round(on / total * 4) ? 'on' : ''}"></i>`).join('') + '</span>';
};

export function peerRowDetailed(peer, history = []) {
  const q = quality(peer.status, peer.rttMs ?? -1, history);
  const badge = peer.status === 'connecting' ? '<span class="badge conn">подключение</span>'
    : `<span class="badge ${peer.status}">${peer.status}</span>`;
  const spark = history.length >= 2
    ? sparklineSVG(history, { w: 120, h: 24, stroke: `var(--q-${q.level})` }) : '<span class="spark-empty"></span>';
  return `<div class="drow" data-q="${q.level}"><span class="av ${avClass(peer.vip)}">${initial(peer.name)}</span>`
    + `<span class="who"><span class="nm">${dispName(peer.name || 'узел')}</span>`
    + `<span class="ip mono">${esc(peer.vip)}</span></span>${spark}<span class="grow"></span>`
    + `${barsHtml(peer.signals)}${badge}${pngHtml(peer)}</div>`;
}

function qualityTile(net) {
  const peers = (net.peers || []).filter(p => p.status !== 'connecting');
  const direct = peers.filter(p => p.status === 'direct').length;
  const relay = peers.filter(p => p.status === 'relay').length;
  const worst = peers.some(p => quality(p.status, p.rttMs, []).level === 'bad') ? 'bad'
    : relay ? 'ok' : 'good';
  const label = { good: 'хорошее', ok: 'среднее', bad: 'плохое' }[worst];
  return `<div class="tile"><div class="k">Качество связи</div>`
    + `<div class="big q-${worst}">${label}</div><div class="sub">${direct} direct · ${relay} relay</div></div>`;
}

export function renderDetailed(state, view, histories = {}) {
  const net = (state.networks || [])[0];
  if (view === 'settings') return `<div class="dmain"><div class="soon">Настройки серверов — перенос в Task 13.</div></div>`;
  if (view === 'map') return `<div class="dmain"><div class="soon">◎ Карта сети — скоро (Phase 4).</div></div>`;
  if (view === 'traffic') return `<div class="dmain"><div class="soon">▮ Трафик — скоро (Phase 3).</div></div>`;
  if (!net) return `<div class="dmain"><div class="soon">Нет активных сетей. Добавь сеть в компактном режиме.</div></div>`;
  const peers = (net.peers || []).slice().sort((a, b) => (a.vip < b.vip ? -1 : 1));
  const rows = peers.map(p => peerRowDetailed(p, histories[p.vip] || [])).join('') || '<div class="empty">Пока никого.</div>';
  return `<div class="dmain"><div class="dhd"><div><div class="title">${dispName(net.name)}</div>`
    + `<div class="sub">${peers.length} участников</div></div><span class="grow"></span>`
    + `<button class="btn-ghost" data-invite="${esc(net.tag)}">⧉ Пригласить</button></div>`
    + `<div class="tiles">${qualityTile(net)}`
    + `<div class="tile"><div class="k">Трафик</div><div class="big dim">— <small>Phase 3</small></div></div>`
    + `<div class="tile"><div class="k">Участников</div><div class="big">${peers.length}</div></div></div>`
    + `<div class="drows">${rows}</div></div>`;
}
window.renderDetailed = renderDetailed;
```

- [ ] **Step 4: Дописать CSS + токены качества в `app.css`**

Добавить в `:root`: `--q-good: var(--accent-2); --q-ok: var(--ping-ok); --q-bad: var(--ping-bad); --q-connecting: var(--fg-mut);`. Классы (из макета `two-modes`): `.dmain`, `.dhd`, `.title`, `.sub`, `.tiles`(grid 3), `.tile`(+`.k`/`.big`/`.sub`/`.dim`), `.q-good/ok/bad` (цвет), `.drows`, `.drow`(стеклянная), `.av`(+`.avm/avk/avs/avd` градиенты-инициалы), `.who`, `.bars i`(+`.on`), `.badge`(+`.direct`/`.relay`/`.conn`), `.spark`, `.spark-empty`(ширина-заглушка), `.soon`(центр, приглушённо).

- [ ] **Step 5: Прогнать юнит-тесты — PASS**

Run: `node --test cmd/lanmesh-gui/webtest/test/list-detailed.test.mjs`
Expected: PASS (3 tests).

- [ ] **Step 6: Проверка в браузере**

Mock-харнесс, кликнуть ⤢ (подробный). Сценарий `team3`: рейл слева с сетью, навигация; основная область — заголовок сети, 3 плитки (Качество «среднее/хорошее», Трафик «— Phase 3», Участников 4), богатые строки с аватар-инициалами, сигнал-барами, бейджами, пингом. Клик по «Карта»/«Трафик» → «скоро». `multi`: в рейле две сети.

- [ ] **Step 7: Commit**

```bash
git add cmd/lanmesh-gui/web cmd/lanmesh-gui/webtest/test/list-detailed.test.mjs
git commit -m "feat(gui): подробный режим — dashboard со списком, плитки, качество"
```

---

## Task 12: Живые фичи — накопление истории RTT, sparkline, тосты

**Files:**
- Modify: `cmd/lanmesh-gui/web/app.js` (история RTT в цикле опроса; передача `histories` в рендер; тосты по diffPeers)
- Modify: `cmd/lanmesh-gui/web/app.css` (`.toast`, анимации появления)
- Test: `cmd/lanmesh-gui/webtest/test/live.test.mjs`

**Interfaces:**
- Consumes: `RttHistory` (Task 5), `diffPeers` (Task 7), `sparklineSVG` (Task 8).
- Produces: чистая функция `collectPeers(state): {vip,name}[]` (плоский список всех пиров всех сетей) — экспортируется из `app.js`-адаптера `web/lib/collect.js` для тестируемости.

- [ ] **Step 1: Написать падающий тест на сбор пиров и интеграцию истории**

```js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { collectPeers } from '../../web/lib/collect.js';
import { RttHistory } from '../../web/lib/rtt-history.js';

test('collectPeers собирает пиров из всех сетей', () => {
  const st = { networks: [{ peers: [{ vip: '1', name: 'A', rttMs: 10 }] }, { peers: [{ vip: '2', name: 'B', rttMs: 20 }] }] };
  assert.deepEqual(collectPeers(st).map(p => p.vip), ['1', '2']);
});
test('история наполняется из последовательных снимков', () => {
  const h = new RttHistory();
  for (const rtt of [10, 12, 11]) h.push('1', rtt);
  assert.deepEqual(h.get('1'), [10, 12, 11]);
});
```

- [ ] **Step 2: Прогнать — FAIL**

Run: `node --test cmd/lanmesh-gui/webtest/test/live.test.mjs`
Expected: FAIL.

- [ ] **Step 3: Реализовать `web/lib/collect.js`**

```js
export function collectPeers(state) {
  const out = [];
  for (const n of state.networks || []) for (const p of n.peers || []) out.push({ vip: p.vip, name: p.name, rttMs: p.rttMs, status: p.status });
  return out;
}
```

- [ ] **Step 4: Интегрировать в `app.js`**

Добавить историю и тосты в цикл опроса:
```js
import { RttHistory } from './lib/rtt-history.js';
import { diffPeers } from './lib/peerdiff.js';
import { collectPeers } from './lib/collect.js';
import { dispName } from './lib/sanitize.js';

const histories = new RttHistory(40);
let prevPeers = [];
const histSnapshot = () => { const o = {}; for (const [k] of histories.map) o[k] = histories.get(k); return o; };

function ingest(state) {
  const peers = collectPeers(state);
  for (const p of peers) histories.push(p.vip, p.rttMs ?? -1);
  histories.prune(peers.map(p => p.vip));
  const { joined, left } = diffPeers(prevPeers, peers);
  if (prevPeers.length) {                          // не тостим первый снимок (стартовый состав)
    for (const p of joined) toast(`${dispName(p.name)} в сети`, 'in');
    for (const p of left) toast(`${dispName(p.name)} вышел`, 'out');
  }
  prevPeers = peers;
}
function toast(text, kind) {
  const el = document.createElement('div');
  el.className = `toast ${kind}`; el.innerHTML = text;
  const box = document.getElementById('toasts'); box.appendChild(el);
  setTimeout(() => el.classList.add('show'), 10);
  setTimeout(() => { el.classList.remove('show'); setTimeout(() => el.remove(), 300); }, 3500);
}
```
Вызвать `ingest(state)` в начале `render(state)`; передать `histSnapshot()` в `window.renderView`/`renderDetailed`. Обновить сигнатуру вызова: `renderView(state, mode, activeView, histSnapshot())` и `renderView` прокинуть `histories` в `renderDetailed(state, view, histories)`. В компактном режиме sparkline не рисуем (по решению — компакт минималистичен).

- [ ] **Step 5: Добавить CSS тостов** — `.toasts`(fixed, стек сверху-справа), `.toast`(стеклянный, слева цветная полоса: `.in`→мята, `.out`→приглушённый), `.toast.show`(translate/opacity переход).

- [ ] **Step 6: Прогнать юнит-тесты — PASS**

Run: `node --test cmd/lanmesh-gui/webtest/test/live.test.mjs`
Expected: PASS (2 tests).

- [ ] **Step 7: Проверка в браузере**

Mock-харнесс, подробный режим, `team3`: за несколько опросов (~5–10 с) под строками у пиров появляются растущие sparkline (mock джиттерит RTT), цвет линии = уровень качества. Переключение `/dev` `team3`↔`multi` (состав пиров меняется) → всплывают тосты «… в сети» / «… вышел» справа сверху и исчезают.

- [ ] **Step 8: Commit**

```bash
git add cmd/lanmesh-gui/web cmd/lanmesh-gui/webtest/test/live.test.mjs
git commit -m "feat(gui): живые фичи — история пинга, sparkline, тосты вход/выход"
```

---

## Task 13: Подключение действий и настроек к API

**Files:**
- Modify: `cmd/lanmesh-gui/web/app.js` (обработчики: add/leave/invite/disconnect/senddiag/sendlogs)
- Create: `cmd/lanmesh-gui/web/views/settings.js` (панель серверов + диагностика)
- Modify: `cmd/lanmesh-gui/web/views/list.js` (вид `settings` → `renderSettings`)
- Modify: `cmd/lanmesh-gui/web/app.css` (формы настроек)
- Test: `cmd/lanmesh-gui/webtest/test/settings.test.mjs`

**Interfaces:**
- Consumes: `parseInvite` (Task 4).
- Produces: `renderSettings(state): string`; хелпер `postJSON(path, body): Promise<Response>` в `app.js`. Действия через делегирование по `data-*` атрибутам (`data-act`, `data-invite`, `data-leave`).

- [ ] **Step 1: Написать падающий тест на renderSettings**

```js
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { renderSettings } from '../../web/views/settings.js';

test('renderSettings содержит поля сигналок, релея и диагностику', () => {
  const s = renderSettings({ running: false, sendLogs: true });
  assert.match(s, /сигналк/i);
  assert.match(s, /relay|ретранслятор/i);
  assert.match(s, /диагностик/i);
});
test('renderSettings блокирует правку серверов при поднятой сети', () => {
  const s = renderSettings({ running: true, sendLogs: true });
  assert.match(s, /disabled/);
});
```

- [ ] **Step 2: Прогнать — FAIL**

Run: `node --test cmd/lanmesh-gui/webtest/test/settings.test.mjs`
Expected: FAIL.

- [ ] **Step 3: Реализовать `web/views/settings.js`**

```js
// Адреса серверов НЕ показываем (как в текущем UI) — только поля для ввода своих.
export function renderSettings(state) {
  const locked = state.running ? 'disabled' : '';
  const lockNote = state.running ? 'Сеть подключена — отключись, чтобы менять серверы.' : 'Пусто = стандартные серверы.';
  return `<div class="dmain"><h2 class="soon-h">Серверы</h2>`
    + `<label>сигналки (по одной ссылке на строку)</label>`
    + `<textarea id="s-signals" rows="3" placeholder="https://…" ${locked}></textarea>`
    + `<label>ретранслятор (relay), host:port</label>`
    + `<input id="s-relay" placeholder="host:port" ${locked}>`
    + `<div class="frow"><button class="btn-primary" data-act="cfg-save" ${locked}>Сохранить</button>`
    + `<button class="btn-ghost" data-act="cfg-reset" ${locked}>Сбросить</button></div>`
    + `<div class="hint">${lockNote}</div>`
    + `<h2 class="soon-h">Диагностика</h2>`
    + `<button class="btn-ghost" data-act="senddiag">📤 Отправить диагностику</button>`
    + `<label class="chk"><input type="checkbox" id="s-logs" ${state.sendLogs ? 'checked' : ''} data-act="sendlogs"> автоотправка логов</label>`
    + `<div id="diag-note" class="hint"></div></div>`;
}
```
В `views/list.js` заменить заглушку `settings`:
```js
import { renderSettings } from './settings.js';
// в renderDetailed: if (view === 'settings') return renderSettings(state);
```

- [ ] **Step 4: Реализовать обработчики действий в `app.js`**

```js
import { parseInvite } from './lib/invite.js';
const postJSON = (path, body) => fetch(path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body || {}) });

document.addEventListener('click', async (e) => {
  const t = e.target;
  const act = t.closest('[data-act]')?.dataset.act;
  if (act === 'add-toggle') { const b = t.closest('.addcard').querySelector('.add-body'); b.hidden = !b.hidden; return; }
  if (act === 'add') {
    const net = document.getElementById('f-net').value.trim(), pass = document.getElementById('f-pass').value;
    const err = document.getElementById('add-err');
    if (!net || !pass) { err.hidden = false; err.textContent = 'Нужны имя сети и пароль.'; return; }
    const body = { network: net, password: pass };
    const inv = parseInvite(document.getElementById('f-invite').value);
    if ((inv.net || '').trim() === net) { if (inv.sigs.length) body.signals = inv.sigs; if (inv.relay !== null) body.relay = inv.relay; }
    const r = await postJSON('/api/addnetwork', body); const j = await r.json();
    if (!r.ok) { err.hidden = false; err.textContent = 'Ошибка: ' + (j.error || r.status); } else poll();
    return;
  }
  if (act === 'senddiag') { const n = document.getElementById('diag-note'); const j = await (await postJSON('/api/senddiag')).json();
    n.textContent = j.tag ? ('✓ отправлено, код: ' + j.tag) : ('Ошибка: ' + (j.error || '')); return; }
  if (act === 'cfg-save') {
    const signals = document.getElementById('s-signals').value.split('\n').map(s => s.trim()).filter(Boolean);
    const relay = document.getElementById('s-relay').value.trim();
    if (!signals.length) return; await postJSON('/api/settings', { signals, relay }); poll(); return;
  }
  if (act === 'cfg-reset') { await postJSON('/api/settings', { signals: [], relay: '' }); poll(); return; }
  const inviteTag = t.closest('[data-invite]')?.dataset.invite;
  if (inviteTag != null) { const j = await (await fetch('/api/invite?tag=' + encodeURIComponent(inviteTag))).json();
    if (j.link) { await navigator.clipboard.writeText(j.link); t.textContent = '✓ скопировано'; setTimeout(() => t.textContent = '⧉ Пригласить', 1500); } return; }
  const leaveTag = t.closest('[data-leave]')?.dataset.leave;
  if (leaveTag != null) { if (confirm('Выйти из этой сети?')) { await postJSON('/api/leavenetwork', { tag: leaveTag }); poll(); } return; }
});
document.addEventListener('change', async (e) => {
  if (e.target.closest('[data-act]')?.dataset.act === 'sendlogs') await postJSON('/api/sendlogs', { enabled: e.target.checked });
});
```

- [ ] **Step 5: CSS форм** — `label`, `textarea`, `input`, `.chk`, `.soon-h`, `.frow` (переиспользовать токены).

- [ ] **Step 6: Прогнать юнит-тесты — PASS**

Run: `node --test cmd/lanmesh-gui/webtest/test/settings.test.mjs`
Expected: PASS (2 tests).

- [ ] **Step 7: Проверка в браузере (mock принимает POST → {ok:true})**

Mock-харнесс: компактный режим — раскрыть «＋ Добавить сеть», ввести имя+пароль, «Добавить» → нет ошибки, идёт `poll`. Кнопка «Пригласить» → в буфере ссылка `lanmesh://…`, текст «✓ скопировано». «Выйти» → подтверждение. Подробный режим → «Настройки»: поля серверов (disabled при `team3`, т.к. running=true; на `disconnected` — активны), «Отправить диагностику» → нота с кодом, чекбокс автоотправки шлёт POST.

- [ ] **Step 8: Commit**

```bash
git add cmd/lanmesh-gui/web cmd/lanmesh-gui/webtest/test/settings.test.mjs
git commit -m "feat(gui): действия (сети/инвайт/настройки/диагностика) на API"
```

---

## Task 14: Финальная сквозная проверка Phase 1

**Files:**
- Create: `cmd/lanmesh-gui/webtest/CHECKLIST.md` (зафиксировать результаты проверки)

- [ ] **Step 1: Прогнать ВСЕ юнит-тесты**

Run: `node --test cmd/lanmesh-gui/webtest/test/`
Expected: PASS (все тесты, ~24+).

- [ ] **Step 2: Сквозная визуальная проверка по сценариям**

Run: `node cmd/lanmesh-gui/webtest/mock-server.mjs`, пройти через `/dev` каждый сценарий в ОБОИХ режимах и отметить в `CHECKLIST.md`:
- [ ] `disconnected` — «не подключено», нет карточек.
- [ ] `empty` — подсказка «Позови друга».
- [ ] `team3` — 4 пира, статусы/пинги/цвета верны; sparkline растут; тосты при смене сценария.
- [ ] `multi` — две сети (рейл в подробном).
- [ ] `noext` — warn-бокс, pill «ограничено».
- [ ] `sigerr` — индикатор проблемы сигналки, pill «ограничено».
- [ ] `hostile` — имя `a\u202Egnp.exe` показано без разворота текста (санитизация).
- [ ] Переключение ⤢/⤡ и авто-режим по ширине окна.
- [ ] Действия: добавить/выйти/пригласить/настройки/диагностика.

- [ ] **Step 3: Зафиксировать чеклист и закоммитить**

```bash
git add cmd/lanmesh-gui/webtest/CHECKLIST.md
git commit -m "test(gui): чеклист сквозной проверки Phase 1 (все сценарии, оба режима)"
```

- [ ] **Step 4: Пуш ветки**

```bash
git push origin gui-redesign
```

---

## Self-Review (выполнено при написании плана)

- **Покрытие спеки:** визуальный язык §4 → Task 9 (токены) + Task 10/11 (компоненты); два режима §6 → Task 9 (переключение) + 10 (компакт) + 11 (подробный); сохранение функциональности §7 → Task 13 (действия) + перенос `dispName`/`parseInvite`/форматтеров (Tasks 2–4); живые фичи §8 P1 → Tasks 5,6,8 (логика) + 11,12 (интеграция); проверка §11 → Task 1 (mock) + все Step «Проверка в браузере» + Task 14. Виды «Карта»/«Трафик» — заглушки (P3/P4), как и заявлено.
- **Плейсхолдеры:** отсутствуют; каждый шаг с кодом содержит полный код. CSS-шаги перечисляют конкретные классы и источник значений (согласованные макеты) — не «сделай красиво».
- **Согласованность типов:** `renderView`(list.js) вызывает `renderDetailed`(list.js) и `renderCompact`; `histories` — снимок `{vip: number[]}` из `RttHistory.get`; `quality(status,rttMs,history)`, `sparklineSVG(values,{w,h,stroke})`, `diffPeers(prev,next)`, `parseInvite(text)`, `dispName(s)` — сигнатуры совпадают между определением и использованием.
