// lanmesh signal на Deno Deploy — порт Cloudflare-воркера (worker/signal-worker.js)
// на Deno KV. Тот же протокол и семантика; для клиента это ещё одна сигналка в пуле:
//
//   lanmesh -signal https://<твой-проект>.deno.dev
//   (или добавить URL в список «Серверы» панели / signals в config.json)
//
// Зачем отдельно от воркера: другой провайдер и домен (*.deno.dev, не *.workers.dev).
// У части провайдеров DPI режет workers.dev по имени в ClientHello — разнородный пул
// сигналок это обходит (движок ходит во все сразу и сливает списки участников).
//
// Состояние — в Deno KV (не в памяти): переживает выгрузку инстанса, как storage у
// Durable Object. TTL записей задаём через expireIn — протухшие пиры/логи KV удаляет
// сам, отдельная уборка не нужна. Ключа сети сервер не знает и трафик расшифровать
// не может: видит только несекретный тег (хэш имени+пароля) и endpoint'ы.
//
// Деплой: см. deploy/deno/README.md (нужен флаг --unstable-kv или Deno Deploy, где
// KV включён по умолчанию).

const PEER_TTL_MS = 60_000; // ~3 пропущенных регистрации -> пир выпадает
const LOG_TTL_MS = 3_600_000; // час
const LOG_MAX_LINES = 200; // строк в одной пачке
const LOG_MAX_LINE = 500; // символов в строке
const LOG_MAX_KEEP = 2000; // строк на сеть — чтобы хранилище не росло
const MAX_PEERS = 256; // потолок участников в ответе (защита клиента)

const kv = await Deno.openKv();

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
    if (url.pathname === "/register") return await register(tag, body);
    if (url.pathname === "/log") return await addLog(tag, body);
    if (url.pathname === "/logs") return await dumpLogs(tag);
  } catch (e) {
    return json({ error: String(e) }, 500);
  }
  return new Response("not found", { status: 404 });
});

// --- пиры ---------------------------------------------------------------------

// deno-lint-ignore no-explicit-any
async function register(tag: string, req: any): Promise<Response> {
  const id = String(req.id || "");
  if (!/^[0-9a-f]{32}$/.test(id)) return json({ error: "bad id" }, 400);

  const self = {
    id,
    name: sanitize(req.name),
    vip: sanitize(req.vip),
    eps: sanitizeEndpoints(req.eps),
  };
  // Пишем себя с TTL — протухнет сам. seen храним для совместимости формата.
  await kv.set(["peer", tag, id], { info: self, seen: Date.now() }, { expireIn: PEER_TTL_MS });

  const peers = [];
  for await (const e of kv.list({ prefix: ["peer", tag] })) {
    // deno-lint-ignore no-explicit-any
    const rec = e.value as any;
    if (rec?.info && rec.info.id !== id) {
      peers.push(rec.info);
      if (peers.length >= MAX_PEERS) break;
    }
  }
  return json({ self, peers });
}

// --- диагностика --------------------------------------------------------------

// deno-lint-ignore no-explicit-any
async function addLog(tag: string, req: any): Promise<Response> {
  const id = String(req.id || "");
  if (!/^[0-9a-f]{32}$/.test(id)) return json({ error: "bad id" }, 400);
  if (!Array.isArray(req.lines) || req.lines.length === 0) {
    return json({ error: "нет строк" }, 400);
  }

  const lines = req.lines
    .slice(0, LOG_MAX_LINES)
    .map((l: unknown) => String(l || "").slice(0, LOG_MAX_LINE));

  const at = Date.now();
  // Третий элемент ключа: время (13 знаков — стабильная длина, значит
  // лексикографический порядок = хронологический) + случайный хвост, чтобы пачки в
  // одну миллисекунду не затирали друг друга.
  const key = ["log", tag, `${at}:${crypto.randomUUID().slice(0, 8)}`];
  await kv.set(key, { at, name: sanitize(req.name), peer: id.slice(0, 8), lines }, {
    expireIn: LOG_TTL_MS,
  });
  await trimLogs(tag);

  return json({ ok: true, stored: lines.length });
}

// trimLogs держит объём логов в рамках (по возрасту — сам expireIn; по объёму —
// здесь): один болтливый клиент не должен раздуть хранилище сети.
async function trimLogs(tag: string): Promise<void> {
  const batches = [];
  let total = 0;
  for await (const e of kv.list({ prefix: ["log", tag] })) {
    // deno-lint-ignore no-explicit-any
    const b = e.value as any;
    batches.push({ key: e.key, n: b.lines?.length ?? 0 });
    total += b.lines?.length ?? 0;
  }
  // Список идёт в порядке ключей = по времени; режем самые старые с головы.
  let i = 0;
  while (total > LOG_MAX_KEEP && i < batches.length) {
    total -= batches[i].n;
    await kv.delete(batches[i].key);
    i++;
  }
}

async function dumpLogs(tag: string): Promise<Response> {
  const out = [];
  for await (const e of kv.list({ prefix: ["log", tag] })) {
    // deno-lint-ignore no-explicit-any
    const b = e.value as any;
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
