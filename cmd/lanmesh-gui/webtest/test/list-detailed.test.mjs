import { test } from 'node:test';
import assert from 'node:assert/strict';
import { peerRowDetailed, renderDetailed } from '../../web/views/list.js';

test('peerRowDetailed: аватар-инициал, бейдж статуса, пинг, vip', () => {
  const s = peerRowDetailed({ name: 'Мурад', vip: '25.44.12.9', status: 'direct', rttMs: 18, signals: [true] }, []);
  assert.match(s, /badge direct/);
  assert.match(s, /25\.44\.12\.9/);
  assert.match(s, />М</);                         // инициал аватара
  assert.match(s, /18 мс/);
});

test('peerRowDetailed: IP кликабелен для копирования (как в компактном режиме)', () => {
  const s = peerRowDetailed({ name: 'X', vip: '25.44.1.2', status: 'direct', rttMs: 10 }, []);
  assert.match(s, /class="ip mono copyable"/);
  assert.match(s, /data-copy="25\.44\.1\.2"/);
  assert.match(s, /title="скопировать IP"/);
});

test('peerRowDetailed: relay/connecting бейджи', () => {
  const relay = peerRowDetailed({ name: 'sara_pc', vip: '25.44.31.7', status: 'relay', rttMs: 128 }, []);
  assert.match(relay, /badge relay/);
  const conn = peerRowDetailed({ name: 'Dev_null', vip: '25.44.9.1', status: 'connecting', rttMs: -1 }, []);
  assert.match(conn, /badge conn/);
});

test('peerRowDetailed санитизирует враждебное имя', () => {
  const s = peerRowDetailed({ name: 'a‮gnp.exe', vip: '1', status: 'direct', rttMs: 5 }, []);
  assert.ok(!s.includes('‮'));
});

test('peerRowDetailed экранирует peer.status в бейдже (класс и текст, defense-in-depth)', () => {
  const s = peerRowDetailed({ name: 'X', vip: '1', status: 'direct"><script>1</script>', rttMs: 5 }, []);
  assert.ok(!s.includes('<script>'));
  assert.match(s, /badge direct&quot;&gt;&lt;script&gt;1&lt;\/script&gt;/);
});

test('renderDetailed map/traffic — заглушки «скоро»', () => {
  const st = { running: true, selfEndpoint: 'x', networks: [{ name: 'n', tag: 't', signals: [], peers: [] }] };
  assert.match(renderDetailed(st, 'map', {}), /скоро/i);
  assert.match(renderDetailed(st, 'traffic', {}), /скоро/i);
});

test('renderDetailed settings — тоже пока заглушка (реальный контент в Task 13)', () => {
  const st = { running: true, selfEndpoint: 'x', networks: [{ name: 'n', tag: 't', signals: [], peers: [] }] };
  assert.match(renderDetailed(st, 'settings', {}), /скоро/i);
});

test('renderDetailed list содержит плитку качества', () => {
  const st = {
    running: true, selfEndpoint: 'x', networks: [{
      name: 'n', tag: 't', signals: [],
      peers: [{ name: 'A', vip: '1', status: 'direct', rttMs: 18, signals: [true] }],
    }],
  };
  assert.match(renderDetailed(st, 'list', {}), /Качество/);
});

test('renderDetailed list: плитка «Трафик» — заглушка без выдуманных цифр', () => {
  const st = { running: true, selfEndpoint: 'x', networks: [{ name: 'n', tag: 't', signals: [], peers: [] }] };
  const s = renderDetailed(st, 'list', {});
  assert.match(s, /Phase 3/);
});

test('renderDetailed list: без активных сетей — подсказка, не падает', () => {
  const st = { running: true, selfEndpoint: 'x', networks: [] };
  assert.match(renderDetailed(st, 'list', {}), /Нет активных сетей/);
});

test('renderDetailed list: заголовок сети склоняет «участников» по числу', () => {
  const p = (i) => ({ name: 'p' + i, vip: '1.1.1.' + i, status: 'direct', rttMs: 10 });
  const st1 = { running: true, selfEndpoint: 'x', networks: [{ name: 'n', tag: 't', signals: [], peers: [p(1)] }] };
  const st4 = { running: true, selfEndpoint: 'x', networks: [{ name: 'n', tag: 't', signals: [], peers: [p(1), p(2), p(3), p(4)] }] };
  assert.match(renderDetailed(st1, 'list', {}), /1 участник</);
  assert.match(renderDetailed(st4, 'list', {}), /4 участника</);
});

test('renderDetailed: выбирает сеть по activeNetTag (переключение сетей)', () => {
  const st = {
    running: true, selfEndpoint: 'x', networks: [
      { name: 'Первая', tag: 'net-a', signals: [], peers: [] },
      { name: 'Вторая', tag: 'net-b', signals: [], peers: [] },
    ],
  };
  const a = renderDetailed(st, 'list', {}, 'net-a');
  assert.match(a, /Первая/);
  assert.doesNotMatch(a, /Вторая/);
  const b = renderDetailed(st, 'list', {}, 'net-b');
  assert.match(b, /Вторая/);
  assert.doesNotMatch(b, /Первая/);
});

test('renderDetailed: activeNetTag без совпадения (или не задан) — берёт первую сеть', () => {
  const st = {
    running: true, selfEndpoint: 'x', networks: [
      { name: 'Первая', tag: 'net-a', signals: [], peers: [] },
      { name: 'Вторая', tag: 'net-b', signals: [], peers: [] },
    ],
  };
  assert.match(renderDetailed(st, 'list', {}), /Первая/);
  assert.match(renderDetailed(st, 'list', {}, 'no-such-tag'), /Первая/);
});

test('renderDetailed: числовая сортировка по vip (9.1 перед 12.9, не лексикографически)', () => {
  const st = {
    running: true, selfEndpoint: 'x', networks: [{
      name: 'n', tag: 't', signals: [], peers: [
        { name: 'A', vip: '25.44.12.9', status: 'direct', rttMs: 10 },
        { name: 'B', vip: '25.44.9.1', status: 'direct', rttMs: 10 },
      ],
    }],
  };
  const s = renderDetailed(st, 'list', {});
  assert.ok(s.indexOf('25.44.9.1') < s.indexOf('25.44.12.9'), 'ожидали 9.1 раньше 12.9 (числовой порядок)');
});
