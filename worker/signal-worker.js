// Cloudflare Worker — сигнальный сервер lanmesh (сведение пиров mesh-VPN).
//
// Что делает: принимает POST /register от клиента, помнит, кто сейчас в сети, и
// возвращает список остальных участников. Обмена ключами и трафика тут нет:
// сервер видит только несекретный тег сети (хэш от имени+пароля) и endpoint'ы —
// расшифровать трафик он не может.
//
// ПОЧЕМУ DURABLE OBJECT, А НЕ KV (переписано 2026-07-16):
// на KV это упиралось в лимит бесплатного тарифа — 1000 записей и 1000 list'ов
// в сутки. Каждый пир раз в 20с делал 1 put + 1 list, то есть ~4300 записей в
// сутки НА УЗЕЛ: вечер игры вдвоём — и put начинал кидать исключение, воркер
// падал с 1101, а клиенты видели 500 и тихо выпадали из сети.
//
// ПОЧЕМУ СОСТОЯНИЕ ПИШЕТСЯ В STORAGE, А НЕ ЖИВЁТ ТОЛЬКО В ПАМЯТИ (2026-07-17):
// сначала была чистая память — и объект стало выгружать по простою прямо между
// регистрациями (они раз в 20с). Наблюдалось в бою: список участников
// периодически пустел ЦЕЛИКОМ. Клиент от этого защищён (см. peerForget), но два
// вреда оставались: терялись логи диагностики и новый участник видел пустую сеть.
// Storage у Durable Object — это НЕ KV, лимиты там свои и другого порядка
// (~100k записанных строк в сутки против злополучной 1000), наши ~4300 на узел
// влезают с запасом.
//
// Память остаётся быстрым путём, storage — только чтобы пережить выгрузку:
// при создании объекта восстанавливаемся из него в blockConcurrencyWhile.
//
// Один объект на сеть: idFromName(тег) — сети изолированы и не мешают друг другу.
//
// Клиент: lanmesh -signal https://<этот-воркер>.workers.dev

const PEER_TTL_MS = 60_000;  // ~3 пропущенных регистрации -> пир выпадает из сети
const LOG_TTL_MS = 3600_000; // час, как и раньше
const LOG_MAX_LINES = 200;   // строк в одной пачке
const LOG_MAX_LINE = 500;    // символов в строке
const LOG_MAX_KEEP = 2000;   // сколько строк держим на сеть, чтобы память не росла

export default {
  async fetch(request, env) {
    const url = new URL(request.url);

    if (url.pathname === "/" || url.pathname === "/health") {
      return new Response("lanmesh signal ok\n", { status: 200 });
    }

    // Тег определяет, в какой объект идём. Для POST он в теле, для /logs — в query.
    let tag = "";
    let body = null;

    if (request.method === "POST" && (url.pathname === "/register" || url.pathname === "/log")) {
      try {
        body = await request.json();
      } catch {
        return json({ error: "bad json" }, 400);
      }
      tag = String(body.net || "");
    } else if (request.method === "GET" && url.pathname === "/logs") {
      tag = String(url.searchParams.get("net") || "");
    } else {
      return new Response("not found", { status: 404 });
    }

    if (!/^[0-9a-f]{64}$/.test(tag)) {
      return url.pathname === "/logs"
        ? text("нужен ?net=<64 hex>\n", 400)
        : json({ error: "bad net" }, 400);
    }

    const id = env.MESH.idFromName(tag);
    const stub = env.MESH.get(id);
    return stub.fetch(new Request(url.toString(), {
      method: request.method,
      headers: { "Content-Type": "application/json" },
      body: body === null ? undefined : JSON.stringify(body),
    }));
  }
};

// MeshRegistry — состояние ОДНОЙ сети: кто в ней сейчас и их логи.
//
// Память — рабочая копия, storage — чтобы пережить выгрузку объекта.
export class MeshRegistry {
  constructor(state, env) {
    this.storage = state.storage;
    this.peers = new Map(); // peerID -> {info, seen}
    this.logs = [];         // [{at, name, peer, lines}]

    // Восстанавливаемся из storage ДО того, как объект начнёт отвечать:
    // blockConcurrencyWhile придержит входящие запросы, иначе первый после
    // выгрузки увидел бы пустую сеть.
    state.blockConcurrencyWhile(async () => {
      const peers = await this.storage.list({ prefix: "peer:" });
      for (const [k, rec] of peers) {
        this.peers.set(k.slice("peer:".length), rec);
      }
      const logs = await this.storage.list({ prefix: "log:" });
      for (const [, b] of logs) this.logs.push(b);
      this.logs.sort((a, b) => a.at - b.at);
    });
  }

