// lanmesh signal на Deno Deploy — порт Cloudflare-воркера (worker/signal-worker.js).
// Тот же протокол и семантика; для клиента это ещё одна сигналка в пуле:
//
//   lanmesh -signal https://<твоё-приложение>.deno.dev
//   (или добавить URL в список «Серверы» панели / signals в config.json)
//
// Зачем отдельно от воркера: другой провайдер и домен (*.deno.dev, не *.workers.dev).
// У части провайдеров DPI режет workers.dev по имени в ClientHello — разнородный пул
// сигналок это обходит (движок ходит во все сразу и сливает списки участников).
//
// Состояние — В ПАМЯТИ (как у Go-сигналки cmd/lanmesh-signal), без внешней БД: новый
// Deno Deploy контейнерный и не даёт Deno KV. Для сигналки это нормально — пиры
// перерегистрируются каждые 20с, так что после перезапуска инстанса реестр
// восстанавливается сам. Ключа сети сервер не знает и трафик расшифровать не может:
// видит только несекретный тег (хэш имени+пароля) и endpoint'ы.
//
// NB: если платформа поднимет НЕСКОЛЬКО инстансов, память между ними не общая —
// для компании друзей (низкий трафик, обычно один инстанс) это не проблема, но
// именно поэтому в пуле держим ещё и воркер/свой сервер.

const PEER_TTL_MS = 60_000; // ~3 пропущенных регистрации -> пир выпадает
const LOG_TTL_MS = 3_600_000; // час
const LOG_MAX_LINES = 200; // строк в одной пачке
const LOG_MAX_LINE = 500; // символов в строке
const LOG_MAX_KEEP = 2000; // строк на сеть — чтобы память не росла
const MAX_NETS = 1000; // потолок сетей в реестре (от OOM при флуде случайными тегами)
const MAX_PEERS = 256; // потолок участников в сети

interface PeerInfo {
  id: string;
  name: string;
  vip: string;
  eps: string[];
}
interface PeerRec {
  info: PeerInfo;
  seen: number;
}
interface LogBatch {
  at: number;
  name: string;
  peer: string;
  lines: string[];
}
interface Net {
  peers: Map<string, PeerRec>;
  logs: LogBatch[];
}

const nets = new Map<string, Net>();

// Периодическая уборка протухших сетей/пиров, чтобы память не текла (аналог sweep
// в Go). Пиры/логи чистятся и на доступе, это лишь подстраховка для «тихих» сетей.
setInterval(() => {
  const now = Date.now();
  for (const [tag, n] of nets) {
    for (const [id, r] of n.peers) {
      if (now - r.seen > PEER_TTL_MS) n.peers.delete(id);
    }
    trimLogs(n, now);
    if (n.peers.size === 0 && n.logs.length === 0) nets.delete(tag);
  }
}, 5 * 60_000);

Deno.serve(async (req: Request): Promise<Response> => {
  const url = new URL(req.url);

  if (url.pathname === "/" || url.pathname === "/health") {
    return new Response("lanmesh signal ok\n", { status: 200 });
  }

  // Тег определяет сеть. Для POST он в теле, для /logs — в query.
  let tag = "";
  // deno-lint-ignore no-explicit-any
  let body: any = null;

  if (req.method === "POST" && (url.pathname === "/register" || url.pathname === "/log")) {
    try {
      body = await req.json();
    } catch {
      return json({ error: "bad json" }, 400);
    }
    tag = String(body.net || "");
  } else if (req.method === "GET" && url.pathname === "/logs") {
    tag = String(url.searchParams.get("net") || "");
  } else {
    return new Response("not found", { status: 404 });
  }

  if (!/^[0-9a-f]{64}$/.test(tag)) {
    return url.pathname === "/logs"
      ? text("нужен ?net=<64 hex>\n", 400)
      : json({ error: "bad net" }, 400);
  }

  try {
    if (url.pathname === "/register") return register(tag, body);
    if (url.pathname === "/log") return addLog(tag, body);
    if (url.pathname === "/logs") return dumpLogs(tag);
  } catch (e) {
    return json({ error: String(e) }, 500);
  }
  return new Response("not found", { status: 404 });
});

// netForWrite возвращает сеть по тегу, создавая при нехватке (с учётом потолка).
function netForWrite(tag: string): Net | null {
  let n = nets.get(tag);
  if (!n) {
    if (nets.size >= MAX_NETS) return null; // реестр переполнен — не заводим
    n = { peers: new Map(), logs: [] };
    nets.set(tag, n);
  }
  return n;
}

