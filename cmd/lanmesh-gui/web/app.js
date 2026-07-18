import { renderHeader, renderRail, pickMode } from './views/shell.js';

const POLL_MS = 1300;
let mode = localStorage.getItem('lm-mode') || pickMode(innerWidth);
let manual = localStorage.getItem('lm-mode') != null;
let activeView = 'list';
let lastState = { running: false, networks: [] };

function setMode(m) { mode = m; document.getElementById('root').dataset.mode = m; }
function render(state) {
  lastState = state;
  document.getElementById('header').innerHTML = renderHeader(state, mode);
  document.getElementById('rail').innerHTML = mode === 'detailed' ? renderRail(state, activeView) : '';
  // тело вида монтируется в Task 10/11 (renderView)
  if (window.renderView) document.getElementById('view').innerHTML = window.renderView(state, mode, activeView);
}
async function poll() {
  try { const r = await fetch('/api/state'); if (!r.ok) return; render(await r.json()); }
  catch (e) { /* переживём сбой */ }
}
// ⤢/⤡ и навигация
document.addEventListener('click', (e) => {
  const act = e.target.closest('[data-act]')?.dataset.act;
  if (act === 'expand') { manual = true; localStorage.setItem('lm-mode', 'detailed'); setMode('detailed'); render(lastState); return; }
  if (act === 'collapse') { manual = true; localStorage.setItem('lm-mode', 'compact'); setMode('compact'); render(lastState); return; }
  const v = e.target.closest('[data-view]')?.dataset.view;
  if (v) { activeView = v; render(lastState); }
});
// Отзывчивость: если пользователь не выбирал режим руками — следуем ширине окна.
new ResizeObserver(() => { if (!manual) setMode(pickMode(innerWidth)); render(lastState); }).observe(document.documentElement);

setMode(mode);
poll();
setInterval(poll, POLL_MS);
