export function parseInvite(text) {
  text = (text || '').trim();
  const qi = text.indexOf('?');
  const q = qi >= 0 ? text.slice(qi + 1) : text;
  let net = null, pass = null, sigs = [], relay = null;
  for (const part of q.split('&')) {
    const eq = part.indexOf('=');
    if (eq < 0) continue;
    const k = part.slice(0, eq);
    let v;
    try { v = decodeURIComponent(part.slice(eq + 1).replace(/\+/g, ' ')); }
    catch (e) { continue; }               // битую %-последовательность игнорируем
    if (k === 'net') net = v;
    else if (k === 'pass') pass = v;
    else if (k === 'sig') sigs.push(v);
    else if (k === 'relay') relay = v;
  }
  return { net, pass, sigs, relay };
}
