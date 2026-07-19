import { test } from 'node:test';
import assert from 'node:assert/strict';
import { peerRowDetailed, renderDetailed, peerSignalDots } from '../../web/views/list.js';

test('peerRowDetailed: аватар-инициал, бейдж статуса, пинг, vip', () => {
  const s = peerRowDetailed({ name: 'Мурад', vip: '25.44.12.9', status: 'direct', rttMs: 18, signals: [true] }, []);
  assert.match(s, /badge direct/);
  assert.match(s, /25\.44\.12\.9/);
  assert.match(s, />М</);                         // инициал аватара
  assert.match(s, /18 мс/);
});

test('peerRowDetailed: IP кликабелен для копирования (как в компактном режиме)', () => {
  const s = peerRowDetailed({ name: 'X', vip: '25.44.1.2', status: 'direct', rttMs: 10 }, []);
  assert.match(s, /class="ip mono copyable"/);
  assert.match(s, /data-copy="25\.44\.1\.2"/);
  assert.match(s, /title="скопировать IP"/);
});

test('peerRowDetailed: relay/connecting бейджи', () => {
  const relay = peerRowDetailed({ name: 'sara_pc', vip: '25.44.31.7', status: 'relay', rttMs: 128 }, []);
  assert.match(relay, /badge relay/);
  const conn = peerRowDetailed({ name: 'Dev_null', vip: '25.44.9.1', status: 'connecting', rttMs: -1 }, []);
  assert.match(conn, /badge conn/);
});

test('peerRowDetailed санитизирует враждебное имя', () => {
  const s = peerRowDetailed({ name: 'a‮gnp.exe', vip: '1', status: 'direct', rttMs: 5 }, []);
  assert.ok(!s.includes('‮'));
});

test('peerRowDetailed экранирует peer.status в бейдже (класс и текст, defense-in-depth)', () => {
  const s = peerRowDetailed({ name: 'X', vip: '1', status: 'direct"><script>1</script>', rttMs: 5 }, []);
  assert.ok(!s.includes('<script>'));
  assert.match(s, /badge direct&quot;&gt;&lt;script&gt;1&lt;\/script&gt;/);
});

test('peerSignalDots: зелёная если пир виден через сервер, серая если нет, хост в подсказке', () => {
  const s = peerSignalDots([true, false], [{ host: 'eu-1', up: true }, { host: 'us-1', up: false }]);
  assert.match(s, /class="sig up"/);
  assert.match(s, /class="sig off"/);       // «не виден» — нейтральный серый, не красный
  assert.doesNotMatch(s, /class="sig down"/);
  assert.match(s, /eu-1: виден/);
  assert.match(s, /us-1: не виден/);
});
test('peerSignalDots: пустой список — ничего не рисуем', () => {
  assert.equal(peerSignalDots([], []), '');
  assert.equal(peerSignalDots(undefined), '');
});
test('peerRowDetailed: показывает точки сигналок пира по net.signals', () => {
  const s = peerRowDetailed({ name: 'A', vip: '1', status: 'direct', rttMs: 10, signals: [true, false] }, [], [{ host: 'eu-1' }, { host: 'us-1' }]);
  assert.match(s, /class="sig up"/);
  assert.match(s, /class="sig off"/);
  assert.match(s, /eu-1: виден/);
});

test('renderDetailed traffic — делегирует в renderTraffic (Phase 3, больше не заглушка)', () => {
  const st = {
    running: true, selfEndpoint: 'x', networks: [{
      name: 'n', tag: 't', signals: [],
      peers: [{ name: 'A', vip: '1.1.1.1', status: 'direct', rttMs: 10, bytesRx: 2048, bytesTx: 1024 }],
    }],
  };
  const s = renderDetailed(st, 'traffic', {}, undefined, { '1.1.1.1': { rxRate: 100, txRate: 50 } });
  // "Скорость" (плитка) сама по себе содержит подстроку "скоро" — сверяемся с точной фразой
  // старой заглушки (soon()), а не с расплывчатым /скоро/i.
  assert.doesNotMatch(s, /Трафик — скоро/i);
  assert.match(s, />A</);
});

test('renderDetailed: без сетей — форма добавления (а не заглушка), не падает', () => {
  const st = { running: true, selfEndpoint: 'x', networks: [] };
  assert.match(renderDetailed(st, 'traffic', {}), /id="f-net"/);
});

test('renderDetailed: вид add — форма добавления сети (имя, пароль, кнопка)', () => {
  const st = { running: true, selfEndpoint: 'x', networks: [{ name: 'n', tag: 't', signals: [], peers: [] }] };
  const s = renderDetailed(st, 'add', {});
  assert.match(s, /id="f-net"/);
  assert.match(s, /id="f-pass"/);
  assert.match(s, /Добавить сеть/);
});

test('renderDetailed settings — делегирует в renderSettings (Task 13)', () => {
  const st = { running: true, selfEndpoint: 'x', networks: [{ name: 'n', tag: 't', signals: [], peers: [] }] };
  const s = renderDetailed(st, 'settings', {});
  assert.match(s, /сигналк/i);
  assert.doesNotMatch(s, /скоро/i);
});

test('renderDetailed: в шапке сети есть кнопки Пригласить и Выйти', () => {
  const st = { running: true, selfEndpoint: 'x', networks: [{ name: 'n', tag: 't', signals: [], peers: [] }] };
  const s = renderDetailed(st, 'list', {});
  assert.match(s, /data-invite="t"/);
  assert.match(s, /data-leave="t"/);
});
test('renderDetailed: неактивную сеть тоже можно «Выйти» (забыть)', () => {
  const s = renderDetailed({ running: false, networks: [], savedNets: [{ tag: 't', name: 'myteam' }] }, 'list', {}, 't');
  assert.match(s, /data-leave="t"/);
});

