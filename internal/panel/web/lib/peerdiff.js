export function diffPeers(prev, next) {
  const prevSet = new Set(prev.map(p => p.vip));
  const nextSet = new Set(next.map(p => p.vip));
  return {
    joined: next.filter(p => !prevSet.has(p.vip)).map(p => ({ vip: p.vip, name: p.name })),
    left: prev.filter(p => !nextSet.has(p.vip)).map(p => ({ vip: p.vip, name: p.name })),
  };
}
