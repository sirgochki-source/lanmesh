export function parseInvite(text) {
  text = (text || '').trim();
  const qi = text.indexOf('?');
  const q = qi >= 0 ? text.slice(qi + 1) : text;
  // disc — способ обнаружения участников. Он вшит в ключ сети, поэтому приглашение
  // несёт его наравне с именем и паролем: выбрать его «по-своему» нельзя, иначе
  // это будет просто другая сеть.
  let net = null, pass = null, sigs = [], relay = null, disc = null;
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
    else if (k === 'disc') disc = (v === 'dht' || v === 'dht+relay') ? v : 'signal';
  }
  return { net, pass, sigs, relay, disc };
}
