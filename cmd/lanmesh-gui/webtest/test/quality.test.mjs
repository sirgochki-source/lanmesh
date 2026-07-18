import { test } from 'node:test';
import assert from 'node:assert/strict';
import { quality } from '../../web/lib/quality.js';

test('connecting', () => assert.equal(quality('connecting', -1, []).level, 'connecting'));
test('direct низкий стабильный пинг → good', () =>
  assert.equal(quality('direct', 18, [17, 18, 19, 18]).level, 'good'));
test('direct низкий, но дёрганый пинг → ok', () =>
  assert.equal(quality('direct', 30, [5, 80, 10, 70]).level, 'ok'));   // высокий разброс
test('direct высокий пинг → bad', () =>
  assert.equal(quality('direct', 200, [200, 205]).level, 'bad'));
test('relay никогда не good', () =>
  assert.equal(quality('relay', 20, [20, 20]).level, 'ok'));

// Additional test cases beyond the brief
test('relay с высоким rtt → bad', () =>
  assert.equal(quality('relay', 200, []).level, 'bad'));
test('direct средний rtt со стабильностью → ok', () =>
  assert.equal(quality('direct', 100, [100, 101, 99]).level, 'ok'));
test('negative rttMs → connecting', () =>
  assert.equal(quality('direct', -1, []).level, 'connecting'));
