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
