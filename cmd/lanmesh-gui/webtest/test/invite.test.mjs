import { test } from 'node:test';
import assert from 'node:assert/strict';
import { parseInvite } from '../../web/lib/invite.js';

test('parseInvite разбирает net/pass/sig/relay', () => {
  const r = parseInvite('lanmesh://join?net=my+team&pass=p%40ss&sig=https://s1&sig=https://s2&relay=r:1');
  assert.equal(r.net, 'my team');
  assert.equal(r.pass, 'p@ss');
  assert.deepEqual(r.sigs, ['https://s1', 'https://s2']);
  assert.equal(r.relay, 'r:1');
});

test('parseInvite не роняется на битой %-последовательности', () => {
  const r = parseInvite('net=ok&pass=%');
  assert.equal(r.net, 'ok');            // битое поле pass просто пропущено
});

test('parseInvite разбирает query без префикса ?', () => {
  const r = parseInvite('net=x&pass=y');
  assert.equal(r.net, 'x');
  assert.equal(r.pass, 'y');
  assert.deepEqual(r.sigs, []);
  assert.equal(r.relay, null);
});

test('parseInvite устанавливает дефолты для пропущенных полей', () => {
  const r = parseInvite('net=only');
  assert.equal(r.net, 'only');
  assert.equal(r.pass, null);
  assert.deepEqual(r.sigs, []);
  assert.equal(r.relay, null);
});
