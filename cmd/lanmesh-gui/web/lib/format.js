export function fmtRtt(ms) {
  if (ms < 0) return null;
  if (ms < 10) return ms.toFixed(1) + ' мс';
  return Math.round(ms) + ' мс';
}
export function rttClass(ms) {            // пороги по ощущениям от игры
  if (ms < 60) return 'rtt-good';
  if (ms < 150) return 'rtt-ok';
  return 'rtt-bad';
}
export function fmtUptime(sec) {
  if (sec < 60) return sec + ' с';
  const m = Math.floor(sec / 60), h = Math.floor(m / 60);
  if (h > 0) return h + ' ч ' + (m % 60) + ' м';
  return m + ' м ' + (sec % 60) + ' с';
}
export function fmtSeen(ms) {
  if (ms < 0) return 'нет пакетов';
  if (ms < 2000) return 'только что';
  return Math.round(ms / 1000) + ' с назад';
}
export function plural(n, one, few, many) {
  const d = n % 10, dd = n % 100;
  if (d === 1 && dd !== 11) return one;
  if (d >= 2 && d <= 4 && (dd < 12 || dd > 14)) return few;
  return many;
}
