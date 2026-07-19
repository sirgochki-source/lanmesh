import { test } from 'node:test';
import assert from 'node:assert/strict';
import { statusPill, pickMode, renderHeader, renderRail, connBtn, displayNets } from '../../web/views/shell.js';

test('statusPill отражает состояние', () => {
  assert.equal(statusPill({ running: false, networks: [] }).cls, 'off');
  assert.equal(statusPill({ running: true, selfEndpoint: '', networks: [] }).cls, 'warn'); // нет внешнего адреса
  assert.equal(statusPill({ running: true, selfEndpoint: 'x', networks: [{ signalError: '' }] }).cls, 'on');
});
test('pickMode по ширине (порог 620)', () => {
  assert.equal(pickMode(400), 'compact');
  assert.equal(pickMode(900), 'detailed');
});
test('renderHeader (compact) содержит вордмарк бренда', () => {
  const h = renderHeader({ running: true, selfEndpoint: 'x', networks: [] }, 'compact');
  assert.match(h, /lan<b>mesh<\/b>/);
});
test('renderHeader (detailed) НЕ дублирует вордмарк — рейл его уже показывает', () => {
  const h = renderHeader({ running: true, selfEndpoint: 'x', networks: [] }, 'detailed');
  assert.doesNotMatch(h, /class="wm"/);
  assert.match(h, /data-act="collapse"/);
  assert.match(h, /class="pill/);
});

test('connBtn: онлайн → «Отключиться» (disconnect)', () => {
  const s = connBtn({ running: true, savedNetworks: 1 });
  assert.match(s, /data-act="disconnect"/);
  assert.match(s, /Отключиться/);
  assert.match(s, /is-on/);
});
test('connBtn: офлайн с сохранёнными сетями → «Подключиться» (reconnect)', () => {
  const s = connBtn({ running: false, savedNetworks: 2 });
  assert.match(s, /data-act="reconnect"/);
  assert.match(s, /Подключиться/);
  assert.match(s, /is-off/);
});
test('connBtn: офлайн без сохранённых сетей → кнопки нет', () => {
  assert.equal(connBtn({ running: false, savedNetworks: 0 }), '');
  assert.equal(connBtn({ running: false }), '');
});
test('renderHeader: содержит кнопку подключения по состоянию', () => {
  assert.match(renderHeader({ running: true, selfEndpoint: 'x', networks: [] }, 'compact'), /data-act="disconnect"/);
  assert.match(renderHeader({ running: false, savedNetworks: 1, networks: [] }, 'detailed'), /data-act="reconnect"/);
});

test('displayNets: сохранённая, но не активная сеть → inactive-заглушка', () => {
  const out = displayNets({ networks: [{ tag: 'a', name: 'Alpha', peers: [] }], savedNets: [{ tag: 'a', name: 'Alpha' }, { tag: 'b', name: 'Beta' }] });
  assert.equal(out.length, 2);
  assert.equal(out.find(n => n.tag === 'a').inactive, undefined); // активная — как есть
  assert.equal(out.find(n => n.tag === 'b').inactive, true);      // сохранённая, не активная
});
test('displayNets: без savedNets — только активные (обратная совместимость)', () => {
  assert.deepEqual(displayNets({ networks: [{ tag: 'a', name: 'A' }] }).map(n => n.tag), ['a']);
});
test('renderRail: неактивная сеть помечена .off', () => {
  const html = renderRail({ networks: [], savedNets: [{ tag: 'b', name: 'Beta' }] }, 'list', null);
  assert.match(html, /class="netitem on off"|netitem[^"]*off/);
  assert.match(html, /Beta/);
});

test('renderRail: помечает активную сеть .on и эмитит data-net', () => {
  const state = { networks: [
    { tag: 'a', name: 'Alpha', peers: [] },
    { tag: 'b', name: 'Beta', peers: [] },
  ] };
  const html = renderRail(state, 'list', 'b');
  assert.match(html, /data-net="a"/);
  assert.match(html, /data-net="b"/);
  const netitemRe = /<div class="netitem ?(on)?" data-view="list" data-net="([^"]+)">/g;
  const items = Object.fromEntries([...html.matchAll(netitemRe)].map(m => [m[2], m[1] === 'on']));
  assert.equal(items.a, false);
  assert.equal(items.b, true);
});
test('renderRail: без совпадения activeNetTag подсвечивает первую сеть', () => {
  const state = { networks: [
    { tag: 'a', name: 'Alpha', peers: [] },
    { tag: 'b', name: 'Beta', peers: [] },
  ] };
  const html = renderRail(state, 'list', null);
  const netitemRe = /<div class="netitem ?(on)?" data-view="list" data-net="([^"]+)">/g;
  const items = Object.fromEntries([...html.matchAll(netitemRe)].map(m => [m[2], m[1] === 'on']));
  assert.equal(items.a, true);
  assert.equal(items.b, false);
});