test('renderDetailed list содержит плитку качества', () => {
  const st = {
    running: true, selfEndpoint: 'x', networks: [{
      name: 'n', tag: 't', signals: [],
      peers: [{ name: 'A', vip: '1', status: 'direct', rttMs: 18, signals: [true] }],
    }],
  };
  assert.match(renderDetailed(st, 'list', {}), /Качество/);
});

test('renderDetailed list: строка сигнальных серверов с подписями и статусом', () => {
  const st = {
    running: true, selfEndpoint: 'x', networks: [{
      name: 'n', tag: 't', signals: [{ host: 'eu-1', up: true }, { host: 'us-1', up: false }],
      peers: [{ name: 'A', vip: '1', status: 'direct', rttMs: 18, signals: [true, false] }],
    }],
  };
  const s = renderDetailed(st, 'list', {});
  assert.match(s, /class="sigdots labeled"/);
  assert.match(s, /class="sig-item up"/);
  assert.match(s, /class="sig-item down"/);
  assert.match(s, />eu-1</);
  assert.match(s, />us-1</);
});

test('renderDetailed list: плитка «Трафик» показывает реальный накопленный трафик и текущую скорость', () => {
  const st = {
    running: true, selfEndpoint: 'x', networks: [{
      name: 'n', tag: 't', signals: [],
      peers: [{ name: 'A', vip: '1.1.1.1', status: 'direct', rttMs: 10, bytesRx: 2048, bytesTx: 0 }],
    }],
  };
  const s = renderDetailed(st, 'list', {}, undefined, { '1.1.1.1': { rxRate: 512, txRate: 0 } });
  assert.doesNotMatch(s, /Phase 3/);
  assert.match(s, /2\.0 КБ/);   // 2048 rx + 0 tx = 2048 Б = 2.0 КБ, накопленный тотал плитки
});
test('renderDetailed list: плитка «Трафик» без данных — честный ноль, не заглушка', () => {
  const st = { running: true, selfEndpoint: 'x', networks: [{ name: 'n', tag: 't', signals: [], peers: [] }] };
  const s = renderDetailed(st, 'list', {});
  assert.doesNotMatch(s, /Phase 3/);
  assert.match(s, /0 Б/);
});

test('renderDetailed list: без сетей — сразу форма добавления', () => {
  const st = { running: true, selfEndpoint: 'x', networks: [] };
  assert.match(renderDetailed(st, 'list', {}), /id="f-net"/);
});

test('renderDetailed: неактивная (отключённая) сеть — серый заголовок + подсказка подключиться', () => {
  const st = { running: false, networks: [], savedNets: [{ tag: 't', name: 'myteam' }] };
  const s = renderDetailed(st, 'list', {}, 't');
  assert.match(s, /myteam/);
  assert.match(s, /отключено/);
  assert.match(s, /Подключиться/);
});

test('renderDetailed list: заголовок сети склоняет «участников» по числу', () => {
  const p = (i) => ({ name: 'p' + i, vip: '1.1.1.' + i, status: 'direct', rttMs: 10 });
  const st1 = { running: true, selfEndpoint: 'x', networks: [{ name: 'n', tag: 't', signals: [], peers: [p(1)] }] };
  const st4 = { running: true, selfEndpoint: 'x', networks: [{ name: 'n', tag: 't', signals: [], peers: [p(1), p(2), p(3), p(4)] }] };
  assert.match(renderDetailed(st1, 'list', {}), /1 участник</);
  assert.match(renderDetailed(st4, 'list', {}), /4 участника</);
});

test('renderDetailed: выбирает сеть по activeNetTag (переключение сетей)', () => {
  const st = {
    running: true, selfEndpoint: 'x', networks: [
      { name: 'Первая', tag: 'net-a', signals: [], peers: [] },
      { name: 'Вторая', tag: 'net-b', signals: [], peers: [] },
    ],
  };
  const a = renderDetailed(st, 'list', {}, 'net-a');
  assert.match(a, /Первая/);
  assert.doesNotMatch(a, /Вторая/);
  const b = renderDetailed(st, 'list', {}, 'net-b');
  assert.match(b, /Вторая/);
  assert.doesNotMatch(b, /Первая/);
});

test('renderDetailed: activeNetTag без совпадения (или не задан) — берёт первую сеть', () => {
  const st = {
    running: true, selfEndpoint: 'x', networks: [
      { name: 'Первая', tag: 'net-a', signals: [], peers: [] },
      { name: 'Вторая', tag: 'net-b', signals: [], peers: [] },
    ],
  };
  assert.match(renderDetailed(st, 'list', {}), /Первая/);
  assert.match(renderDetailed(st, 'list', {}, 'no-such-tag'), /Первая/);
});

test('renderDetailed: числовая сортировка по vip (9.1 перед 12.9, не лексикографически)', () => {
  const st = {
    running: true, selfEndpoint: 'x', networks: [{
      name: 'n', tag: 't', signals: [], peers: [
        { name: 'A', vip: '25.44.12.9', status: 'direct', rttMs: 10 },
        { name: 'B', vip: '25.44.9.1', status: 'direct', rttMs: 10 },
      ],
    }],
  };
  const s = renderDetailed(st, 'list', {});
  assert.ok(s.indexOf('25.44.9.1') < s.indexOf('25.44.12.9'), 'ожидали 9.1 раньше 12.9 (числовой порядок)');
});
