// Компактный список участников: узкие карточки-строки без аватарок,
// точка-статус слева, IP и пинг справа (см. docs/superpowers/design-mockups/03-compact-final.html).
import { dispName, esc } from '../lib/sanitize.js';
import { fmtRtt, rttClass, fmtUptime, plural, fmtBytes } from '../lib/format.js';
import { quality } from '../lib/quality.js';
import { sparklineSVG } from '../lib/sparkline.js';   // реально используется с Task 12 (накопление истории)
import { renderSettings, renderSettingsCompact } from './settings.js';
import { renderTraffic, netTrafficTotals } from './traffic.js';
import { displayNets } from './shell.js';

const pngHtml = (peer) => {
  if (peer.status === 'connecting') return '<span class="png conn">подключение…</span>';
  const r = fmtRtt(peer.rttMs ?? -1);
  return r ? `<span class="png ${rttClass(peer.rttMs)}">${r}</span>` : '';
};
const sdotCls = (s) => s === 'direct' ? 'direct' : s === 'relay' ? 'relay' : 'conn';

// Индикатор сигнальных серверов: точка на каждый сервер, зелёная = отвечает, красная =
// молчит; хост и статус — в подсказке. signals = net.signals = [{host, up}]. labeled=true
// (подробный режим) добавляет подпись хоста рядом с точкой; в компактном — только точки.
export function signalDots(signals, labeled = false) {
  const list = signals || [];
  if (!list.length) return '';
  const items = list.map(s => {
    const cls = s.up ? 'up' : 'down';
    const tip = `${esc(s.host)}: ${s.up ? 'на связи' : 'нет ответа'}`;
    return labeled
      ? `<span class="sig-item ${cls}" title="${tip}"><i></i>${esc(s.host)}</span>`
      : `<i class="sig ${cls}" title="${tip}"></i>`;
  }).join('');
  return `<span class="sigdots${labeled ? ' labeled' : ''}" title="сигнальные серверы">${items}</span>`;
}

// Числовая сортировка по IP: лексикографическое сравнение строк неверно упорядочивает
// октеты ("25.44.9.1" оказывался бы после "25.44.31.7").
function cmpVip(a, b) {
  const pa = a.vip.split('.').map(Number), pb = b.vip.split('.').map(Number);
  for (let i = 0; i < 4; i++) { if ((pa[i] || 0) !== (pb[i] || 0)) return (pa[i] || 0) - (pb[i] || 0); }
  return 0;
}

export function peerRowCompact(peer, netSignals = []) {
  // Имя + IP в столбик (как в подробном), точки сигналок и пинг — справа.
  return `<div class="row"><span class="sdot ${sdotCls(peer.status)}"></span>`
    + `<span class="who"><span class="nm">${dispName(peer.name || 'узел')}</span>`
    + `<span class="ip mono copyable" data-copy="${esc(peer.vip)}" title="скопировать IP">${esc(peer.vip)}</span></span>`
    + `<span class="grow"></span>`
    + `${peerSignalDots(peer.signals, netSignals)}${pngHtml(peer)}</div>`;
}

export function netCardCompact(net) {
  // Неактивная (сохранённая, но отключённая) сеть: серая карточка «отключено», без
  // участников; «Выйти» (забыть сеть) остаётся, «Пригласить» — нет (нужен поднятый узел).
  if (net.inactive) {
    return `<div class="netcard inactive"><div class="netcard-hd">`
      + `<span class="net-name">${dispName(net.name)}</span> <span class="off-badge">отключено</span>`
      + `<span class="grow"></span>`
      + `<button class="btn-ghost" data-leave="${esc(net.tag)}">Выйти</button></div></div>`;
  }
  const peers = (net.peers || []).slice().sort(cmpVip);
  const body = peers.length
    ? peers.map(p => peerRowCompact(p, net.signals)).join('')
    : `<div class="empty">Пока никого. Позови друга в сеть <b>${dispName(net.name)}</b> с тем же паролем или пришли ссылку кнопкой «Пригласить».</div>`;
  // Индикатор сигналок по каждому серверу; запасной единичный эмодзи — если бэкенд
  // не прислал список серверов (старое состояние без net.signals).
  const sig = (net.signals && net.signals.length) ? signalDots(net.signals) : (net.signalError ? '🟡' : '🟢');
  return `<div class="netcard"><div class="netcard-hd"><span class="net-name">${dispName(net.name)}</span> ${sig}`
    + `<span class="cnt">· ${peers.length}</span><span class="grow"></span>`
    + `<button class="btn-ghost" data-invite="${esc(net.tag)}">⧉ Пригласить</button>`
    + `<button class="btn-ghost" data-leave="${esc(net.tag)}">Выйти</button></div>`
    + `<div class="rows">${body}</div></div>`;
}