  async fetch(request) {
    const url = new URL(request.url);
    try {
      if (request.method === "POST" && url.pathname === "/register") {
        return await this.register(await request.json());
      }
      if (request.method === "POST" && url.pathname === "/log") {
        return await this.addLog(await request.json());
      }
      if (request.method === "GET" && url.pathname === "/logs") {
        return await this.dumpLogs();
      }
    } catch (e) {
      return json({ error: String(e) }, 500);
    }
    return new Response("not found", { status: 404 });
  }

  // --- пиры -----------------------------------------------------------------

  async register(req) {
    const id = String(req.id || "");
    if (!/^[0-9a-f]{32}$/.test(id)) {
      return json({ error: "bad id" }, 400);
    }

    const now = Date.now();
    const self = {
      id,
      name: sanitize(req.name),
      vip: sanitize(req.vip),
      eps: sanitizeEndpoints(req.eps),
    };
    const rec = { info: self, seen: now };
    this.peers.set(id, rec);
    await this.storage.put("peer:" + id, rec);

    // Протухших выкидываем прямо тут: отдельная уборка не нужна, таблица
    // маленькая, а трогаем мы её только на регистрации.
    const peers = [];
    const dead = [];
    for (const [pid, r] of this.peers) {
      if (now - r.seen > PEER_TTL_MS) {
        this.peers.delete(pid);
        dead.push("peer:" + pid);
        continue;
      }
      if (pid !== id) peers.push(r.info);
    }
    if (dead.length) await this.storage.delete(dead);

    return json({ self, peers });
  }

  // --- диагностика ----------------------------------------------------------

  async addLog(req) {
    const id = String(req.id || "");
    if (!/^[0-9a-f]{32}$/.test(id)) {
      return json({ error: "bad id" }, 400);
    }
    if (!Array.isArray(req.lines) || req.lines.length === 0) {
      return json({ error: "нет строк" }, 400);
    }

    const lines = req.lines
      .slice(0, LOG_MAX_LINES)
      .map(l => String(l || "").slice(0, LOG_MAX_LINE));

    const at = Date.now();
    // Ключ включает время (13 знаков — длина стабильна, значит лексикографический
    // порядок = хронологический) и случайный хвост, чтобы пачки в одну
    // миллисекунду не затирали друг друга.
    const batch = {
      key: `log:${at}:${crypto.randomUUID().slice(0, 8)}`,
      at,
      name: sanitize(req.name),
      peer: id.slice(0, 8),
      lines,
    };
    this.logs.push(batch);
    await this.storage.put(batch.key, batch);
    await this.trimLogs();

    return json({ ok: true, stored: lines.length });
  }

  // trimLogs держит логи в рамках: сначала по возрасту, потом по объёму —
  // иначе один болтливый клиент раздует и память, и storage.
  async trimLogs() {
    const cutoff = Date.now() - LOG_TTL_MS;
    const drop = [];

    const fresh = [];
    for (const b of this.logs) {
      if (b.at >= cutoff) fresh.push(b);
      else drop.push(b.key);
    }
    this.logs = fresh;

    let total = 0;
    for (const b of this.logs) total += b.lines.length;
    while (total > LOG_MAX_KEEP && this.logs.length > 0) {
      total -= this.logs[0].lines.length;
      drop.push(this.logs[0].key);
      this.logs.shift();
    }

    if (drop.length) await this.storage.delete(drop.filter(Boolean));
  }

  async dumpLogs() {
    await this.trimLogs();
    if (this.logs.length === 0) {
      return text("логов нет (клиенты не слали или истёк час)\n");
    }
    let out = "";
    for (const b of this.logs) {
      const who = `${b.name || "?"} (${b.peer})`;
      for (const l of b.lines) out += `${who}\t${l}\n`;
    }
    return text(out);
  }
}

// --- утилиты ----------------------------------------------------------------

function sanitize(s) {
  return String(s || "").slice(0, 64).replace(/[^\x20-\x7e]/g, "");
}

// Endpoint'ы — массив "ip:port"; чистим формат и ограничиваем количество, чтобы
// пир не мог раздуть запись.
function sanitizeEndpoints(eps) {
  if (!Array.isArray(eps)) return [];
  const out = [];
  for (const e of eps) {
    const s = String(e || "");
    if (/^\d{1,3}(\.\d{1,3}){3}:\d{1,5}$/.test(s)) out.push(s);
    if (out.length >= 8) break;
  }
  return out;
}

function json(obj, status = 200) {
  return new Response(JSON.stringify(obj), {
    status,
    headers: { "Content-Type": "application/json; charset=utf-8", "Cache-Control": "no-store" },
  });
}

function text(s, status = 200) {
  return new Response(s, {
    status,
    headers: { "Content-Type": "text/plain; charset=utf-8", "Cache-Control": "no-store" },
  });
}
