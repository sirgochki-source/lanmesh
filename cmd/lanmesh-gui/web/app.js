import { renderHeader, renderRail, pickMode } from './views/shell.js';
import { RttHistory } from './lib/rtt-history.js';
import { diffPeers } from './lib/peerdiff.js';
import { collectPeers } from './lib/collect.js';
import { dispName } from './lib/sanitize.js';

const POLL_MS = 1300;
let mode = localStorage.getItem('lm-mode') || pickMode(innerWidth);
let manual = localStorage.getItem('lm-mode') != null;
let activeView = 'list';
let activeNetTag = null;
let lastState = { running: false, networks: [] };

// {vip: number[]} — история пинга для спарклайнов detailed-режима, наполняется в ingest().
const rttHistory = new RttHistory(40);
let prevPeers = [];                                 // предыдущий плоский снимок пиров, для diffPeers
const histSnapshot = () => {
  const o = {};
  for (const vip of rttHistory.map.keys()) o[vip] = rttHistory.get(vip);
  return o;
};

// Накопление истории пинга + тосты вход/выход. Вызывается ровно раз на РЕАЛЬНЫЙ опрос
// (из poll(), не из render()) — иначе UI-only перерисовки (переключение режима/вида/сети,
// ResizeObserver), которые зовут render(lastState) с уже обработанным состоянием, дублировали
// бы точки в RttHistory тем же самым значением rttMs при каждом клике между опросами.
function ingest(state) {
  const peers = collectPeers(state);
  for (const p of peers) rttHistory.push(p.vip, p.rttMs ?? -1);
  rttHistory.prune(peers.map(p => p.vip));
  const { joined, left } = diffPeers(prevPeers, peers);
  if (prevPeers.length) {                          // не тостим первый снимок (стартовый состав)
    for (const p of joined) toast(`${dispName(p.name)} в сети`, 'in');
    for (const p of left) toast(`${dispName(p.name)} вышел`, 'out');
  }
  prevPeers = peers;
}
function toast(text, kind) {
  const el = document.createElement('div');
  el.className = `toast ${kind}`; el.innerHTML = text;   // text уже прошёл dispName() — как и всюду в проекте, экранирование через esc(), не через textContent
  const box = document.getElementById('toasts'); box.appendChild(el);
  setTimeout(() => el.classList.add('show'), 10);
  setTimeout(() => { el.classList.remove('show'); setTimeout(() => el.remove(), 300); }, 3500);
}

function setMode(m) { mode = m; document.getElementById('root').dataset.mode = m; }
function render(state) {
  lastState = state;
  document.getElementById('header').innerHTML = renderHeader(state, mode);
  document.getElementById('rail').innerHTML = mode === 'detailed' ? renderRail(state, activeView, activeNetTag) : '';
  if (window.renderView) document.getElementById('view').innerHTML = window.renderView(state, mode, activeView, histSnapshot(), activeNetTag);
}
async function poll() {
  try { const r = await fetch('/api/state'); if (!r.ok) return; const state = await r.json(); ingest(state); render(state); }
  catch (e) { /* переживём сбой */ }
}
// ⤢/⤡ и навигация
document.addEventListener('click', (e) => {
  const act = e.target.closest('[data-act]')?.dataset.act;
  if (act === 'expand') { manual = true; localStorage.setItem('lm-mode', 'detailed'); setMode('detailed'); render(lastState); return; }
  if (act === 'collapse') { manual = true; localStorage.setItem('lm-mode', 'compact'); setMode('compact'); render(lastState); return; }
  // Клик по сети в рейле: выбираем её активной и переключаемся на список
  // (элемент несёт и data-net, и data-view="list" — обрабатываем здесь и выходим,
  // чтобы не сработала ещё раз ветка data-view ниже).
  const netEl = e.target.closest('[data-net]');
  if (netEl) { activeNetTag = netEl.dataset.net; activeView = 'list'; render(lastState); return; }
  const v = e.target.closest('[data-view]')?.dataset.view;
  if (v) { activeView = v; render(lastState); }
});
// Копирование IP при клике на адрес в компактном списке
document.addEventListener('click', (e) => {
  const el = e.target.closest('[data-copy]');
  if (!el) return;
  const ip = el.dataset.copy;
  if (navigator.clipboard) navigator.clipboard.writeText(ip).catch(() => {});
  const chip = document.createElement('div');
  chip.className = 'copytoast';
  chip.textContent = 'IP ' + ip + ' скопирован';
  document.body.appendChild(chip);
  setTimeout(() => chip.remove(), 1500);
});
// Отзывчивость: если пользователь не выбирал режим руками — следуем ширине окна.
new ResizeObserver(() => { if (!manual) setMode(pickMode(innerWidth)); render(lastState); }).observe(document.documentElement);

setMode(mode);
poll();
setInterval(poll, POLL_MS);

import { renderView } from './views/list.js';
window.renderView = renderView;
