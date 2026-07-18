import { test } from 'node:test';
import assert from 'node:assert/strict';
import { RttHistory } from '../../web/lib/rtt-history.js';

test('push/get кольцевого буфера', () => {
  const h = new RttHistory(3);
  h.push('a', 10); h.push('a', 20); h.push('a', 30); h.push('a', 40);
  assert.deepEqual(h.get('a'), [20, 30, 40]);   // ёмкость 3, старое вытеснено
  assert.deepEqual(h.get('zzz'), []);
});
test('push игнорирует отрицательный rtt', () => {
  const h = new RttHistory(); h.push('a', -1); h.push('a', 5);
  assert.deepEqual(h.get('a'), [5]);
});
test('prune убирает неактивных', () => {
  const h = new RttHistory(); h.push('a', 5); h.push('b', 5);
  h.prune(['a']);
  assert.deepEqual(h.get('b'), []);
  assert.deepEqual(h.get('a'), [5]);
});
test('get возвращает копию, не мутирующую внутреннее состояние', () => {
  const h = new RttHistory();
  h.push('a', 10);
  const arr1 = h.get('a');
  arr1.push(999);  // мутируем полученный массив
  const arr2 = h.get('a');
  assert.deepEqual(arr2, [10]);  // внутреннее состояние не изменилось
});
