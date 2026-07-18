import { test } from 'node:test';
import assert from 'node:assert/strict';
import { collectPeers } from '../../web/lib/collect.js';
import { RttHistory } from '../../web/lib/rtt-history.js';
import { diffPeers } from '../../web/lib/peerdiff.js';

test('collectPeers собирает пиров из всех сетей (плоский список, порядок сохранён)', () => {
  const st = { networks: [{ peers: [{ vip: '1', name: 'A', rttMs: 10, status: 'direct' }] }, { peers: [{ vip: '2', name: 'B', rttMs: 20, status: 'relay' }] }] };
  assert.deepEqual(collectPeers(st).map(p => p.vip), ['1', '2']);
});

test('collectPeers сохраняет поля vip/name/rttMs/status для каждого пира', () => {
  const st = { networks: [{ peers: [{ vip: '1', name: 'A', rttMs: 10, status: 'direct' }] }] };
  assert.deepEqual(collectPeers(st), [{ vip: '1', name: 'A', rttMs: 10, status: 'direct' }]);
});

test('collectPeers переживает отсутствие networks/peers (не падает)', () => {
  assert.deepEqual(collectPeers({}), []);
  assert.deepEqual(collectPeers({ networks: [{}] }), []);
});

test('история наполняется из последовательных снимков', () => {
  const h = new RttHistory();
  for (const rtt of [10, 12, 11]) h.push('1', rtt);
  assert.deepEqual(h.get('1'), [10, 12, 11]);
});

test('интеграция: collectPeers + RttHistory накапливают по нескольким опросам, prune убирает выбывших', () => {
  const h = new RttHistory(40);
  const poll1 = { networks: [{ peers: [{ vip: '1', name: 'A', rttMs: 10 }, { vip: '2', name: 'B', rttMs: 20 }] }] };
  const poll2 = { networks: [{ peers: [{ vip: '1', name: 'A', rttMs: 14 }] }] };  // '2' вышел из сети
  for (const st of [poll1, poll2]) {
    const peers = collectPeers(st);
    for (const p of peers) h.push(p.vip, p.rttMs ?? -1);
    h.prune(peers.map(p => p.vip));
  }
  assert.deepEqual(h.get('1'), [10, 14]);
  assert.deepEqual(h.get('2'), []);              // pruned после того, как исчез из снимка
});

test('интеграция: diffPeers на последовательных снимках collectPeers ловит join/leave', () => {
  const poll1 = { networks: [{ peers: [{ vip: '1', name: 'A' }] }] };
  const poll2 = { networks: [{ peers: [{ vip: '1', name: 'A' }, { vip: '2', name: 'B' }] }] };
  const d = diffPeers(collectPeers(poll1), collectPeers(poll2));
  assert.deepEqual(d.joined.map(p => p.vip), ['2']);
  assert.deepEqual(d.left, []);
});
