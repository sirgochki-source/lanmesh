import { test } from 'node:test';
import assert from 'node:assert/strict';
import { statusPill, pickMode, renderHeader, renderRail, connBtn, displayNets, THEMES, themeBtn, renderThemePopover } from '../../web/views/shell.js';

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

test('renderHeader: ⚙ активна (класс on) и подпись «Назад к списку» когда открыты настройки', () => {
  const h = renderHeader({ running: true, selfEndpoint: 'x', networks: [] }, 'compact', 'settings');
  assert.match(h, /iconbtn gear on/);
  assert.match(h, /Назад к списку/);
});
test('renderRail: содержит пункт «＋ Добавить сеть» (data-view=add)', () => {
  const html = renderRail({ networks: [{ tag: 'a', name: 'A', peers: [] }] }, 'list', 'a');
  assert.match(html, /data-view="add"/);
  assert.match(html, /Добавить сеть/);
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

test('THEMES: ровно 9 тем с уникальными id', () => {
  assert.equal(THEMES.length, 9);
  assert.equal(new Set(THEMES.map(t => t.id)).size, 9);
});
test('themeBtn: кнопка-палитра (data-act=theme-menu, точка текущего акцента)', () => {
  const s = themeBtn();
  assert.match(s, /data-act="theme-menu"/);
  assert.match(s, /theme-cur/);
});
test('renderThemePopover: 9 фишек, активная помечена .on', () => {
  const s = renderThemePopover('ocean');
  assert.equal((s.match(/class="theme-chip/g) || []).length, 9);
  assert.match(s, /theme-chip on" data-theme="ocean"/);
  assert.match(s, /data-theme="mint"/);
  assert.match(s, /data-theme="night"/);
});
test('renderHeader: содержит кнопку выбора темы (оба режима)', () => {
  assert.match(renderHeader({ running: true, selfEndpoint: 'x', networks: [] }, 'compact'), /data-act="theme-menu"/);
  assert.match(renderHeader({ running: true, selfEndpoint: 'x', networks: [] }, 'detailed'), /data-act="theme-menu"/);
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
