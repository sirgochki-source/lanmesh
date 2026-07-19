// Вид «Карта» (Phase 4): топология сети — ты в центре, пиры по окружности вокруг тебя,
// линия к каждому раскрашена/оформлена по статусу связи. Чистый inline-SVG строкой,
// без внешних либ — тот же принцип, что у sparklineSVG (lib/sparkline.js).
import { dispName, esc } from '../lib/sanitize.js';
import { fmtRtt } from '../lib/format.js';
import { quality } from '../lib/quality.js';

// Геометрия — фиксированный viewBox, реальный размер на экране даёт CSS (.svgmap: width
// 100%, max-width). cy сдвинут выше центра H/2, чтобы под центральным узлом хватило места
// на две строки подписи (имя + IP) без обрезки нижним краем viewBox.
const W = 640, H = 500, CX = 320, CY = 210;
const R_ORBIT = 150;  // расстояние центр → узел пира
const R_CENTER = 38;  // радиус центрального узла (себя)
const R_PEER = 24;    // радиус узла пира
const R_LABEL = 196;  // расстояние центр → якорь подписи пира (за узлом, по тому же лучу)

const initial = (name) => dispName((name || '?').trim().charAt(0).toUpperCase() || '?');

// Статус решает основное (цвет + пунктир, по спеке), rttMs у direct слегка меняет
// толщину/непрозрачность (quality() без истории — этого достаточно, чтобы «direct,
// но неидеальный» пинг не выглядел так же уверенно, как «direct, отличный»).
function edgeStyle(peer) {
  if (peer.status === 'direct') {
    const q = quality('direct', peer.rttMs ?? -1);
    const width = q.level === 'bad' ? 2 : q.level === 'ok' ? 2.6 : 3.2;
    return { stroke: 'var(--accent-2)', width, dash: null, opacity: q.level === 'bad' ? 0.75 : 1 };
  }
  if (peer.status === 'relay') return { stroke: 'var(--relay)', width: 2.2, dash: null, opacity: 0.85 };
  return { stroke: 'var(--ping-ok)', width: 1.6, dash: '5,4', opacity: 0.75 }; // connecting
}

function pingText(peer) {
  if (peer.status === 'connecting') return '…';
  const r = fmtRtt(peer.rttMs ?? -1);
  return r || '…';
}

// Якорь подписи по стороне окружности: справа от центра — текст растёт вправо (start),
// слева — влево (end), сверху/снизу — по центру (middle). Иначе длинные имена у боковых
// узлов налезали бы на линию или уезжали за viewBox не в ту сторону.
function anchorFor(cos) {
  if (cos > 0.3) return 'start';
  if (cos < -0.3) return 'end';
  return 'middle';
}

function peerNode(peer, i, n) {
  // Начинаем с 12 часов (-π/2) и идём по часовой стрелке — с одним пиром узел оказывается
  // прямо сверху, а не сбоку (эстетика; формула угла i/N·2π сама по себе от спеки).
  const angle = (i / n) * 2 * Math.PI - Math.PI / 2;
  const cos = Math.cos(angle), sin = Math.sin(angle);
  const x = CX + R_ORBIT * cos, y = CY + R_ORBIT * sin;
  const lx = CX + R_LABEL * cos, ly = CY + R_LABEL * sin;
  const anchor = anchorFor(cos);
  const st = edgeStyle(peer);
  const dashAttr = st.dash ? ` stroke-dasharray="${st.dash}"` : '';
  const name = dispName(peer.name || 'узел');
  const ping = pingText(peer);
  const line = `<line class="map-edge" x1="${CX}" y1="${CY}" x2="${x.toFixed(1)}" y2="${y.toFixed(1)}" `
    + `stroke="${st.stroke}" stroke-width="${st.width}" stroke-opacity="${st.opacity}" `
    + `stroke-linecap="round"${dashAttr}/>`;
  const node = `<circle cx="${x.toFixed(1)}" cy="${y.toFixed(1)}" r="${R_PEER}" `
    + `fill="rgba(255,255,255,.07)" stroke="${st.stroke}" stroke-width="1.5"/>`
    + `<text x="${x.toFixed(1)}" y="${y.toFixed(1)}" text-anchor="middle" dominant-baseline="central" `
    + `font-size="14" font-weight="700" fill="var(--fg)">${initial(peer.name)}</text>`;
  const label = `<text x="${lx.toFixed(1)}" y="${(ly - 6).toFixed(1)}" text-anchor="${anchor}" `
    + `font-size="12" font-weight="600" fill="var(--fg)">${name}</text>`
    + `<text x="${lx.toFixed(1)}" y="${(ly + 10).toFixed(1)}" text-anchor="${anchor}" `
    + `font-size="10.5" font-family="var(--mono)" fill="var(--fg-mut)">${ping}</text>`;
  return line + node + label;
}

function centerNode(state) {
  const name = dispName(state.selfName || 'ты');
  const ip = esc(state.selfIP || '—');
  return `<circle cx="${CX}" cy="${CY}" r="${R_CENTER}" fill="rgba(55,245,197,.14)" `
    + `stroke="var(--accent-2)" stroke-width="2"/>`
    + `<text x="${CX}" y="${CY}" text-anchor="middle" dominant-baseline="central" `
    + `font-size="15" font-weight="700" fill="var(--accent-2)">${initial(state.selfName || 'ты')}</text>`
    + `<text x="${CX}" y="${CY + R_CENTER + 18}" text-anchor="middle" font-size="13" `
    + `font-weight="700" fill="var(--fg)">${name}</text>`
    + `<text x="${CX}" y="${CY + R_CENTER + 34}" text-anchor="middle" font-size="10.5" `
    + `font-family="var(--mono)" fill="var(--fg-mut)">${ip}</text>`;
}

const emptyCaption = () => `<text x="${CX}" y="${CY + R_CENTER + 52}" text-anchor="middle" `
  + `font-size="13" fill="var(--fg-mut)">Пока никого.</text>`;

export function renderMap(state, net) {
  const peers = (net && net.peers) || [];
  const n = peers.length;
  const body = centerNode(state || {})
    + (n ? peers.map((p, i) => peerNode(p, i, n)).join('') : emptyCaption());
  return `<svg class="svgmap" viewBox="0 0 ${W} ${H}" role="img" aria-label="Карта сети" `
    + `xmlns="http://www.w3.org/2000/svg">${body}</svg>`;
}
