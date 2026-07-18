export class RttHistory {
  constructor(cap = 40) { this.cap = cap; this.map = new Map(); }
  push(vip, rttMs) {
    if (rttMs < 0) return;                       // «нет измерения» не пишем
    let arr = this.map.get(vip);
    if (!arr) { arr = []; this.map.set(vip, arr); }
    arr.push(rttMs);
    if (arr.length > this.cap) arr.shift();
  }
  get(vip) { return (this.map.get(vip) || []).slice(); }
  prune(activeVips) {
    const live = new Set(activeVips);
    for (const k of this.map.keys()) if (!live.has(k)) this.map.delete(k);
  }
}
