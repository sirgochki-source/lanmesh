import { renderHeader, renderRail, pickMode } from './views/shell.js';
import { RttHistory } from './lib/rtt-history.js';
import { diffPeers } from './lib/peerdiff.js';
import { collectPeers } from './lib/collect.js';
import { dispName } from './lib/sanitize.js';
import { parseInvite } from './lib/invite.js';
import { computeRates } from './lib/traffic.js';
import { srvRow } from './views/settings.js';

const POLL_MS = 1300;
let mode = localStorage.getItem('lm-mode') || pickMode(innerWidth);
let manual = localStorage.getItem('lm-mode') != null;
let activeView = 'list';
let activeNetTag = null;
let lastState = { running: false, networks: [] };

// {vip: number[]} — история пинга для спарклайнов detailed-режима, наполняется в ingest().
const rttHistory = new RttHistory(40);
let prevPeers = [];                                 // предыдущий плоский снимок пиров, для diffPeers
let prevRunning = false;                            // было ли running в прошлом опросе — чтобы не тостить на вкл/выкл
const histSnapshot = () => {
  const o = {};
  for (const vip of rttHistory.map.keys()) o[vip] = rttHistory.get(vip);
  return o;
};

// Трафик (Phase 3): prevBytes/prevBytesAt — предыдущий кумулятивный снимок bytesRx/bytesTx
// и момент его получения, для computeRates() в следующем ingest(). currentRates — снимок
// байт/сек с последнего расчёта, отдаётся наружу через ratesSnapshot() (как histSnapshot()).
let prevBytes = {};
let prevBytesAt = 0;
let currentRates = {};
const ratesSnapshot = () => currentRates;
// Плоский снимок кумулятивных байт всех пиров всех сетей — аналог collectPeers(), но не через
// него: collectPeers() уже законтрактован (см. live.test.mjs) на {vip,name,rttMs,status}, тянуть
// туда bytesRx/bytesTx означало бы расширять чужой публичный контракт ради локальной надобности.
function flattenBytes(state) {
  const out = {};
  for (const n of state.networks || []) for (const p of n.peers || [])
    out[p.vip] = { rx: p.bytesRx || 0, tx: p.bytesTx || 0 };
  return out;
}

// Накопление истории пинга + тосты вход/выход. Вызывается ровно раз на РЕАЛЬНЫЙ опрос
// (из poll(), не из render()) — иначе UI-only перерисовки (переключение режима/вида/сети,
// ResizeObserver), которые зовут render(lastState) с уже обработанным состоянием, дублировали
// бы точки в RttHistory тем же самым значением rttMs при каждом клике между опросами.
// Та же причина не даёт считать rates в render(): дельта считается между РЕАЛЬНЫМИ опросами.
function ingest(state) {
  const peers = collectPeers(state);
  for (const p of peers) rttHistory.push(p.vip, p.rttMs ?? -1);
  rttHistory.prune(peers.map(p => p.vip));
  const { joined, left } = diffPeers(prevPeers, peers);
  // Тостим приход/уход только ВНУТРИ активной сессии (был онлайн и остался): иначе
  // намеренное «Отключиться» (running:true→false) сыпало бы «X вышел» по всем пирам, а
  // «Подключиться» — «X в сети». prevPeers.length убирает ещё и стартовый снимок.
  if (prevPeers.length && prevRunning && state.running) {
    for (const p of joined) toast(`${dispName(p.name)} в сети`, 'in');
    for (const p of left) toast(`${dispName(p.name)} вышел`, 'out');
  }
  prevPeers = peers;
  prevRunning = state.running;

  const curBytes = flattenBytes(state);
  const now = Date.now();
  const dtSec = prevBytesAt ? (now - prevBytesAt) / 1000 : 0;   // 0 на первом опросе — нет prev, computeRates() отдаст нули
  currentRates = computeRates(prevBytes, curBytes, dtSec);
  prevBytes = curBytes;
  prevBytesAt = now;
}
function toast(text, kind) {
  const el = document.createElement('div');
  el.className = `toast ${kind}`; el.innerHTML = text;   // text уже прошёл dispName() — как и всюду в проекте, экранирование через esc(), не через textContent
  const box = document.getElementById('toasts'); box.appendChild(el);
  setTimeout(() => el.classList.add('show'), 10);
  setTimeout(() => { el.classList.remove('show'); setTimeout(() => el.remove(), 300); }, 3500);
}

