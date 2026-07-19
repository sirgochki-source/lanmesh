import { test } from 'node:test';
import assert from 'node:assert/strict';
import { renderMap } from '../../web/views/map.js';

const peer = (name, vip, status, rttMs) => ({ name, vip, status, rttMs });

test('renderMap: возвращает inline <svg>, отзывчивый (viewBox + класс контейнера)', () => {
  const s = renderMap({ selfName: 'MY-PC', selfIP: '25.31.8.2' }, { peers: [] });
  assert.match(s, /^<svg class="svgmap" viewBox="0 0 \d+ \d+"/);
  assert.match(s, /<\/svg>$/);
});

test('renderMap: центральный узел — имя и IP себя', () => {
  const s = renderMap({ selfName: 'MY-PC', selfIP: '25.31.8.2' }, { peers: [] });
  assert.match(s, />MY-PC</);
  assert.match(s, />25\.31\.8\.2</);
});

test('renderMap: без selfName/selfIP — честные заглушки, не падает', () => {
  const s = renderMap({}, { peers: [] });
  assert.match(s, />ты</);
  assert.match(s, />—</);
});

test('renderMap: по одной <line> на пира, подписаны имена', () => {
  const net = { peers: [
    peer('Мурад', '25.44.12.9', 'direct', 18),
    peer('Kolya', '25.44.7.3', 'direct', 42),
    peer('sara_pc', '25.44.31.7', 'relay', 128),
  ] };
  const s = renderMap({ selfName: 'MY-PC', selfIP: '25.31.8.2' }, net);
  assert.equal((s.match(/<line/g) || []).length, 3);
  assert.match(s, />Мурад</);
  assert.match(s, />Kolya</);
  assert.match(s, />sara_pc</);
});

test('renderMap: direct — линия мятного акцента, без пунктира', () => {
  const s = renderMap({}, { peers: [peer('A', '1.1.1.1', 'direct', 18)] });
  const line = s.match(/<line[^>]*>/)[0];
  assert.match(line, /stroke="var\(--accent-2\)"/);
  assert.doesNotMatch(line, /stroke-dasharray/);
});

test('renderMap: relay — линия синего акцента, без пунктира', () => {
  const s = renderMap({}, { peers: [peer('A', '1.1.1.1', 'relay', 128)] });
  const line = s.match(/<line[^>]*>/)[0];
  assert.match(line, /stroke="var\(--relay\)"/);
  assert.doesNotMatch(line, /stroke-dasharray/);
});

test('renderMap: connecting — линия янтарного акцента, пунктирная', () => {
  const s = renderMap({}, { peers: [peer('A', '1.1.1.1', 'connecting', -1)] });
  const line = s.match(/<line[^>]*>/)[0];
  assert.match(line, /stroke="var\(--ping-ok\)"/);
  assert.match(line, /stroke-dasharray="[\d,]+"/);
});

test('renderMap: пинг — «N мс» для активных, «…» для connecting/rttMs<0', () => {
  const net = { peers: [peer('A', '1.1.1.1', 'direct', 18), peer('B', '2.2.2.2', 'connecting', -1)] };
  const s = renderMap({}, net);
  assert.match(s, /18 мс/);
  assert.match(s, />…</);
});

test('renderMap: пустая сеть — центр + «Пока никого», ни одной <line>', () => {
  const s = renderMap({ selfName: 'MY-PC', selfIP: '25.31.8.2' }, { peers: [] });
  assert.match(s, /Пока никого/);
  assert.equal((s.match(/<line/g) || []).length, 0);
  assert.match(s, />MY-PC</); // центр всё равно рисуется
});

test('renderMap: net без peers вовсе (undefined) — не падает, как пустая сеть', () => {
  const s = renderMap({ selfName: 'MY-PC' }, { });
  assert.match(s, /Пока никого/);
  assert.equal((s.match(/<line/g) || []).length, 0);
});

test('renderMap санитизирует враждебное имя пира и себя', () => {
  const s1 = renderMap({}, { peers: [peer('a‮gnp.exe', '1.1.1.1', 'direct', 5)] });
  assert.ok(!s1.includes('‮'));
  const s2 = renderMap({ selfName: 'a‮gnp.exe' }, { peers: [] });
  assert.ok(!s2.includes('‮'));
});
