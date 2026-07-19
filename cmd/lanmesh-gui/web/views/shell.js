// Оболочка приложения: чистые функции рендера (шапка, рейл, статус-pill,
// выбор режима по ширине). DOM-монтаж и опрос — в app.js.
import { plural } from '../lib/format.js';
import { dispName as escName, esc } from '../lib/sanitize.js';

export function pickMode(width) { return width < 620 ? 'compact' : 'detailed'; }

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

export function renderHeader(state, mode) {
  const p = statusPill(state);
  const conn = connBtn(state);
  // Кнопки своей рамки (frameless-окно) — видны только в нативном приложении
  // (класс .native на <html>), в браузере/mock скрыты. Свернуть / закрыть-в-трей.
  const win = `<button class="wbtn" data-win="minimize" title="Свернуть">–</button>`
    + `<button class="wbtn wbtn-close" data-win="close" title="Скрыть в трей">✕</button>`;
  // detailed: рейл уже показывает бренд "lanmesh" — не дублируем вордмарк в шапке.
  if (mode === 'detailed') {
    return `<div class="hd"><span class="grow"></span>`
      + `<span class="pill ${p.cls}"><span class="pdot"></span>${p.text}</span>${conn}`
      + `<button class="iconbtn" data-act="collapse" title="Компактный режим">⤡</button>`
      + win + `</div>`;
  }
  return `<div class="hd"><span class="wm">lan<b>mesh</b></span><span class="grow"></span>`
    + `<span class="pill ${p.cls}"><span class="pdot"></span>${p.text}</span>${conn}`
    + `<button class="iconbtn" data-act="show-settings" title="Настройки">⚙</button>`
    + `<button class="iconbtn" data-act="expand" title="Подробный режим">⤢</button>`
    + win + `</div>`;
}

// Рейл подробного режима: список сетей + навигация видов.
export function renderRail(state, activeView, activeNetTag) {
  const nets = state.networks || [];
  // Если активный тег не задан или не совпадает ни с одной сетью — подсвечиваем первую.
  const activeTag = nets.some(n => n.tag === activeNetTag) ? activeNetTag : (nets[0] && nets[0].tag);
  const netsHtml = nets.map(n =>
    `<div class="netitem ${n.tag === activeTag ? 'on' : ''}" data-view="list" data-net="${esc(n.tag)}"><span class="pdot"></span>${escName(n.name)}`
    + `<span class="cnt">${(n.peers || []).length}</span></div>`).join('');
  const nav = [['list', '▤', 'Список'], ['traffic', '▮', 'Трафик'], ['settings', '⚙', 'Настройки']]
    .map(([v, ic, t]) => `<div class="n ${v === activeView ? 'on' : ''}" data-view="${v}">${ic}&nbsp; ${t}</div>`).join('');
  return `<div class="rail"><div class="brand">lan<b>mesh</b></div>${netsHtml}<div class="nav">${nav}</div></div>`;
}
