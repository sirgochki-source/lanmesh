// Плоский список пиров всех сетей — адаптер между app.js (side-effectful poll loop)
// и чистыми модулями (RttHistory/diffPeers), которые не знают про структуру networks[].
export function collectPeers(state) {
  const out = [];
  for (const n of state.networks || []) for (const p of n.peers || []) out.push({ vip: p.vip, name: p.name, rttMs: p.rttMs, status: p.status });
  return out;
}
