// Режим обнаружения «без серверов» (DHT): значок вместо точек сигналок,
// переключатель в форме добавления сети.
import { test } from 'node:test';
import assert from 'node:assert/strict';
import { dhtBadge, netCardCompact, addFormFields, dhtToggleHtml, isDHT } from '../../web/views/list.js';
import { displayNets } from '../../web/views/shell.js';
import { parseInvite } from '../../web/lib/invite.js';

globalThis.window ??= globalThis;

const dhtNet = (dht) => ({
  name: 'секретка', tag: 'aa11', discovery: 'dht', peers: [], dht,
});

test('dhtBadge: узлы набраны — значок «живой»', () => {
  const s = dhtBadge(dhtNet({ nodes: 214, probes: 3, rounds: 5 }));
  assert.match(s, /dhtbadge up/);
  assert.match(s, />DHT</);
  assert.match(s, /узлов 214/);
});

test('dhtBadge: ноль узлов — значок «мёртвый» (DHT режут)', () => {
  const s = dhtBadge(dhtNet({ nodes: 0, rounds: 3, error: 'DHT недоступна: ни одного узла' }));
  assert.match(s, /dhtbadge down/);
  assert.match(s, /DHT недоступна/);
});

test('dhtBadge: ошибка раунда перевешивает набранные узлы', () => {
  const s = dhtBadge(dhtNet({ nodes: 50, error: 'обход не удался' }));
  assert.match(s, /dhtbadge down/);
});

test('dhtBadge: без блока dht не падает', () => {
  const s = dhtBadge({ name: 'x', discovery: 'dht' });
  assert.match(s, /dhtbadge down/);
  assert.match(s, /узлов 0/);
});

test('карточка DHT-сети показывает значок DHT и НЕ показывает точки сигналок', () => {
  const s = netCardCompact(dhtNet({ nodes: 100 }));
  assert.match(s, /dhtbadge/);
  assert.doesNotMatch(s, /sigdots/);
});

test('карточка обычной сети по-прежнему показывает точки сигналок', () => {
  const s = netCardCompact({
    name: 'myteam', tag: 'bb22', peers: [],
    signals: [{ host: 'a', up: true }, { host: 'b', up: false }],
  });
  assert.match(s, /sigdots/);
  assert.doesNotMatch(s, /dhtbadge/);
});

test('отключённая DHT-сеть остаётся помечена как DHT', () => {
  const s = netCardCompact({ name: 'секретка', tag: 'aa11', discovery: 'dht', inactive: true });
  assert.match(s, /dhtbadge/);
  assert.match(s, /отключено/);
});

test('форма добавления сети несёт переключатель «без серверов»', () => {
  const s = addFormFields();
  assert.match(s, /id="f-dht"/);
  assert.match(s, /type="checkbox"/);
  assert.match(dhtToggleHtml(), /Без серверов/);
});

test('displayNets переносит режим обнаружения в неактивную карточку', () => {
  const nets = displayNets({
    running: false,
    networks: [],
    savedNets: [{ name: 'секретка', tag: 'aa11', discovery: 'dht' }],
  });
  assert.equal(nets.length, 1);
  assert.equal(nets[0].discovery, 'dht');
  assert.equal(nets[0].inactive, true);
});

test('приглашение несёт режим обнаружения', () => {
  const inv = parseInvite('lanmesh://join?net=%D1%81%D0%B5%D0%BA%D1%80%D0%B5%D1%82%D0%BA%D0%B0&pass=123&disc=dht');
  assert.equal(inv.disc, 'dht');
  assert.equal(inv.net, 'секретка');
});

test('приглашение обычной сети — режим signal, а не null-догадка', () => {
  assert.equal(parseInvite('lanmesh://join?net=a&pass=b&disc=signal').disc, 'signal');
  // Неизвестное значение трактуем как обычную сеть, а не как DHT.
  assert.equal(parseInvite('lanmesh://join?net=a&pass=b&disc=hz').disc, 'signal');
});

test('старое приглашение без disc — режим не задан (галка остаётся за пользователем)', () => {
  assert.equal(parseInvite('lanmesh://join?net=a&pass=b&sig=https://x/&relay=').disc, null);
});

test('режим dht+relay тоже считается DHT-сетью', () => {
  assert.equal(isDHT({ discovery: 'dht+relay' }), true);
  assert.equal(isDHT({ discovery: 'dht' }), true);
  assert.equal(isDHT({ discovery: 'signal' }), false);
  assert.equal(isDHT({}), false);
  const s = netCardCompact({ name: 'x', tag: 'cc33', discovery: 'dht+relay', peers: [], dht: { nodes: 9 } });
  assert.match(s, /dhtbadge up/);
  assert.match(s, /ретранслятор разрешён/);
});

test('чистая DHT-сеть в подсказке заявляет полное отсутствие серверов', () => {
  assert.match(dhtBadge({ discovery: 'dht', dht: { nodes: 9 } }), /без серверов совсем/);
});

test('форма несёт обе галки, вторая изначально скрыта', () => {
  const s = addFormFields();
  assert.match(s, /id="f-dht"/);
  assert.match(s, /id="f-dht-relay"/);
  assert.match(s, /<label class="chk dhtopt" hidden title=/);  // вторая галка скрыта до выбора DHT
});

test('приглашение переносит режим с релеем', () => {
  assert.equal(parseInvite('lanmesh://join?net=a&pass=b&disc=dht%2Brelay&relay=h:1').disc, 'dht+relay');
  assert.equal(parseInvite('lanmesh://join?net=a&pass=b&disc=dht%2Brelay&relay=h:1').relay, 'h:1');
});
