// Оболочка приложения: чистые функции рендера (шапка, рейл, статус-pill,
// выбор режима по ширине). DOM-монтаж и опрос — в app.js.
import { plural } from '../lib/format.js';
import { dispName as escName, esc } from '../lib/sanitize.js';

export function pickMode(width) { return width < 620 ? 'compact' : 'detailed'; }

// displayNets — сети для показа: активные (state.networks) как есть; сохранённые, но
// не активные (после «Отключиться») — заглушкой inactive:true, чтобы сеть не пропадала
// из списка, а становилась серой. Порядок: по сохранённым, затем активные без сохранения.
export function displayNets(state) {
  const active = new Map((state.networks || []).map(n => [n.tag, n]));
  const saved = state.savedNets || [];
  const out = saved.map(s => active.get(s.tag)
    || { name: s.name, tag: s.tag, discovery: s.discovery, inactive: true, peers: [], signals: [] });
  const savedTags = new Set(saved.map(s => s.tag));
  for (const n of state.networks || []) if (!savedTags.has(n.tag)) out.push(n);
  return out;
}

export function statusPill(state) {
  if (!state.running) return { cls: 'off', text: 'не подключено' };
  const nets = state.networks || [];
  const noExt = !state.selfEndpoint;
  const anyBad = nets.some(n => n.signalError);
  if (noExt || anyBad) return { cls: 'warn', text: 'ограничено' };
  return { cls: 'on', text: nets.length + ' ' + plural(nets.length, 'сеть', 'сети', 'сетей') };
}

// connBtn — глобальная кнопка узла: «Отключиться» когда онлайн (data-act=disconnect →
// уйти в офлайн, НЕ выходя из сетей), «Подключиться» когда офлайн, но есть сохранённые
// сети (data-act=reconnect). Если офлайн и сетей нет — кнопки нет (нечего поднимать).
// Иконка питания видна всегда; подпись в компактном режиме скрывается по CSS.
export function connBtn(state) {
  const on = state.running;
  if (!on && !(state.savedNetworks > 0)) return '';
  const act = on ? 'disconnect' : 'reconnect';
  const label = on ? 'Отключиться' : 'Подключиться';
  return `<button class="conn-btn ${on ? 'is-on' : 'is-off'}" data-act="${act}" title="${label}">`
    + `<span class="pw">⏻</span><span class="lbl">${label}</span></button>`;
}

// 9 вариантов оформления: id (= data-theme в CSS), подпись и две краски для превью-точки.
// Отличаются не только акцентом, но и стеклом/размытием/фоном (см. app.css).
export const THEMES = [
  { id: 'mint', name: 'Мята', a: '#37f5c5', b: '#22d3ee' },
  { id: 'aurora', name: 'Аврора', a: '#a78bfa', b: '#7c3aed' },
  { id: 'ocean', name: 'Океан', a: '#38bdf8', b: '#0ea5e9' },
  { id: 'sunset', name: 'Закат', a: '#fb923c', b: '#f43f5e' },
  { id: 'frost', name: 'Иней', a: '#7dd3fc', b: '#38bdf8' },
  { id: 'onyx', name: 'Оникс', a: '#34d399', b: '#10b981' },
  { id: 'gold', name: 'Золото', a: '#fbbf24', b: '#f59e0b' },
  { id: 'neon', name: 'Неон', a: '#fb7185', b: '#e11d48' },
  { id: 'night', name: 'Ночь', a: '#818cf8', b: '#6366f1' },
];

// Кнопка-палитра: точка в текущем акценте (var(--accent)); клик открывает поповер.
export function themeBtn() {
  return `<button class="iconbtn theme-btn" data-act="theme-menu" title="Тема оформления"><span class="theme-cur"></span></button>`;
}

// Поповер выбора темы: сетка из 9 «фишек» (превью-точка + имя), активная помечена .on.
export function renderThemePopover(current) {
  const chips = THEMES.map(t =>
    `<button class="theme-chip ${t.id === current ? 'on' : ''}" data-theme="${t.id}" title="${t.name}">`
    + `<span class="theme-dot" style="background:linear-gradient(135deg,${t.a},${t.b})"></span>`
    + `<span class="theme-name">${t.name}</span></button>`).join('');
  return `<div class="theme-pop-hd">Тема оформления</div><div class="theme-grid">${chips}</div>`;
}

export function renderHeader(state, mode, activeView = 'list') {
  const p = statusPill(state);
  const conn = connBtn(state);
  const theme = themeBtn();
  // ⚙ работает как переключатель: открыть настройки / вернуться к списку. В настройках — подсвечена.
  const gear = `<button class="iconbtn gear ${activeView === 'settings' ? 'on' : ''}" data-act="show-settings" title="${activeView === 'settings' ? 'Назад к списку' : 'Настройки'}">⚙</button>`;
  // Кнопки своей рамки (frameless-окно) — видны только в нативном приложении
  // (класс .native на <html>), в браузере/mock скрыты. Свернуть / закрыть-в-трей.
  const win = `<button class="wbtn" data-win="minimize" title="Свернуть">–</button>`
    + `<button class="wbtn wbtn-close" data-win="close" title="Скрыть в трей">✕</button>`;
  // detailed: рейл уже показывает бренд "lanmesh" — не дублируем вордмарк в шапке.
  if (mode === 'detailed') {
    return `<div class="hd"><span class="grow"></span>`
      + `<span class="pill ${p.cls}"><span class="pdot"></span>${p.text}</span>${conn}${theme}`
      + `<button class="iconbtn" data-act="collapse" title="Компактный режим">⤡</button>`
      + win + `</div>`;
  }
  return `<div class="hd"><span class="wm">lan<b>mesh</b></span><span class="grow"></span>`
    + `<span class="pill ${p.cls}"><span class="pdot"></span>${p.text}</span>${conn}${theme}`
    + gear
    + `<button class="iconbtn" data-act="expand" title="Подробный режим">⤢</button>`
    + win + `</div>`;
}

// Рейл подробного режима: список сетей + навигация видов.
export function renderRail(state, activeView, activeNetTag) {
  const nets = displayNets(state); // включает сохранённые-неактивные (после отключения)
  // Если активный тег не задан или не совпадает ни с одной сетью — подсвечиваем первую.
  const activeTag = nets.some(n => n.tag === activeNetTag) ? activeNetTag : (nets[0] && nets[0].tag);
  const netsHtml = nets.map(n =>
    `<div class="netitem ${n.tag === activeTag ? 'on' : ''}${n.inactive ? ' off' : ''}" data-view="list" data-net="${esc(n.tag)}"><span class="pdot"></span>${escName(n.name)}`
    + `<span class="cnt">${n.inactive ? '' : (n.peers || []).length}</span></div>`).join('');
  // Пункт «＋ Добавить сеть» под списком сетей — в подробном режиме иначе сеть не добавить.
  const addItem = `<div class="netitem add ${activeView === 'add' ? 'on' : ''}" data-view="add">＋ Добавить сеть</div>`;
  const nav = [['list', '▤', 'Список'], ['traffic', '▮', 'Трафик'], ['settings', '⚙', 'Настройки']]
    .map(([v, ic, t]) => `<div class="n ${v === activeView ? 'on' : ''}" data-view="${v}">${ic}&nbsp; ${t}</div>`).join('');
  return `<div class="rail"><div class="brand">lan<b>mesh</b></div>${netsHtml}${addItem}<div class="nav">${nav}</div></div>`;
}
