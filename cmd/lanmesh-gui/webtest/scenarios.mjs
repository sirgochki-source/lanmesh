// Фейковые снимки /api/state для проверки всех состояний UI.
const peer = (name, vip, status, rttMs, signals = [true]) => ({
  name, vip, status, endpoint: status === 'direct' ? '203.0.113.7:41234' : '',
  lastSeenMs: status === 'connecting' ? -1 : 800, rttMs, signals,
});
const net = (name, tag, peers, signals = [{ host: 's1', up: true }], signalError = '') =>
  ({ name, tag, signalError, signals, peers });
const base = (over = {}) => ({
  running: true, selfName: 'MY-PC', selfIP: '25.31.8.2', selfEndpoint: '203.0.113.9:5000',
  stunVia: 'stun1', uptimeSec: 8040, error: '', sendLogs: true, networks: [], ...over,
});

export const SCENARIOS = {
  disconnected: () => base({ running: false, selfIP: '', selfEndpoint: '', uptimeSec: 0, networks: [] }),
  empty: () => base({ networks: [net('myteam', 'a'.repeat(64), [])] }),
  team3: () => base({ networks: [net('myteam', 'a'.repeat(64), [
    peer('Мурад', '25.44.12.9', 'direct', 18),
    peer('Kolya', '25.44.7.3', 'direct', 42),
    peer('sara_pc', '25.44.31.7', 'relay', 128),
    peer('Dev_null', '25.44.9.1', 'connecting', -1),
  ])] }),
  multi: () => base({ networks: [
    net('myteam', 'a'.repeat(64), [peer('Мурад', '25.44.12.9', 'direct', 18), peer('Kolya', '25.44.7.3', 'direct', 42)]),
    net('dota', 'b'.repeat(64), [peer('gamer2000', '25.60.1.4', 'direct', 55)]),
  ] }),
  noext: () => base({ selfEndpoint: '', networks: [net('myteam', 'a'.repeat(64), [peer('Мурад', '25.44.12.9', 'connecting', -1)])] }),
  sigerr: () => base({ networks: [net('myteam', 'a'.repeat(64), [peer('Мурад', '25.44.12.9', 'direct', 18)],
    [{ host: 's1', up: false }], 'сигналка недоступна')] }),
  hostile: () => base({ networks: [net('myteam', 'a'.repeat(64), [
    // Имя с bidi-override + zero-width: sanitize обязан их вычистить.
    peer('a\u202Egnp.exe', '25.44.5.5', 'direct', 20),
  ])] }),
};
export const DEFAULT = 'team3';