export function renderCompact(state, view = 'list') {
  if (view === 'settings') return renderSettingsCompact(state);
  const nets = displayNets(state); // сохранённые сети не пропадают после отключения — серые карточки
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
  if (mode === 'compact') return renderCompact(state, view);
  return window.renderDetailed ? window.renderDetailed(state, view, histories, activeNetTag, rates) : renderCompact(state);
}

/* ==================== Task 11: подробный режим — Sidebar Dashboard ====================
   Значения — из docs/superpowers/design-mockups/02-two-modes.html (секция DETAILED). */

const initial = (name) => dispName((name || 'у').trim().charAt(0).toUpperCase());
// Цвет аватара — детерминированно по хвосту vip, чтобы у одного узла он не «прыгал» между рендерами.
const avClass = (vip) => 'av' + (['m', 'k', 's', 'd'][(parseInt(vip.replace(/\D/g, '').slice(-2) || '0', 10)) % 4]);
// peerSignalDots — через какие сигналки виден ПИР: точка на каждый сервер, зелёная (up)
// если пир виден через него (peer.signals[i]), серая (off) если нет. Хост берём из
// net.signals[i] (тот же порядок signalURLs). «Не виден» — нейтральный серый, не красный:
// это не ошибка сервера (красный оставлен индикатору сети), а просто «пира там нет».
export function peerSignalDots(peerSignals, netSignals = []) {
  const ps = peerSignals || [];
  if (!ps.length) return '';
  const dots = ps.map((on, i) => {
    const host = (netSignals[i] && netSignals[i].host) || ('сигналка ' + (i + 1));
    return `<i class="sig ${on ? 'up' : 'off'}" title="${esc(host)}: ${on ? 'виден' : 'не виден'}"></i>`;
  }).join('');
  return `<span class="sigdots" title="через какие сигналки виден пир">${dots}</span>`;
}

export function peerRowDetailed(peer, history = [], netSignals = []) {
  const q = quality(peer.status, peer.rttMs ?? -1, history);
  const badge = peer.status === 'connecting' ? '<span class="badge conn">подключение</span>'
    : `<span class="badge ${esc(peer.status)}">${esc(peer.status)}</span>`;
  const spark = history.length >= 2
    ? sparklineSVG(history, { w: 120, h: 24, stroke: `var(--q-${q.level})` }) : '<span class="spark-empty"></span>';
  return `<div class="drow" data-q="${q.level}"><span class="av ${avClass(peer.vip)}">${initial(peer.name)}</span>`
    + `<span class="who"><span class="nm">${dispName(peer.name || 'узел')}</span>`
    + `<span class="ip mono copyable" data-copy="${esc(peer.vip)}" title="скопировать IP">${esc(peer.vip)}</span></span>`
    + `${spark}<span class="grow"></span>`
    + `${peerSignalDots(peer.signals, netSignals)}${badge}${pngHtml(peer)}</div>`;
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
  const nets = displayNets(state); // включает сохранённые-неактивные (после отключения)
  const net = nets.find(n => n.tag === activeNetTag) || nets[0];
  if (view === 'settings') return renderSettings(state);
  if (!net) return soon('Нет сохранённых сетей. Добавь сеть в компактном режиме.');
  // Неактивная (отключённая) сеть: серый заголовок «отключено» + подсказка подключиться,
  // без плиток и участников. Раньше traffic ожидает реальную сеть — проверяем ДО него.
  if (net.inactive) {
    return `<div class="dmain"><div class="dhd"><div><div class="title">${dispName(net.name)}</div>`
      + `<div class="sub"><span class="off-badge">отключено</span></div></div></div>`
      + `<div class="soon">Сеть сохранена, но узел отключён. Нажми «Подключиться» вверху, чтобы поднять её.</div></div>`;
  }
  if (view === 'traffic') return renderTraffic(net, rates);
  const peers = (net.peers || []).slice().sort(cmpVip);
  const rows = peers.map(p => peerRowDetailed(p, histories[p.vip] || [], net.signals)).join('') || '<div class="empty">Пока никого.</div>';
  const traf = netTrafficTotals(net, rates);
  return `<div class="dmain"><div class="dhd"><div><div class="title">${dispName(net.name)}</div>`
    + `<div class="sub">${peers.length} ${plural(peers.length, 'участник', 'участника', 'участников')}</div>`
    + `${signalDots(net.signals, true)}</div><span class="grow"></span>`
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
