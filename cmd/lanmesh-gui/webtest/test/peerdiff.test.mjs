import { test } from 'node:test';
import assert from 'node:assert/strict';
import { diffPeers } from '../../web/lib/peerdiff.js';

test('diffPeers находит вход и выход', () => {
  const prev = [{ vip: '1', name: 'A' }, { vip: '2', name: 'B' }];
  const next = [{ vip: '2', name: 'B' }, { vip: '3', name: 'C' }];
  const d = diffPeers(prev, next);
  assert.deepEqual(d.joined, [{ vip: '3', name: 'C' }]);
  assert.deepEqual(d.left, [{ vip: '1', name: 'A' }]);
});

test('нет изменений — пустые списки', () => {
  const a = [{ vip: '1', name: 'A' }];
  const d = diffPeers(a, a);
  assert.deepEqual(d, { joined: [], left: [] });
});

test('идентичность по vip, не по name', () => {
  const prev = [{ vip: '1', name: 'Alice' }];
  const next = [{ vip: '1', name: 'AliceRenamed' }];
  const d = diffPeers(prev, next);
  assert.deepEqual(d.joined, []);
  assert.deepEqual(d.left, []);
});

test('пустой prev — все в joined', () => {
  const prev = [];
  const next = [{ vip: '1', name: 'A' }, { vip: '2', name: 'B' }];
  const d = diffPeers(prev, next);
  assert.deepEqual(d.joined, [{ vip: '1', name: 'A' }, { vip: '2', name: 'B' }]);
  assert.deepEqual(d.left, []);
});