// --- пиры ---------------------------------------------------------------------

// deno-lint-ignore no-explicit-any
function register(tag: string, req: any): Response {
  const id = String(req.id || "");
  if (!/^[0-9a-f]{32}$/.test(id)) return json({ error: "bad id" }, 400);

  const n = netForWrite(tag);
  if (!n) return json({ error: "реестр переполнен" }, 503);

  const now = Date.now();
  if (!n.peers.has(id) && n.peers.size >= MAX_PEERS) {
    return json({ error: "сеть переполнена" }, 503);
  }
  const self: PeerInfo = {
    id,
    name: sanitize(req.name),
    vip: sanitize(req.vip),
    eps: sanitizeEndpoints(req.eps),
  };
  n.peers.set(id, { info: self, seen: now });

  const peers: PeerInfo[] = [];
  for (const [pid, r] of n.peers) {
    if (now - r.seen > PEER_TTL_MS) {
      n.peers.delete(pid);
      continue;
    }
    if (pid !== id) peers.push(r.info);
  }
  peers.sort((a, b) => (a.vip < b.vip ? -1 : a.vip > b.vip ? 1 : 0));
  return json({ self, peers });
}

// --- диагностика --------------------------------------------------------------

// deno-lint-ignore no-explicit-any
function addLog(tag: string, req: any): Response {
  const id = String(req.id || "");
  if (!/^[0-9a-f]{32}$/.test(id)) return json({ error: "bad id" }, 400);
  if (!Array.isArray(req.lines) || req.lines.length === 0) {
    return json({ error: "нет строк" }, 400);
  }
  const n = netForWrite(tag);
  if (!n) return json({ error: "реестр переполнен" }, 503);

  const lines = req.lines
    .slice(0, LOG_MAX_LINES)
    .map((l: unknown) => String(l || "").slice(0, LOG_MAX_LINE));

  n.logs.push({ at: Date.now(), name: sanitize(req.name), peer: id.slice(0, 8), lines });
  trimLogs(n, Date.now());
  return json({ ok: true, stored: lines.length });
}

// trimLogs режет логи по возрасту, потом по объёму. Вызывать при добавлении/уборке.
function trimLogs(n: Net, now: number): void {
  const cutoff = now - LOG_TTL_MS;
  n.logs = n.logs.filter((b) => b.at >= cutoff);
  let total = 0;
  for (const b of n.logs) total += b.lines.length;
  while (total > LOG_MAX_KEEP && n.logs.length > 0) {
    total -= n.logs[0].lines.length;
    n.logs.shift();
  }
}

function dumpLogs(tag: string): Response {
  const n = nets.get(tag);
  if (!n) return text("логов нет (клиенты не слали или истёк час)\n");
  trimLogs(n, Date.now());
  const out: string[] = [];
  for (const b of n.logs) {
    const who = `${b.name || "?"} (${b.peer})`;
    for (const l of b.lines) out.push(`${who}\t${l}`);
  }
  if (out.length === 0) return text("логов нет (клиенты не слали или истёк час)\n");
  return text(out.join("\n") + "\n");
}

// --- утилиты ------------------------------------------------------------------

function sanitize(s: unknown): string {
  // Только печатный ASCII, до 64 символов — чтобы имя/vip не раздували запись и не
  // тащили управляющие/bidi-символы.
  return String(s || "").slice(0, 64).replace(/[^\x20-\x7e]/g, "");
}

function sanitizeEndpoints(eps: unknown): string[] {
  if (!Array.isArray(eps)) return [];
  const out: string[] = [];
  for (const e of eps) {
    const s = String(e || "");
    if (/^\d{1,3}(\.\d{1,3}){3}:\d{1,5}$/.test(s)) out.push(s);
    if (out.length >= 8) break;
  }
  return out;
}

// deno-lint-ignore no-explicit-any
function json(obj: any, status = 200): Response {
  return new Response(JSON.stringify(obj), {
    status,
    headers: { "Content-Type": "application/json; charset=utf-8", "Cache-Control": "no-store" },
  });
}

function text(s: string, status = 200): Response {
  return new Response(s, {
    status,
    headers: { "Content-Type": "text/plain; charset=utf-8", "Cache-Control": "no-store" },
  });
}
