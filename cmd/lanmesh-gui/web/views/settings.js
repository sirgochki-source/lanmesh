// Настройки: сервера (сигналки/relay) + диагностика (Task 13).
// Адреса серверов НЕ показываем (как в старом UI) — только поля для ввода своих.

// settingsBody — внутреннее содержимое настроек без обёртки. Переиспользуется и
// подробным видом (в .dmain), и компактным экраном (renderSettingsCompact).
export function settingsBody(state) {
  const locked = state.running ? 'disabled' : '';
  const lockNote = state.running ? 'Сеть подключена — отключись, чтобы менять серверы.' : 'Пусто = стандартные серверы.';
  return `<h2 class="soon-h">Серверы</h2>`
    + `<label>сигналки (по одной ссылке на строку)</label>`
    + `<textarea id="s-signals" rows="3" placeholder="https://…" ${locked}></textarea>`
    + `<label>ретранслятор (relay), host:port</label>`
    + `<input id="s-relay" placeholder="host:port" ${locked}>`
    + `<div class="frow"><button class="btn-primary" data-act="cfg-save" ${locked}>Сохранить</button>`
    + `<button class="btn-ghost" data-act="cfg-reset" ${locked}>Сбросить</button></div>`
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
