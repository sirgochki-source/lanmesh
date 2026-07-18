// Вид «Трафик» (Phase 3): накопленные bytesRx/bytesTx с /api/state + текущая скорость
// (последний снимок computeRates() из app.js, см. lib/traffic.js). Разметка переиспользует
// классы Command Glass из renderDetailed (.dmain/.dhd/.tiles/.tile/.drow/.who) — новые
// классы (.trow/.tbar*/.tcell*) только для того, чего в существующих видах не было
// (полоса нагрузки, парная колонка ↓/↑ с текущей скоростью под накопленным значением).
import { dispName } from '../lib/sanitize.js';
import { fmtBytes } from '../lib/format.js';

// Совокупный трафик сети: сумма накопленных RX/TX её пиров + сумма их текущих скоростей.
// Общий для плитки «Трафик» в основном списке (renderDetailed) и шапки этого вида.
export function netTrafficTotals(net, rates = {}) {
  const peers = (net && net.peers) || [];
  let rx = 0, tx = 0, rate = 0;
  for (const p of peers) {
    rx += p.bytesRx || 0;
    tx += p.bytesTx || 0;
    const r = rates[p.vip];
    if (r) rate += (r.rxRate || 0) + (r.txRate || 0);
  }
  return { rx, tx, rate };
}

function peerTrafficRow(p, rate, maxTotal) {
  const r = rate || { rxRate: 0, txRate: 0 };
  const total = (p.bytesRx || 0) + (p.bytesTx || 0);
  const pct = maxTotal > 0 ? Math.round(total / maxTotal * 100) : 0;
  return `<div class="drow trow"><span class="who"><span class="nm">${dispName(p.name || 'узел')}</span></span>`
    + `<div class="tbar-wrap"><div class="tbar" style="width:${pct}%"></div></div>`
    + `<span class="tcell rx"><span class="tarr">↓</span><span class="tval">${fmtBytes(p.bytesRx || 0)}</span>`
    + `<span class="trate">${fmtBytes(r.rxRate)}/с</span></span>`
    + `<span class="tcell tx"><span class="tarr">↑</span><span class="tval">${fmtBytes(p.bytesTx || 0)}</span>`
    + `<span class="trate">${fmtBytes(r.txRate)}/с</span></span></div>`;
}

export function renderTraffic(net, rates = {}) {
  const peers = (net && net.peers) || [];
  const totals = netTrafficTotals(net, rates);
  // Сортировка по убыванию суммарного трафика — «топ по нагрузке» наверху, а не порядок IP
  // (в отличие от списка участников: здесь смысл вида именно в сравнении вклада пиров).
  const sorted = peers.slice().sort((a, b) =>
    ((b.bytesRx || 0) + (b.bytesTx || 0)) - ((a.bytesRx || 0) + (a.bytesTx || 0)));
  const maxTotal = Math.max(0, ...sorted.map(p => (p.bytesRx || 0) + (p.bytesTx || 0)));
  const rows = sorted.length
    ? sorted.map(p => peerTrafficRow(p, rates[p.vip], maxTotal)).join('')
    : '<div class="empty">Пока никого.</div>';
  return `<div class="dmain"><div class="dhd"><div><div class="title">Трафик</div>`
    + `<div class="sub">${dispName((net && net.name) || '')}</div></div><span class="grow"></span></div>`
    + `<div class="tiles">`
    + `<div class="tile"><div class="k">Всего передано</div><div class="big">${fmtBytes(totals.rx + totals.tx)}</div></div>`
    + `<div class="tile"><div class="k">Скорость сейчас</div><div class="big">${fmtBytes(totals.rate)}/с</div></div>`
    + `</div><div class="drows">${rows}</div></div>`;
}
