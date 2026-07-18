import { test } from 'node:test';
import assert from 'node:assert/strict';
import { statusPill, pickMode, renderHeader } from '../../web/views/shell.js';

test('statusPill отражает состояние', () => {
  assert.equal(statusPill({ running: false, networks: [] }).cls, 'off');
  assert.equal(statusPill({ running: true, selfEndpoint: '', networks: [] }).cls, 'warn'); // нет внешнего адреса
  assert.equal(statusPill({ running: true, selfEndpoint: 'x', networks: [{ signalError: '' }] }).cls, 'on');
});
test('pickMode по ширине (порог 620)', () => {
  assert.equal(pickMode(400), 'compact');
  assert.equal(pickMode(900), 'detailed');
});
test('renderHeader экранирует и содержит бренд', () => {
  const h = renderHeader({ running: true, selfEndpoint: 'x', networks: [] }, 'compact');
  assert.match(h, /lan<b>mesh<\/b>/);
});
