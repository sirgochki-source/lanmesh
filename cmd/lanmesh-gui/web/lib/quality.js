// Разброс = стандартное отклонение по истории (если точек мало — 0).
function stdev(a) {
  if (a.length < 3) return 0;
  const m = a.reduce((s, x) => s + x, 0) / a.length;
  return Math.sqrt(a.reduce((s, x) => s + (x - m) ** 2, 0) / a.length);
}
const L = { good: 'отличное', ok: 'среднее', bad: 'плохое', relayok: 'через релей', connecting: 'подключение' };
export function quality(status, rttMs, history = []) {
  if (status === 'connecting' || rttMs < 0) return { level: 'connecting', label: L.connecting };
  const jittery = stdev(history) > 25;                 // мс — заметно «дёргается»
  if (status === 'relay') return rttMs < 150 ? { level: 'ok', label: L.relayok } : { level: 'bad', label: L.bad };
  // direct
  if (rttMs < 60 && !jittery) return { level: 'good', label: L.good };
  if (rttMs < 150) return { level: 'ok', label: L.ok };
  return { level: 'bad', label: L.bad };
}
