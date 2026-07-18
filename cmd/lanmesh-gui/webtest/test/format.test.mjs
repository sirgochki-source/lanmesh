import { test } from 'node:test';
import assert from 'node:assert/strict';
import { fmtRtt, rttClass, fmtUptime, fmtSeen, plural } from '../../web/lib/format.js';

test('fmtRtt', () => {
  assert.equal(fmtRtt(-1), null);
  assert.equal(fmtRtt(3.14), '3.1 мс');
  assert.equal(fmtRtt(42.6), '43 мс');
});
test('rttClass по порогам 60/150', () => {
  assert.equal(rttClass(18), 'rtt-good');
  assert.equal(rttClass(60), 'rtt-ok');
  assert.equal(rttClass(150), 'rtt-bad');
});
test('fmtUptime', () => {
  assert.equal(fmtUptime(45), '45 с');
  assert.equal(fmtUptime(8040), '2 ч 14 м');
});
test('plural (русское склонение)', () => {
  assert.equal(plural(1, 'сеть', 'сети', 'сетей'), 'сеть');
  assert.equal(plural(3, 'сеть', 'сети', 'сетей'), 'сети');
  assert.equal(plural(5, 'сеть', 'сети', 'сетей'), 'сетей');
});