function setMode(m) { mode = m; document.getElementById('root').dataset.mode = m; }
// fromPoll=true — вызов из фонового poll() (каждые POLL_MS), а не из UI-хендлера.
// В этом случае, пока фокус стоит внутри #view (пользователь печатает в форме добавления
// сети/настроек), #view НЕ перерисовываем — иначе опрос стирал бы вводимый текст и фокус.
function render(state, fromPoll = false) {
  lastState = state;
  document.getElementById('header').innerHTML = renderHeader(state, mode);
  document.getElementById('rail').innerHTML = mode === 'detailed' ? renderRail(state, activeView, activeNetTag) : '';
  const viewEl = document.getElementById('view');
  if (fromPoll && (activeView === 'settings' || viewEl.contains(document.activeElement))) {
    // пропускаем: настройки — редактируемая форма (список серверов), опрос не должен
    // стирать добавленные/удалённые строки и введённый текст; либо пользователь печатает
  } else if (window.renderView) {
    viewEl.innerHTML = window.renderView(state, mode, activeView, histSnapshot(), activeNetTag, ratesSnapshot());
  }
}
async function poll() {
  try { const r = await fetch('/api/state'); if (!r.ok) return; const state = await r.json(); ingest(state); render(state, true); }
  catch (e) { /* переживём сбой */ }
}
// POST-хелпер для действий (Task 13): добавить/выйти/настройки/диагностика — все шлют JSON.
const postJSON = (path, body) => fetch(path, { method: 'POST', headers: { 'Content-Type': 'application/json' }, body: JSON.stringify(body || {}) });
// poll() после успешного действия из #view (add/cfg-save/cfg-reset/leave): клик по кнопке-триггеру
// оставляет фокус на ней (стандартное поведение <button>), а она внутри #view — поэтому Fix 1
// принял бы её за «пользователь ещё печатает» и заморозил бы перерисовку с результатом действия.
// Снимаем фокус явно, чтобы результат отобразился сразу, а не после случайного клика мимо.
function refreshView() { document.activeElement?.blur(); poll(); }
// ⤢/⤡ и навигация
document.addEventListener('click', (e) => {
  const act = e.target.closest('[data-act]')?.dataset.act;
  // window.lmResize существует только в нативном окне (webview2.Bind в Go) — в
  // браузере/mock его нет, поэтому гвардим. Меняет размер окна под режим.
  if (act === 'expand') { manual = true; localStorage.setItem('lm-mode', 'detailed'); setMode('detailed'); window.lmResize && window.lmResize('detailed'); render(lastState); return; }
  if (act === 'collapse') { manual = true; localStorage.setItem('lm-mode', 'compact'); setMode('compact'); window.lmResize && window.lmResize('compact'); render(lastState); return; }
  // ⚙ в компактной шапке -> экран настроек; «← Список» -> назад. activeView живёт
  // как модульное состояние (как в detailed), поэтому переживает опросы.
  if (act === 'show-settings') { activeView = 'settings'; render(lastState); return; }
  if (act === 'show-list') { activeView = 'list'; render(lastState); return; }
  // Кнопки своей рамки окна (нативное приложение): свернуть / закрыть-в-трей.
  const win = e.target.closest('[data-win]')?.dataset.win;
  if (win) { window.lmWindow && window.lmWindow(win); return; }
  // Клик по сети в рейле: выбираем её активной и переключаемся на список
  // (элемент несёт и data-net, и data-view="list" — обрабатываем здесь и выходим,
  // чтобы не сработала ещё раз ветка data-view ниже).
  const netEl = e.target.closest('[data-net]');
  if (netEl) { activeNetTag = netEl.dataset.net; activeView = 'list'; render(lastState); return; }
  const v = e.target.closest('[data-view]')?.dataset.view;
  if (v) { activeView = v; render(lastState); }
});
// Летучая подсказка вне #view (body-уровня) — переживает перерисовку #view опросом
// (в отличие от мутаций текста внутри #view, которые Fix 1 может пропустить/заменить).
function flashChip(text) {
  const chip = document.createElement('div');
  chip.className = 'copytoast';
  chip.textContent = text;
  document.body.appendChild(chip);
  setTimeout(() => chip.remove(), 1500);
}
// Копирование IP при клике на адрес в компактном списке
document.addEventListener('click', (e) => {
  const el = e.target.closest('[data-copy]');
  if (!el) return;
  const ip = el.dataset.copy;
  if (navigator.clipboard) navigator.clipboard.writeText(ip).catch(() => {});
  flashChip('IP ' + ip + ' скопирован');
});
// Автозаполнение имени сети/пароля из вставленной ссылки-приглашения (делегирование на
// document, чтобы работать и после перерисовки формы).
document.addEventListener('input', (e) => {
  if (e.target.id !== 'f-invite') return;
  const inv = parseInvite(e.target.value);
  if (inv.net != null) document.getElementById('f-net').value = inv.net;
  if (inv.pass != null) document.getElementById('f-pass').value = inv.pass;
});
// Действия (Task 13): добавление/выход из сети, приглашение, настройки серверов, диагностика.
// Отдельный (третий) слушатель click — не трогаем существующие ветки expand/collapse/data-view выше.
document.addEventListener('click', async (e) => {
  const t = e.target;
  const act = t.closest('[data-act]')?.dataset.act;
  // Отключиться (в офлайн, не выходя из сетей) / Подключиться (переподнять сохранённые).
  if (act === 'disconnect' || act === 'reconnect') {
    const btn = t.closest('[data-act]');
    if (btn) { btn.disabled = true; const l = btn.querySelector('.lbl'); if (l) l.textContent = act === 'disconnect' ? 'Отключаю…' : 'Подключаю…'; }
    const r = await postJSON('/api/' + act);
    if (!r.ok) { const j = await r.json().catch(() => ({})); flashChip('Ошибка: ' + (j.error || r.status)); }
    poll(); // состояние (running) обновит шапку и кнопку; финал добьёт регулярный опрос
    return;
  }
  if (act === 'add-toggle') {
    const b = t.closest('.addcard').querySelector('.add-body');
    b.hidden = !b.hidden;
    if (!b.hidden) document.getElementById('f-net')?.focus(); // фокус внутри #view держит форму живой при опросе (Fix 1)
    return;
  }
  if (act === 'add') {
    const fNet = document.getElementById('f-net').value.trim(), fPass = document.getElementById('f-pass').value;
    const inv = parseInvite(document.getElementById('f-invite').value);
    // Если поля пустые — используем то, что распознали из вставленной ссылки-приглашения.
    const net = fNet || inv.net || '', pass = fPass || inv.pass || '';
    if (!net || !pass) { flashChip('Нужны имя сети и пароль'); return; }
    const body = { network: net, password: pass };
    if ((inv.net || '').trim() === net) { if (inv.sigs.length) body.signals = inv.sigs; if (inv.relay !== null) body.relay = inv.relay; }
    const r = await postJSON('/api/addnetwork', body); const j = await r.json();
    if (!r.ok) flashChip('Ошибка: ' + (j.error || r.status)); else refreshView();
    return;
  }
  if (act === 'senddiag') {
    const j = await (await postJSON('/api/senddiag')).json();
    flashChip(j.tag ? 'Диагностика отправлена · код ' + j.tag : 'Ошибка диагностики');
    return;
  }
  if (act === 'sig-add') {
    document.getElementById('sig-list')?.insertAdjacentHTML('beforeend', srvRow());
    document.querySelector('#sig-list .srv-row:last-child .s-sig')?.focus();  // фокус в #view держит форму живой при опросе
    return;
  }
  if (act === 'sig-del') { t.closest('.srv-row')?.remove(); return; }
  if (act === 'cfg-save') {
    const signals = [...document.querySelectorAll('#sig-list .s-sig')].map(i => i.value.trim()).filter(Boolean);
    const relay = document.getElementById('s-relay').value.trim();
    const r = await postJSON('/api/settings', { signals, relay });
    if (!r.ok) { const j = await r.json().catch(() => ({})); flashChip('Ошибка: ' + (j.error || r.status)); }
    else flashChip('Настройки серверов сохранены');
    return;
  }
  if (act === 'cfg-reset') {
    await postJSON('/api/settings', { signals: [], relay: '' });
    // Чистим форму на месте: в настройках опрос не перерисовывает #view, поэтому
    // строки не пропадут сами — сбрасываем к одной пустой строке и пустому relay.
    const list = document.getElementById('sig-list'); if (list) list.innerHTML = srvRow();
    const relay = document.getElementById('s-relay'); if (relay) relay.value = '';
    flashChip('Сброшено к стандартным серверам');
    return;
  }
  const inviteTag = t.closest('[data-invite]')?.dataset.invite;
  if (inviteTag != null) {
    const j = await (await fetch('/api/invite?tag=' + encodeURIComponent(inviteTag))).json();
    if (j.link) {
      try { await navigator.clipboard.writeText(j.link); } catch (e) {}
      flashChip('Ссылка-приглашение скопирована');
    } else {
      flashChip(j.note || 'Не удалось получить ссылку');
    }
    return;
  }
  const leaveTag = t.closest('[data-leave]')?.dataset.leave;
  if (leaveTag != null) { if (confirm('Выйти из этой сети?')) { await postJSON('/api/leavenetwork', { tag: leaveTag }); refreshView(); } return; }
});
document.addEventListener('change', async (e) => {
  if (e.target.closest('[data-act]')?.dataset.act === 'sendlogs') await postJSON('/api/sendlogs', { enabled: e.target.checked });
});
// Отзывчивость: если пользователь не выбирал режим руками — следуем ширине окна.
new ResizeObserver(() => { if (!manual) setMode(pickMode(innerWidth)); render(lastState); }).observe(document.documentElement);

// Перетаскивание окна за свою полосу-заголовок (только нативное приложение, где есть
// мост window.lmDrag). mousedown по .hd, кроме кликов по кнопкам/интерактивным
// элементам, запускает нативный drag окна. Так надёжнее WM_NCHITTEST: дочернее окно
// WebView2 накрывает клиент, и hit-test у родителя за полосу не срабатывает.
document.addEventListener('mousedown', (e) => {
  if (!window.lmDrag || e.button !== 0) return;
  if (!e.target.closest('.hd')) return;
  if (e.target.closest('button, a, input, [data-act], [data-win], [data-copy]')) return;
  window.lmDrag();
});

// В нативном окне (webview2.Bind даёт window.lmWindow) — своя рамка: показываем
// кнопки окна (свернуть/закрыть). В браузере/mock их нет.
if (window.lmWindow) document.documentElement.classList.add('native');
setMode(mode);
// Синхронизируем размер нативного окна с начальным режимом: окно создаётся широким
// (под подробный), поэтому при сохранённом компактном режиме его надо сразу сузить —
// иначе компакт открывается в ширину подробного.
if (window.lmResize) window.lmResize(mode);
poll();
setInterval(poll, POLL_MS);

import { renderView } from './views/list.js';
window.renderView = renderView;
