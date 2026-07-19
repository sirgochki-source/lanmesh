// Компактный список участников: узкие карточки-строки без аватарок,
// точка-статус слева, IP и пинг справа (см. docs/superpowers/design-mockups/03-compact-final.html).
import { dispName, esc } from '../lib/sanitize.js';
import { fmtRtt, rttClass, fmtUptime, plural, fmtBytes } from '../lib/format.js';
import { quality } from '../lib/quality.js';
import { sparklineSVG } from '../lib/sparkline.js';   // реально используется с Task 12 (накопление истории)
import { renderSettings } from './settings.js';
import { renderTraffic, netTrafficTotals } from './traffic.js';

const pngHtml = (peer) => {
  if (peer.status === 'connecting') return '<span class="png conn">подключение…</span>';
  const r = fmtRtt(peer.rttMs ?? -1);
  return r ? `<span class="png ${rttClass(peer.rttMs)}">${r}</span>` : '';
};
const sdotCls = (s) => s === 'direct' ? 'direct' : s === 'relay' ? 'relay' : 'conn';

// Числовая сортировка по IP: лексикографическое сравнение строк неверно упорядочивает
// октеты ("25.44.9.1" оказывался бы после "25.44.31.7").
function cmpVip(a, b) {
  const pa = a.vip.split('.').map(Number), pb = b.vip.split('.').map(Number);
  for (let i = 0; i < 4; i++) { if ((pa[i] || 0) !== (pb[i] || 0)) return (pa[i] || 0) - (pb[i] || 0); }
  return 0;
}

export function peerRowCompact(peer) {
  return `<div class="row"><span class="sdot ${sdotCls(peer.status)}"></span>`
    + `<span class="nm">${dispName(peer.name || 'узел')}</span><span class="grow"></span>`
    + `<span class="ip mono copyable" data-copy="${esc(peer.vip)}" title="скопировать IP">${esc(peer.vip)}</span>${pngHtml(peer)}</div>`;
}

export function netCardCompact(net) {
  const peers = (net.peers || []).slice().sort(cmpVip);
  const body = peers.length
    ? peers.map(peerRowCompact).join('')
    : `<div class="empty">Пока никого. Позови друга в сеть <b>${dispName(net.name)}</b> с тем же паролем или пришли ссылку кнопкой «Пригласить».</div>`;
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
      + `<span><span class="k">внешний</span><span class="v">${state.selfEndpoint ? 'определён' : 'не определён'}</span></span>`
      + `<span><span class="k">аптайм</span><span class="v up">${fmtUptime(state.uptimeSec || 0)}</span></span></div>` : '';
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

// Диспетчер видов. history в compact не нужна — только detailed (спарклайн).
// rates — снимок текущих скоростей (Phase 3, см. computeRates()/app.js), тоже только detailed.
export function renderView(state, mode, view, histories = {}, activeNetTag, rates = {}) {
  if (mode === 'compact') return renderCompact(state);
  return window.renderDetailed ? window.renderDetailed(state, view, histories, activeNetTag, rates) : renderCompact(state);
}

/* ==================== Task 11: подробный режим — Sidebar Dashboard ====================
   Значения — из docs/superpowers/design-mockups/02-two-modes.html (секция DETAILED). */

const initial = (name) => dispName((name || 'у').trim().charAt(0).toUpperCase());
// Цвет аватара — детерминированно по хвосту vip, чтобы у одного узла он не «прыгал» между рендерами.
const avClass = (vip) => 'av' + (['m', 'k', 's', 'd'][(parseInt(vip.replace(/\D/g, '').slice(-2) || '0', 10)) % 4]);
const barsHtml = (signals) => {
  const on = (signals || []).filter(Boolean).length, total = Math.max(1, (signals || []).length);
  return '<span class="bars">' + Array.from({ length: 4 }, (_, i) =>
    `<i class="${i < Math.round(on / total * 4) ? 'on' : ''}"></i>`).join('') + '</span>';
};

export function peerRowDetailed(peer, history = []) {
  const q = quality(peer.status, peer.rttMs ?? -1, history);
  const badge = peer.status === 'connecting' ? '<span class="badge conn">подключение</span>'
    : `<span class="badge ${esc(peer.status)}">${esc(peer.status)}</span>`;
  const spark = history.length >= 2
    ? sparklineSVG(history, { w: 120, h: 24, stroke: `var(--q-${q.level})` }) : '<span class="spark-empty"></span>';
  return `<div class="drow" data-q="${q.level}"><span class="av ${avClass(peer.vip)}">${initial(peer.name)}</span>`
    + `<span class="who"><span class="nm">${dispName(peer.name || 'узел')}</span>`
    + `<span class="ip mono copyable" data-copy="${esc(peer.vip)}" title="скопировать IP">${esc(peer.vip)}</span></span>`
    + `${spark}<span class="grow"></span>`
    + `${barsHtml(peer.signals)}${badge}${pngHtml(peer)}</div>`;
}

export function qualityTile(net) {
  const peers = (net.peers || []).filter(p => p.status !== 'connecting');
  const direct = peers.filter(p => p.status === 'direct').length;
  const relay = peers.filter(p => p.status === 'relay').length;
  const worst = peers.some(p => quality(p.status, p.rttMs, []).level === 'bad') ? 'bad'
    : relay ? 'ok' : 'good';
  const label = { good: 'хорошее', ok: 'среднее', bad: 'плохое' }[worst];
  return `<div class="tile"><div class="k">Качество связи</div>`
    + `<div class="big q-${worst}">${label}</div><div class="sub">${direct} direct · ${relay} relay</div></div>`;
}

const soon = (text) => `<div class="dmain"><div class="soon">${text}</div></div>`;

export function renderDetailed(state, view, histories = {}, activeNetTag, rates = {}) {
  const nets = state.networks || [];
  const net = nets.find(n => n.tag === activeNetTag) || nets[0];
  if (view === 'settings') return renderSettings(state);
  // 'traffic' ожидает реальную сеть, а не строит «сеть по умолчанию» на пустом месте;
  // при отсутствии сетей — та же подсказка, что и у списка.
  if (!net) return soon('Нет активных сетей. Добавь сеть в компактном режиме.');
  if (view === 'traffic') return renderTraffic(net, rates);
  const peers = (net.peers || []).slice().sort(cmpVip);
  const rows = peers.map(p => peerRowDetailed(p, histories[p.vip] || [])).join('') || '<div class="empty">Пока никого.</div>';
  const traf = netTrafficTotals(net, rates);
  return `<div class="dmain"><div class="dhd"><div><div class="title">${dispName(net.name)}</div>`
    + `<div class="sub">${peers.length} ${plural(peers.length, 'участник', 'участника', 'участников')}</div></div><span class="grow"></span>`
    + `<button class="btn-ghost" data-invite="${esc(net.tag)}">⧉ Пригласить</button></div>`
    + `<div class="tiles">${qualityTile(net)}`
    + `<div class="tile"><div class="k">Трафик</div><div class="big">${fmtBytes(traf.rx + traf.tx)}</div>`
    + `<div class="sub">${fmtBytes(traf.rate)}/с</div></div>`
    + `<div class="tile"><div class="k">Участников</div><div class="big">${peers.length}</div></div></div>`
    + `<div class="drows">${rows}</div></div>`;
}
// typeof-охрана: list.js импортируется напрямую в node --test (без DOM),
// а window.renderDetailed нужен только браузеру — присваивание на этапе
// импорта модуля не должно падать вне браузерного контекста.
if (typeof window !== 'undefined') window.renderDetailed = renderDetailed;
