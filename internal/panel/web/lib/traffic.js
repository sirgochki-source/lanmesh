// Чистый расчёт скорости (байт/с) из двух последовательных кумулятивных снимков —
// вынесено из app.js для тестируемости (Phase 3, вид «Трафик»).
// prev/cur: {vip: {rx, tx}} — кумулятивные байты на момент снимка.
// Возвращает {vip: {rxRate, txRate}} только для vip, присутствующих в cur.
export function computeRates(prev, cur, dtSec) {
  const out = {};
  const haveDt = dtSec > 0;                       // dt<=0 (первый снимок/несогласованные часы) — скорость 0
  for (const vip in cur) {
    const c = cur[vip] || { rx: 0, tx: 0 };
    const p = haveDt ? (prev && prev[vip]) : null; // нет предыдущего значения для vip — тоже 0
    if (!p) { out[vip] = { rxRate: 0, txRate: 0 }; continue; }
    out[vip] = {
      // clamp >=0: счётчики только растут, отрицательная дельта — сбой/рестарт узла, не «скорость назад»
      rxRate: Math.max(0, (c.rx - p.rx) / dtSec),
      txRate: Math.max(0, (c.tx - p.tx) / dtSec),
    };
  }
  return out;
}
