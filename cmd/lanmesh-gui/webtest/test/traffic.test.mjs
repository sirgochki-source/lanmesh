import { test } from 'node:test';
import assert from 'node:assert/strict';
import { fmtBytes } from '../../web/lib/format.js';
import { computeRates } from '../../web/lib/traffic.js';
import { renderTraffic, netTrafficTotals } from '../../web/views/traffic.js';

test('fmtBytes: байты — целое число, без дробной части', () => {
  assert.equal(fmtBytes(0), '0 Б');
  assert.equal(fmtBytes(500), '500 Б');
  assert.equal(fmtBytes(1023), '1023 Б');
});
test('fmtBytes: КБ — порог на 1024, один знак после запятой', () => {
  assert.equal(fmtBytes(1024), '1.0 КБ');
  assert.equal(fmtBytes(1536), '1.5 КБ');
  assert.equal(fmtBytes(1024 * 1023), '1023.0 КБ');
});
test('fmtBytes: МБ', () => {
  assert.equal(fmtBytes(1024 * 1024), '1.0 МБ');
  assert.equal(fmtBytes(3.4 * 1024 * 1024), '3.4 МБ');
});
test('fmtBytes: ГБ', () => {
  assert.equal(fmtBytes(2.1 * 1024 * 1024 * 1024), '2.1 ГБ');
});

test('computeRates: дельта/dt — корректный расчёт скорости', () => {
  const prev = { '1.1.1.1': { rx: 1000, tx: 500 } };
  const cur = { '1.1.1.1': { rx: 3000, tx: 1500 } };
  assert.deepEqual(computeRates(prev, cur, 2), { '1.1.1.1': { rxRate: 1000, txRate: 500 } });
});
test('computeRates: отрицательная дельта (счётчик "уменьшился", напр. рестарт узла) — зажим в 0', () => {
  const prev = { '1.1.1.1': { rx: 5000, tx: 5000 } };
  const cur = { '1.1.1.1': { rx: 1000, tx: 1000 } };
  assert.deepEqual(computeRates(prev, cur, 1), { '1.1.1.1': { rxRate: 0, txRate: 0 } });
});
test('computeRates: dt<=0 — скорость 0 для всех vip из cur', () => {
  const prev = { '1.1.1.1': { rx: 1000, tx: 1000 } };
  const cur = { '1.1.1.1': { rx: 2000, tx: 2000 } };
  assert.deepEqual(computeRates(prev, cur, 0), { '1.1.1.1': { rxRate: 0, txRate: 0 } });
  assert.deepEqual(computeRates(prev, cur, -1), { '1.1.1.1': { rxRate: 0, txRate: 0 } });
});
test('computeRates: нет prev для vip (новый пир/первый снимок) — скорость 0', () => {
  const cur = { '2.2.2.2': { rx: 5000, tx: 2000 } };
  assert.deepEqual(computeRates({}, cur, 1), { '2.2.2.2': { rxRate: 0, txRate: 0 } });
  assert.deepEqual(computeRates(undefined, cur, 1), { '2.2.2.2': { rxRate: 0, txRate: 0 } });
});
test('computeRates: в результате только vip из cur (вышедшие пиры не попадают)', () => {
  const prev = { '1.1.1.1': { rx: 100, tx: 100 }, '9.9.9.9': { rx: 999, tx: 999 } };
  const cur = { '1.1.1.1': { rx: 200, tx: 150 } };
  assert.deepEqual(Object.keys(computeRates(prev, cur, 1)), ['1.1.1.1']);
});

test('renderTraffic: шапка — суммарный накопленный трафик и текущая скорость сети', () => {
  const net = {
    name: 'myteam', tag: 't',
    peers: [{ name: 'Мурад', vip: '25.44.12.9', status: 'direct', rttMs: 18, bytesRx: 2048, bytesTx: 1024 }],
  };
  const s = renderTraffic(net, { '25.44.12.9': { rxRate: 512, txRate: 256 } });
  assert.match(s, /3\.0 КБ/);      // 2048+1024 = 3072 Б = 3.0 КБ — суммарный тотал в шапке
  assert.match(s, /768 Б\/с/);     // 512+256 = 768 Б/с — суммарная текущая скорость
});
test('renderTraffic: строка пира — имя (через dispName), накопленное ↓/↑ и текущая скорость', () => {
  const net = {
    name: 'myteam', tag: 't',
    peers: [{ name: 'Мурад', vip: '25.44.12.9', status: 'direct', rttMs: 18, bytesRx: 2048, bytesTx: 1024 }],
  };
  const s = renderTraffic(net, { '25.44.12.9': { rxRate: 512, txRate: 256 } });
  assert.match(s, /Мурад/);
  assert.match(s, /2\.0 КБ/);      // накопленный RX пира
  assert.match(s, /1\.0 КБ/);      // накопленный TX пира
});
test('renderTraffic санитизирует враждебное имя пира', () => {
  const net = { name: 'n', tag: 't', peers: [{ name: 'a‮gnp.exe', vip: '1', status: 'direct', rttMs: 5, bytesRx: 10, bytesTx: 10 }] };
  const s = renderTraffic(net, {});
  assert.ok(!s.includes('‮'));
});
test('renderTraffic: сеть без пиров — не падает, честный пустой вид', () => {
  const s = renderTraffic({ name: 'myteam', tag: 't', peers: [] }, {});
  assert.match(s, /Пока никого/);
});
test('renderTraffic: без снимка rates (undefined) — не падает, скорость 0', () => {
  const net = { name: 'n', tag: 't', peers: [{ name: 'A', vip: '1', status: 'direct', rttMs: 5, bytesRx: 10, bytesTx: 10 }] };
  assert.doesNotThrow(() => renderTraffic(net));
});

test('netTrafficTotals: суммирует rx/tx/rate по всем пирам сети', () => {
  const net = {
    peers: [
      { vip: 'a', bytesRx: 100, bytesTx: 50 },
      { vip: 'b', bytesRx: 200, bytesTx: 25 },
    ],
  };
  const t = netTrafficTotals(net, { a: { rxRate: 10, txRate: 5 }, b: { rxRate: 20, txRate: 0 } });
  assert.deepEqual(t, { rx: 300, tx: 75, rate: 35 });
});
