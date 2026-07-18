import { test } from 'node:test';
import assert from 'node:assert/strict';
import { peerRowCompact, netCardCompact, renderCompact, addFormHtml, renderView } from '../../web/views/list.js';

// renderView читает window.renderDetailed (заглушка до Task 11); в браузере window
// всегда есть, но в node --test его нет — подставляем минимальный shim, чтобы
// проверить реальную ветку диспетчера, а не только ту, что не трогает window.
globalThis.window ??= globalThis;

test('peerRowCompact: класс статуса, vip, пинг', () => {
  const s = peerRowCompact({ name: 'Мурад', vip: '25.44.12.9', status: 'direct', rttMs: 18, lastSeenMs: 800 });
  assert.match(s, /sdot direct/);
  assert.match(s, /25\.44\.12\.9/);
  assert.match(s, /18 мс/);
});
test('peerRowCompact: relay/connecting классы и подпись подключения', () => {
  const relay = peerRowCompact({ name: 'sara_pc', vip: '25.44.31.7', status: 'relay', rttMs: 128 });
  assert.match(relay, /sdot relay/);
  const conn = peerRowCompact({ name: 'Dev_null', vip: '25.44.9.1', status: 'connecting', rttMs: -1 });
  assert.match(conn, /sdot conn/);
  assert.match(conn, /png conn/);
  assert.match(conn, /подключение…/);
});
test('peerRowCompact санитизирует враждебное имя', () => {
  const s = peerRowCompact({ name: 'a‮exe', vip: '1', status: 'direct', rttMs: 5 });
  assert.ok(!s.includes('‮'));            // bidi-override вычищен
  assert.match(s, /aexe/);
});
test('peerRowCompact окрашивает пинг по порогам rttClass', () => {
  assert.match(peerRowCompact({ name: 'a', vip: '1', status: 'direct', rttMs: 18 }), /png rtt-good/);
  assert.match(peerRowCompact({ name: 'a', vip: '1', status: 'direct', rttMs: 80 }), /png rtt-ok/);
  assert.match(peerRowCompact({ name: 'a', vip: '1', status: 'direct', rttMs: 200 }), /png rtt-bad/);
});

test('netCardCompact: заголовок, счётчик, кнопки, точка сигналки', () => {
  const s = netCardCompact({ name: 'myteam', tag: 'tag123', signalError: '', peers: [
    { name: 'A', vip: '25.0.0.2', status: 'direct', rttMs: 10 },
    { name: 'B', vip: '25.0.0.1', status: 'direct', rttMs: 10 },
  ] });
  assert.match(s, /myteam/);
  assert.match(s, /· 2/);
  assert.match(s, /data-invite="tag123"/);
  assert.match(s, /data-leave="tag123"/);
  assert.match(s, /🟢/);
  // отсортировано по vip
  assert.ok(s.indexOf('25.0.0.1') < s.indexOf('25.0.0.2'));
});
test('netCardCompact: жёлтая точка при ошибке сигналки', () => {
  const s = netCardCompact({ name: 'n', tag: 't', signalError: 'недоступна', peers: [] });
  assert.match(s, /🟡/);
});
test('netCardCompact: пустая сеть даёт подсказку с именем сети', () => {
  const s = netCardCompact({ name: 'myteam', tag: 't', peers: [] });
  assert.match(s, /Пока никого/);
  assert.match(s, /Позови друга/);
  assert.match(s, /<b>myteam<\/b>/);
  assert.match(s, /Пригласить/);
});

test('renderCompact для пустой сети даёт подсказку', () => {
  const s = renderCompact({ running: true, selfEndpoint: 'x', networks: [{ name: 'myteam', tag: 't', signals: [], peers: [] }] });
  assert.match(s, /Пока никого|Позови/);
});
test('renderCompact показывает warn при отсутствии внешнего адреса', () => {
  const s = renderCompact({ running: true, selfEndpoint: '', networks: [{ name: 'n', tag: 't', signals: [], peers: [] }] });
  assert.match(s, /Внешний адрес неизвестен/);
});
test('renderCompact не показывает warn, когда внешний адрес есть', () => {
  const s = renderCompact({ running: true, selfEndpoint: 'x', networks: [] });
  assert.doesNotMatch(s, /Внешний адрес неизвестен/);
});
test('renderCompact: без running нет self-строки и карточек, но форма добавления есть', () => {
  const s = renderCompact({ running: false, selfEndpoint: '', networks: [] });
  assert.doesNotMatch(s, /class="self"/);
  assert.match(s, /add-toggle/);
});
test('renderCompact: форма добавления сети раскрыта, только когда сетей нет', () => {
  const empty = renderCompact({ running: true, selfEndpoint: 'x', networks: [] });
  assert.doesNotMatch(empty, /add-body" hidden/);
  const withNet = renderCompact({ running: true, selfEndpoint: 'x', networks: [{ name: 'n', tag: 't', peers: [] }] });
  assert.match(withNet, /add-body" hidden/);
});

test('addFormHtml: hidden-атрибут переключается флагом open', () => {
  assert.doesNotMatch(addFormHtml(true), /add-body" hidden/);
  assert.match(addFormHtml(false), /add-body" hidden/);
});

test('renderView: диспетчер compact вызывает renderCompact', () => {
  const state = { running: true, selfEndpoint: 'x', networks: [] };
  assert.equal(renderView(state, 'compact', 'list'), renderCompact(state));
});
test('renderView: без window.renderDetailed режим detailed падает обратно на renderCompact', () => {
  const state = { running: true, selfEndpoint: 'x', networks: [] };
  assert.equal(renderView(state, 'detailed', 'list'), renderCompact(state));
});
