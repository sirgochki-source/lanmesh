// Настройки: сервера (сигналки/relay) + диагностика (Task 13).
// По явной просьбе показываем СВОИ (кастомные) адреса из cfgSignals/cfgRelay и даём
// их править/удалять/добавлять. Стандартные (дефолтные) адреса — плейсхолдеры, их не
// раскрываем: пустой список = используются стандартные серверы.
import { esc } from '../lib/sanitize.js';

// srvRow — одна редактируемая строка сигналки: поле адреса + кнопка удаления.
export const srvRow = (url = '', dis = '') =>
  `<div class="srv-row"><input class="s-sig" value="${esc(url)}" placeholder="https://…" ${dis}>`
  + `<button class="srv-del" data-act="sig-del" title="Удалить" ${dis}>✕</button></div>`;

// settingsBody — внутреннее содержимое настроек без обёртки. Переиспользуется и
// подробным видом (в .dmain), и компактным экраном (renderSettingsCompact).
export function settingsBody(state) {
  const dis = state.running ? 'disabled' : '';
  const lockNote = state.running
    ? 'Сеть подключена — отключись, чтобы менять серверы.'
    : 'Пустой список = используются стандартные серверы.';
  const sigs = state.cfgSignals || [];
  const rows = (sigs.length ? sigs : ['']).map(u => srvRow(u, dis)).join('');
  return `<h2 class="soon-h">Сигнальные серверы</h2>`
    + `<div id="sig-list" class="srv-list">${rows}</div>`
    + `<button class="btn-ghost srv-add" data-act="sig-add" ${dis}>＋ Добавить сигналку</button>`
    + `<h2 class="soon-h">Ретранслятор (relay)</h2>`
    + `<input id="s-relay" class="s-relay" value="${esc(state.cfgRelay || '')}" placeholder="host:port" ${dis}>`
    + `<div class="frow"><button class="btn-primary" data-act="cfg-save" ${dis}>Сохранить</button>`
    + `<button class="btn-ghost" data-act="cfg-reset" ${dis}>Сбросить к стандартным</button></div>`
    + `<div class="hint">${lockNote}</div>`
    + `<h2 class="soon-h">Диагностика</h2>`
    + `<button class="btn-ghost" data-act="senddiag">📤 Отправить диагностику</button>`
    + `<label class="chk"><input type="checkbox" id="s-logs" ${state.sendLogs ? 'checked' : ''} data-act="sendlogs"> автоотправка логов</label>`
    + `<div id="diag-note" class="hint"></div>`;
}

// Подробный режим — настройки в рабочей области.
export function renderSettings(state) {
  return `<div class="dmain">${settingsBody(state)}</div>`;
}

// Компактный режим — отдельный экран настроек с кнопкой возврата к списку
// (в компакте нет рейла с навигацией, вход — через ⚙ в шапке).
export function renderSettingsCompact(state) {
  return `<div class="cfg-compact"><div class="cfg-compact-hd">`
    + `<button class="btn-ghost" data-act="show-list">← Список</button>`
    + `<span class="cfg-compact-title">Настройки</span></div>`
    + settingsBody(state) + `</div>`;
}
