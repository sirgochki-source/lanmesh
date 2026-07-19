import { test } from 'node:test';
import assert from 'node:assert/strict';
import { renderSettings, renderSettingsCompact, settingsBody, srvRow } from '../../web/views/settings.js';
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

test('settingsBody показывает добавленные сигналки строками и предзаполняет relay', () => {
  const s = settingsBody({ running: false, cfgSignals: ['https://a.example', 'https://b.example'], cfgRelay: 'r.example:25555' });
  assert.match(s, /value="https:\/\/a\.example"/);
  assert.match(s, /value="https:\/\/b\.example"/);
  assert.match(s, /value="r\.example:25555"/);
  assert.equal((s.match(/class="s-sig"/g) || []).length, 2);
  assert.match(s, /data-act="sig-add"/);   // добавить
  assert.match(s, /data-act="sig-del"/);   // удалить у строки
});
test('settingsBody: без cfgSignals — одна пустая строка для ввода', () => {
  assert.equal((settingsBody({ running: false }).match(/class="s-sig"/g) || []).length, 1);
});
test('srvRow: адрес-инпут + кнопка удаления, значение экранируется', () => {
  const s = srvRow('https://x"><b>');
  assert.match(s, /class="s-sig"/);
  assert.match(s, /data-act="sig-del"/);
  assert.ok(!s.includes('<b>'));
});
test('settingsBody блокирует правку при поднятой сети', () => {
  assert.match(settingsBody({ running: true, cfgSignals: ['https://a'] }), /class="s-sig"[^>]*disabled/);
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
