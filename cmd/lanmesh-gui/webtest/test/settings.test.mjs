import { test } from 'node:test';
import assert from 'node:assert/strict';
import { renderSettings, renderSettingsCompact } from '../../web/views/settings.js';
import { renderCompact } from '../../web/views/list.js';
import { renderHeader } from '../../web/views/shell.js';

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

test('renderSettingsCompact — те же настройки + кнопка «назад к списку»', () => {
  const s = renderSettingsCompact({ running: false, sendLogs: true });
  assert.match(s, /сигналк/i);
  assert.match(s, /диагностик/i);
  assert.match(s, /data-act="show-list"/);
});

test('компакт: ⚙ в шапке ведёт в настройки (data-act="show-settings")', () => {
  const h = renderHeader({ running: true, selfEndpoint: 'x', networks: [] }, 'compact');
  assert.match(h, /data-act="show-settings"/);
  // в detailed шапке шестерёнки нет — там настройки в рейле
  const d = renderHeader({ running: true, selfEndpoint: 'x', networks: [] }, 'detailed');
  assert.doesNotMatch(d, /show-settings/);
});

test('компакт: view="settings" рендерит экран настроек, а не список', () => {
  const st = { running: false, networks: [] };
  const s = renderCompact(st, 'settings');
  assert.match(s, /data-act="show-list"/);
  assert.match(s, /сигналк/i);
  // а по умолчанию — список (форма добавления сети)
  assert.match(renderCompact(st, 'list'), /Добавить сеть/);
});
