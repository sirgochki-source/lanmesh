import { test } from 'node:test';
import assert from 'node:assert/strict';
import { renderSettings } from '../../web/views/settings.js';

test('renderSettings содержит поля сигналок, релея и диагностику', () => {
  const s = renderSettings({ running: false, sendLogs: true });
  assert.match(s, /сигналк/i);
  assert.match(s, /relay|ретранслятор/i);
  assert.match(s, /диагностик/i);
});
test('renderSettings блокирует правку серверов при поднятой сети', () => {
  const s = renderSettings({ running: true, sendLogs: true });
  assert.match(s, /disabled/);
});
