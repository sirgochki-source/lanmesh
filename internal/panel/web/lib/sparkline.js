export function scalePoints(values, w, h) {
  const n = values.length;
  if (n === 0) return [];
  const min = Math.min(...values), max = Math.max(...values);
  const span = max - min || 1;
  const pad = 1;                                  // чтобы линия не липла к краям
  return values.map((v, i) => ({
    x: n === 1 ? 0 : +( (i / (n - 1)) * w ).toFixed(2),
    y: +( pad + (1 - (v - min) / span) * (h - 2 * pad) ).toFixed(2),
  }));
}

export function sparklineSVG(values, { w = 40, h = 16, stroke = '#46e6c0' } = {}) {
  if (!values || values.length < 2) return '';
  const pts = scalePoints(values, w, h).map(p => `${p.x},${p.y}`).join(' ');
  return `<svg class="spark" width="${w}" height="${h}" viewBox="0 0 ${w} ${h}" aria-hidden="true">`
    + `<polyline points="${pts}" fill="none" stroke="${stroke}" stroke-width="1.5" `
    + `stroke-linejoin="round" stroke-linecap="round"/></svg>`;
}
