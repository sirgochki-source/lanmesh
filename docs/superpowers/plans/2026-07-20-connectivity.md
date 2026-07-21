# Подпроект A «Связность» — план реализации

> **For agentic workers:** REQUIRED SUB-SKILL: Use superpowers:subagent-driven-development (recommended) or superpowers:executing-plans to implement this plan task-by-task. Steps use checkbox (`- [ ]`) syntax for tracking.

**Goal:** сократить долю пар, вынужденных идти через единственный ретранслятор, добавив проброс порта на роутере, транспорт IPv6, транзитивный PEX и кэш endpoint'ов между запусками.

**Architecture:** новых механизмов соединения не появляется — появляются новые *источники кандидатов*, вливающиеся в уже существующие точки входа `Engine.SetSelfCandidates` (свои адреса) и `Engine.AddProbes` (чужие). Транспорт переезжает на один dual-stack сокет с адресами `netip.AddrPort` и нормализацией `Unmap()` на границе чтения.

**Tech Stack:** Go 1.26.5, Windows-only (Wintun), без новых внешних зависимостей.

Спека: `docs/superpowers/specs/2026-07-20-connectivity-design.md`.

## Global Constraints

- **Go не в PATH.** Все команды — через `C:\Users\ivest\go-sdk\go\bin\go.exe`, либо один раз за сессию: `$env:PATH = "C:\Users\ivest\go-sdk\go\bin;$env:PATH"`.
- **Никаких новых зависимостей в `go.mod`.** Проект поставляется одним самодостаточным exe; `portmap` пишется вручную.
- **Язык кода:** комментарии и сообщения об ошибках — по-русски, как во всём проекте. Комментарий объясняет ПОЧЕМУ, а не ЧТО.
- **Правило нормализации адресов** (проверено эмпирически, см. спеку): на чтении — `Unmap()` сразу после `ReadFromUDPAddrPort`; на записи — адрес отдаётся как есть, unmapped.
- **Дисциплина правок тестов при миграции (Задача 2):** в `git diff -- '*_test.go'` допустимы только смена типа переменной и способа построения литерала адреса. Изменённое ожидание, порог, тайминг или условие — стоп, а не повод подправить `want`.
- **Остаются на IPv4 сознательно:** DHT (`internal/discovery/dhtdisc` — формат BEP-5), ретранслятор (`cmd/lanmesh-relay`), STUN (`internal/signal/stun.go`). Их не трогаем.
- **Ритм задачи: реализация → тест → ОДИН коммит.** Шаги вида «прогнать тест, убедиться что падает» в тексте задач ниже оставлены как описание проверок, но красная фаза не требуется и в отчёте не нужна. Внутри задачи коммит ровно один — дробить не надо.
  - **Единственное исключение — задача 1.** Характеризационный тест обязан быть написан и зафиксирован зелёным ДО задачи 2. Это не ритуал: снимок поведения, сделанный после миграции, запишет уже сломанное поведение как ожидаемое и перестанет что-либо доказывать.
- **Медленные интеграционные тесты** (ожидание пробития 25–60 с) обязаны скипаться при `-short`:

  ```go
  if testing.Short() {
      t.Skip("ждёт пробития NAT до 25с — только полный прогон")
  }
  ```

  Быстрый прогон по ходу работы: `go test -short ./...`. Полный, обязательный перед коммитом задачи: `go test ./...` — обязан быть зелёным.

---

## File Structure

| Файл | Ответственность | Задача |
|---|---|---|
| `internal/peer/characterization_test.go` (создать) | снимок сегодняшнего поведения через публичный API; `package peer_test` — компилятор запрещает лезть во внутренности | 1 |
| `internal/peer/engine.go` (изменить) | транспортные адреса на `netip.AddrPort`; dual-stack; приём/отправка `FramePeers` | 2, 3, 9 |
| `internal/peer/pex_test.go` (создать) | кодирование кадра PEX, потолок, отбор пиров | 9 |
| `internal/peer/dualstack_test.go` (создать) | v4-пир и v6-пир через dual-stack сокет | 3 |
| `internal/proto/proto.go` (изменить) | `FramePeers byte = 8` + формат тела | 9 |
| `internal/app/session.go` (изменить) | dual-stack сокет, фиксированный порт, v6-кандидаты, `pickExternal`, подключение `netcache` и `portmap` | 3–8 |
| `internal/netcache/netcache.go` (создать) | «(тег, PeerID) → последние рабочие endpoint'ы», TTL, атомарная запись | 5 |
| `internal/netcache/netcache_test.go` (создать) | TTL, потолок, битый файл, изоляция по тегу | 5 |
| `internal/portmap/portmap.go` (создать) | каскад PCP → NAT-PMP → UPnP, аренда, отбраковка | 7 |
| `internal/portmap/pcp.go`, `natpmp.go`, `upnp.go` (создать) | по протоколу на файл — каждый разбирается отдельно | 7 |
| `internal/portmap/firewall_windows.go` (создать) | входящее правило брандмауэра через `netsh advfirewall` | 8 |
| `cmd/lanmesh-gui/main.go` (изменить) | поля `Port`, `PortMap` в `Config`; статус в `/api/state` | 4, 8 |
| `cmd/natcheck/main.go` (изменить) | четвёртый шаг: проброс и наличие IPv6 | 10 |
| `README.md` (изменить) | актуализировать «Ограничения», описать новое | 10 |

---

## Задача 1: Характеризационный тест — снимок сегодняшнего поведения

Это доказательство отсутствия регрессии. Пишется **до единой правки в коде** и после миграции обязан пройти без изменений.

**Files:**
- Create: `internal/peer/characterization_test.go`

**Interfaces:**
- Consumes: публичный API `internal/peer` — `NewEngine`, `AddNetwork`, `SyncPeers`, `UseRelay`, `PeerViews`, `Run`.
- Produces: тест `TestCharacterizationDirectPathAndRelay`, который задачи 2 и 3 обязаны сохранить зелёным без правок.

- [ ] **Шаг 1: Создать файл с внешним тестовым пакетом и харнессом**

Ключевое решение: `package peer_test`, а не `package peer`. Тогда «не трогать внутренности» проверяет **компилятор**, а не ревьюер — существующие тесты живут внутри пакета и лезут в `ps.active` напрямую, поэтому такой гарантии не дают.

```go
// Характеризационный тест: снимок поведения движка ДО миграции транспорта на
// netip.AddrPort. Живёт во ВНЕШНЕМ пакете (peer_test) сознательно — так
// компилятор не пускает к неэкспортированным полям, и тест не может «поехать»
// вместе с внутренностями. После миграции обязан пройти без единой правки.
package peer_test

import (
	"io"
	"net"
	"testing"
	"time"

	"github.com/sirgochki-source/lanmesh/internal/crypto"
	"github.com/sirgochki-source/lanmesh/internal/peer"
	"github.com/sirgochki-source/lanmesh/internal/proto"
)

// tag — тег сети. Тип [32]byte записан литералом: relayTagLen неэкспортирован.
var tag = func() [32]byte {
	var t [32]byte
	copy(t[:], []byte("характеризационный-тег-32-байта!"))
	return t
}()

type memTUN struct {
	read  chan []byte
	wrote chan []byte
}

func newMemTUN() *memTUN {
	return &memTUN{read: make(chan []byte), wrote: make(chan []byte, 32)}
}

func (m *memTUN) Read(buf []byte) (int, error) {
	p, ok := <-m.read
	if !ok {
		return 0, io.EOF
	}
	return copy(buf, p), nil
}

func (m *memTUN) Write(pkt []byte) (int, error) {
	select {
	case m.wrote <- append([]byte(nil), pkt...):
	default:
	}
	return len(pkt), nil
}

type testNode struct {
	eng  *peer.Engine
	conn *net.UDPConn
	tun  *memTUN
	id   proto.PeerID
	ip   string
}

func newTestNode(t *testing.T, sealer *crypto.Sealer) *testNode {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("сокет: %v", err)
	}
	id, err := proto.NewPeerID()
	if err != nil {
		t.Fatalf("id: %v", err)
	}
	ip := proto.VirtualIP(id)
	tun := newMemTUN()
	eng := peer.NewEngine(conn, tun, id, ip)
	eng.AddNetwork(tag, sealer, "characterization")
	t.Cleanup(func() { conn.Close() })
	return &testNode{eng: eng, conn: conn, tun: tun, id: id, ip: ip.String()}
}

func (n *testNode) info(name string) proto.PeerInfo {
	return proto.PeerInfo{
		PeerID:    n.id.String(),
		Name:      name,
		VirtualIP: n.ip,
		Endpoints: []string{n.conn.LocalAddr().String()},
	}
}
```

- [ ] **Шаг 2: Дописать в тот же файл сам тест прямого пути**

```go
// Снимок прямого пути: пробитие -> подтверждение -> ping/pong -> RTT ->
// перенос данных из TUN в TUN -> счётчики трафика. Всё через публичный API.
func TestCharacterizationDirectPath(t *testing.T) {
	sealer, err := crypto.NewSealer(crypto.DeriveNetworkKey("характеризация", "пароль"))
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}
	a, b := newTestNode(t, sealer), newTestNode(t, sealer)

	a.eng.SyncPeers(tag, []proto.PeerInfo{b.info("B")})
	b.eng.SyncPeers(tag, []proto.PeerInfo{a.info("A")})
	go a.eng.Run()
	go b.eng.Run()

	// 1. Пробитие и замер RTT. punchInterval=2с, ping — со следующего тика.
	deadline := time.Now().Add(25 * time.Second)
	for {
		v := a.eng.PeerViews(tag)
		if len(v) == 1 && v[0].Status == "direct" && v[0].RttMs >= 0 {
			if v[0].Name != "B" || v[0].VirtualIP != b.ip {
				t.Fatalf("не тот пир: %+v", v[0])
			}
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("прямой путь не установился за 25с: %+v", a.eng.PeerViews(tag))
		}
		time.Sleep(200 * time.Millisecond)
	}

	// 2. Данные из TUN узла A должны прийти в TUN узла B.
	pkt := ipv4Packet(a.ip, b.ip, []byte("характеризация"))
	select {
	case a.tun.read <- pkt:
	case <-time.After(2 * time.Second):
		t.Fatal("движок A не забрал пакет из TUN")
	}
	select {
	case got := <-b.tun.wrote:
		if string(got) != string(pkt) {
			t.Fatalf("пакет исказился при переносе:\nбыло %q\nстало %q", pkt, got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("пакет не дошёл до TUN узла B")
	}

	// 3. Счётчики трафика должны отразить перенос.
	v := a.eng.PeerViews(tag)
	if len(v) != 1 || v[0].BytesTx == 0 {
		t.Fatalf("счётчик отправленных байт не сдвинулся: %+v", v)
	}
}

// ipv4Packet собирает минимальный IPv4-пакет (протокол 253, «для экспериментов»
// по RFC 3692) — движок смотрит только на заголовок и адрес назначения.
func ipv4Packet(src, dst string, payload []byte) []byte {
	pkt := make([]byte, 20+len(payload))
	pkt[0] = 0x45 // версия 4, длина заголовка 5 слов
	pkt[8] = 64   // TTL
	pkt[9] = 253  // протокол
	total := len(pkt)
	pkt[2], pkt[3] = byte(total>>8), byte(total)
	copy(pkt[12:16], net.ParseIP(src).To4())
	copy(pkt[16:20], net.ParseIP(dst).To4())
	copy(pkt[20:], payload)
	return pkt
}
```

- [ ] **Шаг 3: Прогнать на ТЕКУЩЕМ коде — обязан пройти**

```
C:\Users\ivest\go-sdk\go\bin\go.exe test ./internal/peer/ -run TestCharacterization -v -count=1
```

Ожидается: `PASS`. Если падает — тест описывает не то поведение, которое есть; чинить тест, а не движок. Смысл шага именно в этом: снимок должен быть верным ДО правок.

- [ ] **Шаг 4: Прогнать трижды — отсеять флаки**

```
C:\Users\ivest\go-sdk\go\bin\go.exe test ./internal/peer/ -run TestCharacterization -count=3
```

Ожидается: `ok`. Нестабильный снимок бесполезен как доказательство — если моргает, поднять таймауты в шаге 2 и повторить.

- [ ] **Шаг 5: Коммит**

```bash
git add internal/peer/characterization_test.go
git commit -m "test(peer): характеризационный снимок поведения перед миграцией транспорта

Внешний пакет peer_test выбран сознательно: существующие тесты живут
внутри пакета и обращаются к ps.active напрямую, поэтому пережить смену
типа без правок не могут. Компилятор не пускает внешний тест к
неэкспортированным полям — значит снимок не «поедет» вместе с
внутренностями и годится как доказательство отсутствия регрессии."
```

---

## Задача 2: Миграция транспортных адресов на `netip.AddrPort`

Поведение сохраняется полностью: сокет остаётся `udp4`, IPv6 не появляется.

**Files:**
- Modify: `internal/peer/engine.go` (поля `probeAddr.addr`, `peerState.endpoints`, `peerState.active`, `Engine.relay`; `writeFrame`, `writeFrameRelay`, `netToTun`, `sendPunch`, `sendToAll`, `directAddr`, `sameAddr`, `mergeEndpoints`, `mergeCandidates`, `AddProbes`, `UseRelay`, `relayAddr`)
- Modify: `internal/peer/engine_test.go`, `relay_test.go`, `discovery_test.go` (только типы литералов)

**Interfaces:**
- Consumes: `TestCharacterizationDirectPath` из задачи 1.
- Produces: `Engine.UseRelay(netip.AddrPort)`; внутренние адреса — всегда unmapped `netip.AddrPort`. Задача 3 опирается на то, что тип уже сменён.

- [ ] **Шаг 1: Сменить типы полей**

`internal/peer/engine.go`, строки 156–161 и 163–205:

```go
// probeAddr — голый адрес-кандидат и момент, когда мы о нём узнали.
type probeAddr struct {
	addr     netip.AddrPort
	added    time.Time
	tries    int
	lastPoke time.Time
}
```

В `peerState` (строки 171–172):

```go
	endpoints []netip.AddrPort // кандидаты (STUN + локальные)
	active    netip.AddrPort   // подтверждён по входящему пакету — ТОЛЬКО прямой путь
```

В `Engine` (строка 221):

```go
	relay netip.AddrPort
```

Пустое значение теперь `netip.AddrPort{}`, а не `nil`; проверка «есть ли адрес» — `.IsValid()`.

- [ ] **Шаг 2: Переписать чтение сокета с нормализацией**

`netToTun`, строка 698. Это то самое место, где `Unmap()` обязателен — проба показала, что v4-отправитель на dual-stack сокете приходит как `::ffff:…` с `Is4() == false`, и без нормализации он не сравнится с сохранённым чистым v4:

```go
		n, srcAP, err := e.conn.ReadFromUDPAddrPort(buf)
		if err != nil {
			return err
		}
		// Нормализация ровно здесь и больше нигде: дальше по коду mapped-адресов
		// не существует, поэтому sameAddr/дедуп probes/подтверждение endpoint'а
		// работают с одной формой. Проверено: v4 через dual-stack сокет приходит
		// как ::ffff:a.b.c.d, Is4()==false.
		src := netip.AddrPortFrom(srcAP.Addr().Unmap(), srcAP.Port())
```

- [ ] **Шаг 3: Переписать запись**

`writeFrame` (строка 1296) и `writeFrameRelay` (строка 1307):

```go
func (e *Engine) writeFrame(n *network, dst netip.AddrPort, typ byte, payload []byte) {
	sealed, err := e.seal(n, typ, payload)
	if err != nil {
		return
	}
	// Адрес отдаём как есть, unmapped: отображение в v4-in-v6 для AF_INET6-сокета
	// делает стандартная библиотека (проверено на Go 1.26.5).
	e.conn.WriteToUDPAddrPort(sealed, dst)
}
```

В `writeFrameRelay` — так же, последняя строка становится `e.conn.WriteToUDPAddrPort(pkt, relay)`, а проверка сверху: `if !relay.IsValid() { return }`.

- [ ] **Шаг 4: Упростить `sameAddr` и поправить остальные места**

`sameAddr` (строка 1379) схлопывается — `netip.AddrPort` сравним оператором:

```go
// sameAddr больше не нужен: netip.AddrPort сравнивается напрямую, а обе стороны
// нормализованы Unmap на чтении. Все вызовы заменить на ==.
```

Заменить `sameAddr(src, relay)` на `src == relay` (строка 705). В `directAddr` (1255) — `if ps.active.IsValid() && ...`, возврат `netip.AddrPort{}` вместо `nil`. В `AddProbes` (345) — `netip.ParseAddrPort(s)` вместо `net.ResolveUDPAddr("udp4", s)`, ключ карты `k := a.String()` остаётся. В `mergeEndpoints` (1416), `mergeCandidates` (1442), `sendToAll` (1218) — смена типа поля `direct`.

- [ ] **Шаг 5: Поправить `UseRelay` и вызовы в тестах**

```go
func (e *Engine) UseRelay(addr netip.AddrPort) {
```

В `internal/peer/discovery_test.go:198` и `relay_test.go:164` заменить `relay.LocalAddr().(*net.UDPAddr)` на `relay.LocalAddr().(*net.UDPAddr).AddrPort()`. В `engine_test.go:300` — `ps.active = netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), 1)`. Объявления `var active *net.UDPAddr` (169, 191, relay_test 240) — на `var active netip.AddrPort`.

**Ничего кроме типов и литералов.** Изменённое ожидание — стоп.

- [ ] **Шаг 6: Прогнать характеризационный тест — доказательство**

```
C:\Users\ivest\go-sdk\go\bin\go.exe test ./internal/peer/ -run TestCharacterization -count=3
```

Ожидается: `ok`, **без единой правки файла из задачи 1**.

- [ ] **Шаг 7: Прогнать всё и проверить дисциплину диффа**

```
C:\Users\ivest\go-sdk\go\bin\go.exe test ./... -count=1
git diff --stat -- '*_test.go'
git diff -- '*_test.go'
```

Ожидается: всё зелёное; в диффе тестов — только типы и литералы адресов.

- [ ] **Шаг 8: Коммит**

```bash
git add internal/peer/
git commit -m "refactor(peer): транспортные адреса на netip.AddrPort без смены поведения

Подготовка к dual-stack сокету. Сокет остаётся udp4, IPv6 не появляется.
netip.AddrPort сравним оператором == и не аллоцирует в Read/Write — sameAddr
схлопнулся, горячий путь стал дешевле. Характеризационный тест из
предыдущего коммита прошёл без правок; в тестах менялись только типы
переменных и литералы адресов."
```

---

## Задача 3: Dual-stack сокет и кандидаты IPv6

**Files:**
- Modify: `internal/app/session.go:525` (создание сокета), `internal/app/session.go:1358` (`localEndpoints`)
- Create: `internal/peer/dualstack_test.go`

**Interfaces:**
- Consumes: `netip.AddrPort` из задачи 2.
- Produces: `app.listenNode(port int) (*net.UDPConn, error)` — сокет с фолбэком; `localEndpoints` возвращает и v6-кандидаты.

- [ ] **Шаг 1: Написать падающий тест «v4-пир через dual-stack сокет»**

Это конфигурация 90% продакшена, и ни характеризационный тест (идёт по `udp4`), ни будущий v6-тест её не покрывают.

`internal/peer/dualstack_test.go`:

```go
package peer

import (
	"net"
	"testing"
	"time"

	"github.com/sirgochki-source/lanmesh/internal/crypto"
	"github.com/sirgochki-source/lanmesh/internal/proto"
)

// newNodeOn — как newNode, но сокет создаёт вызывающий: нужно свести узлы на
// РАЗНЫХ семействах сокетов.
func newNodeOn(t *testing.T, sealer *crypto.Sealer, conn *net.UDPConn) *node {
	t.Helper()
	id, err := proto.NewPeerID()
	if err != nil {
		t.Fatalf("id: %v", err)
	}
	ip := proto.VirtualIP(id)
	tun := newFakeTUN()
	eng := NewEngine(conn, tun, id, ip)
	eng.AddNetwork(testTag, sealer, "dualstack")
	return &node{eng: eng, conn: conn, tun: tun, id: id, ip: ip.String(), tag: testTag}
}

// Узел на dual-stack сокете обязан пробиться к узлу на чистом udp4 — это самая
// частая конфигурация в бою (все существующие пиры ходят по IPv4).
func TestDualStackReachesIPv4Peer(t *testing.T) {
	sealer, err := crypto.NewSealer(crypto.DeriveNetworkKey("dualstack", "пароль"))
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}

	dsConn, err := net.ListenUDP("udp", &net.UDPAddr{Port: 0})
	if err != nil {
		t.Skipf("dual-stack сокет недоступен: %v", err)
	}
	defer dsConn.Close()
	v4Conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("udp4 сокет: %v", err)
	}
	defer v4Conn.Close()

	ds := newNodeOn(t, sealer, dsConn)
	v4 := newNodeOn(t, sealer, v4Conn)

	// Dual-stack сокет слушает [::], поэтому адресовать его надо явным 127.0.0.1.
	dsPort := dsConn.LocalAddr().(*net.UDPAddr).Port
	dsInfo := proto.PeerInfo{
		PeerID: ds.id.String(), Name: "DS", VirtualIP: ds.ip,
		Endpoints: []string{net.JoinHostPort("127.0.0.1", strconv.Itoa(dsPort))},
	}
	ds.eng.SyncPeers(testTag, []proto.PeerInfo{v4.info("V4")})
	v4.eng.SyncPeers(testTag, []proto.PeerInfo{dsInfo})

	go ds.eng.Run()
	go v4.eng.Run()
	waitDirect(t, ds.eng, testTag, 25*time.Second)
}

// waitDirect ждёт, пока у движка появится ровно один пир со статусом direct.
func waitDirect(t *testing.T, eng *Engine, tag [relayTagLen]byte, d time.Duration) {
	t.Helper()
	deadline := time.Now().Add(d)
	for time.Now().Before(deadline) {
		v := eng.PeerViews(tag)
		if len(v) == 1 && v[0].Status == "direct" {
			return
		}
		time.Sleep(200 * time.Millisecond)
	}
	t.Fatalf("прямой путь не установился за %v: %+v", d, eng.PeerViews(tag))
}

```

Импорты файла: `"net"`, `"net/netip"`, `"strconv"`, `"testing"`, `"time"`, плюс `crypto` и `proto`. Порт в адрес подставляется через `strconv.Itoa(dsPort)`.

- [ ] **Шаг 2: Прогнать — обязан пройти уже сейчас**

```
C:\Users\ivest\go-sdk\go\bin\go.exe test ./internal/peer/ -run TestDualStack -v -count=1
```

Ожидается: `PASS`. Тест проверяет свойство стандартной библиотеки, которое задача 2 уже сделала доступным; он закрепляет проверенное пробой поведение как постоянный регрессионный тест.

- [ ] **Шаг 3: Добавить v6-тест на `::1`**

Дописать в `internal/peer/dualstack_test.go`:

```go
// Два узла соединяются по IPv6-loopback. Детерминированно: ::1 есть без сети.
func TestPeersOverIPv6Loopback(t *testing.T) {
	sealer, err := crypto.NewSealer(crypto.DeriveNetworkKey("v6", "пароль"))
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}
	mk := func() *net.UDPConn {
		c, err := net.ListenUDP("udp6", &net.UDPAddr{IP: net.IPv6loopback, Port: 0})
		if err != nil {
			t.Skipf("IPv6 недоступен: %v", err)
		}
		return c
	}
	ac, bc := mk(), mk()
	defer ac.Close()
	defer bc.Close()

	a, b := newNodeOn(t, sealer, ac), newNodeOn(t, sealer, bc)
	a.eng.SyncPeers(testTag, []proto.PeerInfo{{
		PeerID: b.id.String(), Name: "B", VirtualIP: b.ip,
		Endpoints: []string{bc.LocalAddr().String()},
	}})
	b.eng.SyncPeers(testTag, []proto.PeerInfo{{
		PeerID: a.id.String(), Name: "A", VirtualIP: a.ip,
		Endpoints: []string{ac.LocalAddr().String()},
	}})
	go a.eng.Run()
	go b.eng.Run()
	waitDirect(t, a.eng, testTag, 25*time.Second)
}
```

- [ ] **Шаг 4: Прогнать v6-тест**

```
C:\Users\ivest\go-sdk\go\bin\go.exe test ./internal/peer/ -run "TestDualStack|TestPeersOverIPv6" -v -count=1
```

Ожидается: обa `PASS` (либо `SKIP` для v6, если IPv6 в системе отключён).

- [ ] **Шаг 5: Перевести боевой сокет на dual-stack**

`internal/app/session.go`, заменить строку 525:

```go
	conn, err := listenNode(0)
	if err != nil {
		return fmt.Errorf("udp listen: %w", err)
	}
```

Добавить рядом функцию:

```go
// listenNode поднимает боевой UDP-сокет. Просим "udp" с неуказанным IP — Go
// ставит IPV6_V6ONLY=0, и один сокет обслуживает оба семейства. Фолбэк на udp4
// нужен там, где IPv6-стек отключён политикой или отсутствует: узел обязан
// работать ровно как раньше, а не падать при старте.
func listenNode(port int) (*net.UDPConn, error) {
	conn, err := net.ListenUDP("udp", &net.UDPAddr{Port: port})
	if err == nil {
		return conn, nil
	}
	log.Printf("dual-stack сокет недоступен (%v) — работаем только по IPv4", err)
	return net.ListenUDP("udp4", &net.UDPAddr{Port: port})
}
```

- [ ] **Шаг 6: Добавить IPv6 в кандидаты**

`internal/app/session.go`, `localEndpoints` (строка 1358). Сейчас v6 отсеивается через `ip4 == nil`. Новое тело цикла:

```go
	for _, a := range addrs {
		ipnet, ok := a.(*net.IPNet)
		if !ok || ipnet.IP.IsLoopback() || ipnet.IP.IsLinkLocalUnicast() {
			continue
		}
		if ip4 := ipnet.IP.To4(); ip4 != nil {
			if ip4[0] == 25 {
				continue // наш же виртуальный адаптер
			}
			out = append(out, fmt.Sprintf("%s:%d", ip4.String(), port))
			continue
		}
		// IPv6: берём только глобальные юникасты. ULA (fc00::/7) не маршрутизируется
		// в интернете, а link-local отсеян выше. STUN для них не нужен — NAT нет,
		// адрес интерфейса и есть внешний адрес.
		if ipnet.IP.IsGlobalUnicast() && !ipnet.IP.IsPrivate() {
			out = append(out, fmt.Sprintf("[%s]:%d", ipnet.IP.String(), port))
		}
	}
```

- [ ] **Шаг 7: Прогнать всё**

```
C:\Users\ivest\go-sdk\go\bin\go.exe test ./... -count=1
```

Ожидается: всё зелёное, включая характеризационный тест.

- [ ] **Шаг 8: Коммит**

```bash
git add internal/peer/dualstack_test.go internal/app/session.go
git commit -m "feat(app): dual-stack сокет и кандидаты IPv6

Один сокет на оба семейства с фолбэком на udp4 там, где IPv6 отключён.
В кандидаты добавлены глобальные IPv6-юникасты: при нативном v6 NAT нет,
адрес интерфейса и есть внешний, поэтому STUN для них не запрашивается.

Тест v4-пира через dual-stack сокет закрывает конфигурацию, которую не
трогали ни характеризационный тест (идёт по udp4), ни v6-тест (::1), —
а в бою она будет основной."
```

---

## Задача 4: Фиксированный локальный порт

**Files:**
- Modify: `cmd/lanmesh-gui/main.go:84-101` (`Config`), `internal/app/session.go` (`bringUpNode`)
- Create: тест в `internal/app/port_test.go`

**Interfaces:**
- Consumes: `listenNode(port int)` из задачи 3.
- Produces: `app.PickPort(saved int) (listen int, save bool)`; поле `Config.Port int`.

- [ ] **Шаг 1: Написать падающий тест выбора порта**

`internal/app/port_test.go`:

```go
package app

import (
	"net"
	"testing"
)

// Первый запуск: сохранённого порта нет — берём случайный и просим сохранить.
func TestPickPortFirstRun(t *testing.T) {
	got, save := PickPort(0)
	if got < portRangeLo || got > portRangeHi {
		t.Fatalf("порт %d вне диапазона %d..%d", got, portRangeLo, portRangeHi)
	}
	if !save {
		t.Fatal("первый запуск обязан просить сохранение порта")
	}
}

// Сохранённый порт свободен — используем его и сохранять заново не нужно.
func TestPickPortReusesSaved(t *testing.T) {
	free, err := net.ListenUDP("udp4", &net.UDPAddr{Port: 0})
	if err != nil {
		t.Fatalf("сокет: %v", err)
	}
	port := free.LocalAddr().(*net.UDPAddr).Port
	free.Close()

	got, save := PickPort(port)
	if got != port {
		t.Fatalf("сохранённый порт %d не переиспользован, получили %d", port, got)
	}
	if save {
		t.Fatal("переиспользование не должно перезаписывать конфиг")
	}
}

// Сохранённый порт занят: берём другой, но конфиг НЕ трогаем — иначе второй
// экземпляр (run-node2.cmd) при каждом старте угонял бы порт у первого.
func TestPickPortBusyKeepsConfig(t *testing.T) {
	busy, err := net.ListenUDP("udp", &net.UDPAddr{Port: 0})
	if err != nil {
		t.Fatalf("сокет: %v", err)
	}
	defer busy.Close()
	port := busy.LocalAddr().(*net.UDPAddr).Port

	got, save := PickPort(port)
	if got == port {
		t.Fatalf("занятый порт %d выдан повторно", port)
	}
	if save {
		t.Fatal("при занятом порте конфиг перезаписывать нельзя")
	}
}
```

- [ ] **Шаг 2: Прогнать — обязан упасть**

```
C:\Users\ivest\go-sdk\go\bin\go.exe test ./internal/app/ -run TestPickPort -v -count=1
```

Ожидается: `FAIL` — `undefined: PickPort`.

- [ ] **Шаг 3: Реализовать**

Добавить в `internal/app/session.go`:

```go
// Диапазон для постоянного порта узла. НЕ эфемерный (у Windows 49152–65535):
// сохранённый оттуда порт после перезагрузки может оказаться занят посторонним
// приложением, которое система обслужила раньше нас.
const (
	portRangeLo = 20000
	portRangeHi = 40000
)

// PickPort выбирает локальный UDP-порт узла. Возвращает порт и признак «сохрани
// меня в конфиг».
//
// Постоянный порт нужен двум фичам: проброс на роутере иначе пересоздавался бы
// каждый запуск и засорял таблицу маппингов, а кэш endpoint'ов был бы наполовину
// бесполезен — друзья помнят прежний ip:port, а узел уже на другом.
//
// Занятый сохранённый порт НЕ перезаписывает конфиг: иначе второй экземпляр на
// той же машине (run-node2.cmd) при каждом старте угонял бы порт у первого.
func PickPort(saved int) (int, bool) {
	if saved != 0 && portFree(saved) {
		return saved, false
	}
	for i := 0; i < 20; i++ {
		p := portRangeLo + rand.IntN(portRangeHi-portRangeLo)
		if portFree(p) {
			return p, saved == 0
		}
	}
	return 0, false // сдаёмся на случайный от ОС; сохранять нечего
}

// portFree — свободен ли порт на всех интерфейсах. Проверяем тем же способом,
// каким потом слушаем (dual-stack), иначе проверка соврала бы.
func portFree(p int) bool {
	c, err := net.ListenUDP("udp", &net.UDPAddr{Port: p})
	if err != nil {
		return false
	}
	c.Close()
	return true
}
```

Добавить импорт `"math/rand/v2"`.

- [ ] **Шаг 4: Прогнать тесты — обязаны пройти**

```
C:\Users\ivest\go-sdk\go\bin\go.exe test ./internal/app/ -run TestPickPort -v -count=1
```

Ожидается: три `PASS`.

- [ ] **Шаг 5: Подключить к сессии и конфигу**

В `cmd/lanmesh-gui/main.go`, в `Config` (после строки 100):

```go
	// Port — постоянный локальный UDP-порт узла; 0 = ещё не выбран.
	Port int `json:"port,omitempty"`
```

В `internal/app/session.go`, `bringUpNode`, заменить вызов из задачи 3:

```go
	port, savePort := PickPort(s.savedPort)
	conn, err := listenNode(port)
	if err != nil {
		return fmt.Errorf("udp listen: %w", err)
	}
	if savePort {
		if cb := s.onPortChosen; cb != nil {
			cb(conn.LocalAddr().(*net.UDPAddr).Port)
		}
	}
```

Добавить в `Session` поля `savedPort int` и `onPortChosen func(int)`, выставляемые из GUI при создании сессии; GUI в колбэке пишет `cfg.Port` и сохраняет конфиг.

- [ ] **Шаг 6: Прогнать всё**

```
C:\Users\ivest\go-sdk\go\bin\go.exe test ./... -count=1
```

- [ ] **Шаг 7: Коммит**

```bash
git add internal/app/ cmd/lanmesh-gui/main.go
git commit -m "feat(app): постоянный локальный UDP-порт узла

Порт выбирается один раз и живёт в config.json. Предусловие для проброса
(иначе маппинг пересоздавался бы каждый запуск) и для кэша endpoint'ов
(иначе друзья помнят прежний порт, а мы уже на другом).

Диапазон 20000-40000, вне эфемерного: сохранённый порт из 49152-65535
после перезагрузки может оказаться занят посторонним приложением.
Занятый порт не перезаписывает конфиг — иначе второй экземпляр на той же
машине угонял бы порт у первого."
```

---

## Задача 5: Пакет `internal/netcache`

**Files:**
- Create: `internal/netcache/netcache.go`, `internal/netcache/netcache_test.go`

**Interfaces:**
- Produces: `netcache.Open(path string) *Cache`; `(*Cache).Get(tag string, id string) []string`; `(*Cache).Put(tag, id, endpoint string)`; `(*Cache).Save() error`. Тег и PeerID — hex-строки, как в остальном коде; адрес — `ip:port`.

- [ ] **Шаг 1: Написать падающие тесты**

`internal/netcache/netcache_test.go`:

```go
package netcache

import (
	"os"
	"path/filepath"
	"testing"
	"time"
)

func tmp(t *testing.T) string {
	t.Helper()
	return filepath.Join(t.TempDir(), "endpoints.json")
}

// Записали — прочитали после перезагрузки с диска.
func TestPutGetRoundTrip(t *testing.T) {
	path := tmp(t)
	c := Open(path)
	c.Put("тег1", "пир1", "203.0.113.5:31337")
	if err := c.Save(); err != nil {
		t.Fatalf("сохранение: %v", err)
	}

	got := Open(path).Get("тег1", "пир1")
	if len(got) != 1 || got[0] != "203.0.113.5:31337" {
		t.Fatalf("ожидали один адрес, получили %v", got)
	}
}

// Адрес из одной сети не должен всплыть в пробах другой: пробить чужой адрес
// безвредно, но это лишний трафик и лишний шум в диагностике.
func TestTagIsolation(t *testing.T) {
	c := Open(tmp(t))
	c.Put("тег1", "пир1", "203.0.113.5:1")
	if got := c.Get("тег2", "пир1"); len(got) != 0 {
		t.Fatalf("адрес протёк в чужую сеть: %v", got)
	}
}

// Держим три последних адреса: самый свежий вытесняет самый старый.
func TestKeepsLastThree(t *testing.T) {
	c := Open(tmp(t))
	for _, a := range []string{"1.1.1.1:1", "2.2.2.2:2", "3.3.3.3:3", "4.4.4.4:4"} {
		c.Put("тег", "пир", a)
	}
	got := c.Get("тег", "пир")
	if len(got) != maxPerPeer {
		t.Fatalf("ожидали %d адресов, получили %d: %v", maxPerPeer, len(got), got)
	}
	for _, a := range got {
		if a == "1.1.1.1:1" {
			t.Fatal("самый старый адрес не вытеснен")
		}
	}
}

// Протухшие записи не отдаются и не переживают сохранение.
func TestTTLExpiry(t *testing.T) {
	path := tmp(t)
	c := Open(path)
	c.Put("тег", "пир", "203.0.113.5:1")
	c.entries["тег|пир"][0].Seen = time.Now().Add(-ttl - time.Hour)
	if got := c.Get("тег", "пир"); len(got) != 0 {
		t.Fatalf("протухший адрес отдан: %v", got)
	}
}

// Битый файл не должен ронять узел: читается как пустой кэш.
func TestCorruptFileIsEmpty(t *testing.T) {
	path := tmp(t)
	if err := os.WriteFile(path, []byte("{это не json"), 0600); err != nil {
		t.Fatalf("подготовка: %v", err)
	}
	if got := Open(path).Get("тег", "пир"); len(got) != 0 {
		t.Fatalf("из битого файла что-то прочиталось: %v", got)
	}
}

// Сохранение атомарно: временный файл не остаётся рядом.
func TestSaveLeavesNoTemp(t *testing.T) {
	path := tmp(t)
	c := Open(path)
	c.Put("тег", "пир", "203.0.113.5:1")
	if err := c.Save(); err != nil {
		t.Fatalf("сохранение: %v", err)
	}
	files, _ := os.ReadDir(filepath.Dir(path))
	if len(files) != 1 {
		t.Fatalf("рядом остался мусор: %v", files)
	}
}
```

- [ ] **Шаг 2: Прогнать — обязаны упасть**

```
C:\Users\ivest\go-sdk\go\bin\go.exe test ./internal/netcache/ -v -count=1
```

Ожидается: `FAIL` — пакета нет.

- [ ] **Шаг 3: Реализовать пакет**

`internal/netcache/netcache.go`:

```go
// Package netcache помнит между запусками адреса, по которым пиры реально
// отвечали. При следующем старте они уходят в пробитие СРАЗУ, не дожидаясь
// ответа сигналки: если адрес друга не менялся, линк поднимается на первой
// секунде, а не после первого раунда регистрации.
//
// Файл НЕ шифруется сознательно: config.json рядом хранит пароли сетей открытым
// текстом, поэтому шифрование соседнего файла с адресами ничего не защищает —
// кто прочитал одно, прочитал и другое.
package netcache

import (
	"encoding/json"
	"os"
	"path/filepath"
	"sync"
	"time"
)

const (
	// ttl — длинный сознательно: проба стоит несколько UDP-пакетов с backoff, а
	// мёртвый адрес отсеется сам. Короткий TTL сделал бы кэш бесполезным.
	ttl = 30 * 24 * time.Hour
	// maxPerPeer — сколько адресов помним на пира: домашний, мобильный, рабочий.
	maxPerPeer = 3
)

type entry struct {
	Addr string    `json:"addr"`
	Seen time.Time `json:"seen"`
}

// Cache — «(тег сети, PeerID) → последние подтверждённые адреса».
type Cache struct {
	path string

	mu      sync.Mutex
	entries map[string][]entry
	dirty   bool
}

// Open читает кэш. Ошибки чтения не возвращаются: битый или отсутствующий файл —
// это просто пустой кэш, ронять из-за него узел незачем.
func Open(path string) *Cache {
	c := &Cache{path: path, entries: map[string][]entry{}}
	data, err := os.ReadFile(path)
	if err != nil {
		return c
	}
	var stored map[string][]entry
	if json.Unmarshal(data, &stored) != nil {
		return c
	}
	c.entries = stored
	return c
}

func key(tag, id string) string { return tag + "|" + id }

// Get отдаёт живые адреса пира, свежие первыми.
func (c *Cache) Get(tag, id string) []string {
	c.mu.Lock()
	defer c.mu.Unlock()
	now := time.Now()
	var out []string
	for _, e := range c.entries[key(tag, id)] {
		if now.Sub(e.Seen) < ttl {
			out = append(out, e.Addr)
		}
	}
	return out
}

// Put запоминает адрес, по которому пир ОТВЕТИЛ. Кандидатов сюда класть нельзя:
// кэш накопил бы мусор из DHT и воспроизводил его при каждом старте.
func (c *Cache) Put(tag, id, addr string) {
	c.mu.Lock()
	defer c.mu.Unlock()
	k := key(tag, id)
	list := c.entries[k]
	for i, e := range list {
		if e.Addr == addr {
			list[i].Seen = time.Now()
			c.entries[k], c.dirty = list, true
			return
		}
	}
	list = append([]entry{{Addr: addr, Seen: time.Now()}}, list...)
	if len(list) > maxPerPeer {
		list = list[:maxPerPeer]
	}
	c.entries[k], c.dirty = list, true
}

// Save пишет кэш атомарно (temp + rename). Зовётся по таймеру и при выходе, а не
// на каждый пакет: файл не должен стать источником дисковой нагрузки под
// игровым трафиком.
func (c *Cache) Save() error {
	c.mu.Lock()
	if !c.dirty {
		c.mu.Unlock()
		return nil
	}
	now := time.Now()
	live := map[string][]entry{}
	for k, list := range c.entries {
		var keep []entry
		for _, e := range list {
			if now.Sub(e.Seen) < ttl {
				keep = append(keep, e)
			}
		}
		if len(keep) > 0 {
			live[k] = keep
		}
	}
	data, err := json.Marshal(live)
	c.dirty = false
	c.mu.Unlock()
	if err != nil {
		return err
	}
	if err := os.MkdirAll(filepath.Dir(c.path), 0700); err != nil {
		return err
	}
	tmp := c.path + ".tmp"
	if err := os.WriteFile(tmp, data, 0600); err != nil {
		return err
	}
	return os.Rename(tmp, c.path)
}
```

- [ ] **Шаг 4: Прогнать — обязаны пройти**

```
C:\Users\ivest\go-sdk\go\bin\go.exe test ./internal/netcache/ -v -count=1
```

Ожидается: шесть `PASS`.

- [ ] **Шаг 5: Коммит**

```bash
git add internal/netcache/
git commit -m "feat(netcache): кэш подтверждённых endpoint'ов между запусками

Ключ — пара (тег сети, PeerID): без тега адрес одной сети утёк бы в пробы
соседней. Кладём только адреса, по которым пир ОТВЕТИЛ, а не кандидатов,
иначе кэш накапливал бы мусор из DHT и воспроизводил его при каждом старте.

TTL 30 дней намеренно длинный: проба стоит несколько UDP-пакетов с backoff,
мёртвый адрес отсеется сам, а короткий TTL сделал бы кэш бесполезным."
```

---

## Задача 6: Подключить кэш к сессии

**Files:**
- Modify: `internal/app/session.go` (подключение сети, подтверждение пира, таймер сохранения)
- Modify: `internal/peer/engine.go` (колбэк о подтверждённом адресе)

**Interfaces:**
- Consumes: `netcache` из задачи 5; `Engine.AddProbes` (существует).
- Produces: `Engine.OnDirectConfirmed(func(tag [relayTagLen]byte, id proto.PeerID, addr netip.AddrPort))` — движок сообщает наружу о подтверждённом прямом адресе.

- [ ] **Шаг 1: Написать падающий интеграционный тест**

Добавить в `internal/peer/dualstack_test.go`:

```go
// Движок обязан сообщать наружу о подтверждённом прямом адресе — на этом
// держится кэш endpoint'ов. Кандидаты не годятся: в кэш должно попадать только
// то, по чему пир реально ответил.
func TestOnDirectConfirmedFires(t *testing.T) {
	sealer, err := crypto.NewSealer(crypto.DeriveNetworkKey("кэш", "пароль"))
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}
	a, b := newNode(t, sealer), newNode(t, sealer)
	defer a.conn.Close()
	defer b.conn.Close()

	got := make(chan string, 4)
	a.eng.OnDirectConfirmed(func(tag [relayTagLen]byte, id proto.PeerID, addr netip.AddrPort) {
		if id == b.id {
			select {
			case got <- addr.String():
			default:
			}
		}
	})

	a.eng.SyncPeers(testTag, []proto.PeerInfo{b.info("B")})
	b.eng.SyncPeers(testTag, []proto.PeerInfo{a.info("A")})
	go a.eng.Run()
	go b.eng.Run()

	select {
	case addr := <-got:
		if addr != b.conn.LocalAddr().(*net.UDPAddr).AddrPort().String() {
			t.Fatalf("подтверждён не тот адрес: %s", addr)
		}
	case <-time.After(25 * time.Second):
		t.Fatal("колбэк подтверждения не сработал")
	}
}
```

Добавить в импорты файла `"net/netip"`.

- [ ] **Шаг 2: Прогнать — обязан упасть**

```
C:\Users\ivest\go-sdk\go\bin\go.exe test ./internal/peer/ -run TestOnDirectConfirmed -v -count=1
```

Ожидается: `FAIL` — `eng.OnDirectConfirmed undefined`.

- [ ] **Шаг 3: Реализовать колбэк в движке**

В `Engine` (после строки 243) добавить поле:

```go
	// onDirect — наружу сообщаем ТОЛЬКО подтверждённый прямой адрес (пришёл
	// расшифрованный кадр). Кандидаты не годятся: кэш накопил бы мусор из DHT.
	onDirect func(tag [relayTagLen]byte, id proto.PeerID, addr netip.AddrPort)
```

Метод рядом с `SetSelfName`:

```go
// OnDirectConfirmed ставит колбэк, дёргаемый при подтверждении прямого пути к
// пиру. Зовётся из горячего пути чтения — колбэк обязан быть быстрым и не
// блокировать (запись в кэш идёт в память, на диск сохраняет таймер сессии).
func (e *Engine) OnDirectConfirmed(fn func(tag [relayTagLen]byte, id proto.PeerID, addr netip.AddrPort)) {
	e.mu.Lock()
	e.onDirect = fn
	e.mu.Unlock()
}
```

В `netToTun`, в месте, где входящий кадр подтверждает endpoint (там, где выставляется `ps.active = src` и `ps.lastRecv = now`), после присваивания добавить:

```go
		if changed && e.onDirect != nil {
			e.onDirect(nw.tag, ps.id, src)
		}
```

где `changed` — признак, что `ps.active` изменился или был невалиден (не дёргаем колбэк на каждом пакете).

- [ ] **Шаг 4: Прогнать — обязан пройти**

```
C:\Users\ivest\go-sdk\go\bin\go.exe test ./internal/peer/ -run TestOnDirectConfirmed -v -count=1
```

- [ ] **Шаг 5: Подключить кэш в сессии**

В `internal/app/session.go`: открыть кэш рядом с `config.json` при создании сессии, поставить колбэк после `NewEngine`, влить адреса при подключении сети и завести таймер сохранения.

```go
	// Кэш заливаем ДО первого раунда сигналки: если адрес друга не менялся,
	// пробитие начинается на первой секунде, а не после ответа сервера.
	if s.cache != nil {
		for _, p := range known {
			if addrs := s.cache.Get(tagHex, p.PeerID); len(addrs) > 0 {
				eng.AddProbes(ns.tagB, addrs)
			}
		}
	}
```

Колбэк:

```go
	eng.OnDirectConfirmed(func(tag [relayTagLen]byte, id proto.PeerID, addr netip.AddrPort) {
		s.cache.Put(hex.EncodeToString(tag[:]), id.String(), addr.String())
	})
```

Таймер: раз в минуту `s.cache.Save()`, и один раз в `tearDownNode`.

- [ ] **Шаг 6: Прогнать всё**

```
C:\Users\ivest\go-sdk\go\bin\go.exe test ./... -count=1
```

- [ ] **Шаг 7: Коммит**

```bash
git add internal/peer/ internal/app/
git commit -m "feat(app): подключить кэш endpoint'ов к сессии

Движок сообщает о подтверждённом прямом адресе колбэком; сессия кладёт его
в кэш и при следующем подключении сети отдаёт в AddProbes ДО первого раунда
сигналки. Колбэк дёргается только на смене адреса, не на каждом пакете —
он в горячем пути чтения."
```

---

## Задача 7: Пакет `internal/portmap`

**Files:**
- Create: `internal/portmap/portmap.go`, `pcp.go`, `natpmp.go`, `upnp.go`, `portmap_test.go`

**Interfaces:**
- Produces: `portmap.Run(ctx context.Context, localPort int, stunIP netip.Addr) <-chan Mapping`; `type Mapping struct { External netip.AddrPort; Proto string }`; `portmap.Usable(ext netip.Addr, stunIP netip.Addr) bool`.

- [ ] **Шаг 1: Написать падающий тест отбраковки — самый важный тест подпроекта**

`internal/portmap/portmap_test.go`:

```go
package portmap

import (
	"net/netip"
	"testing"
)

// Отбраковка неанонсируемых адресов. Без неё фича ВРЕДНА: пиры будут долбиться
// в мусорный адрес вместо рабочих кандидатов, то есть станет хуже, чем без
// проброса вовсе.
func TestUsableRejectsUnroutable(t *testing.T) {
	stun := netip.MustParseAddr("203.0.113.5")
	cases := []struct {
		name string
		ext  string
		want bool
	}{
		{"CGNAT: роутер сам за операторским NAT", "100.64.1.1", false},
		{"приватный: двойной NAT", "192.168.1.1", false},
		{"приватный 10/8", "10.0.0.1", false},
		{"link-local", "169.254.1.1", false},
		{"loopback", "127.0.0.1", false},
		{"публичный, но чужой IP — не наш маппинг", "198.51.100.7", false},
		{"публичный и совпал со STUN", "203.0.113.5", true},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			if got := Usable(netip.MustParseAddr(c.ext), stun); got != c.want {
				t.Fatalf("Usable(%s) = %v, ожидали %v", c.ext, got, c.want)
			}
		})
	}
}

// STUN промолчал: проброшенный адрес — единственный источник внешнего адреса,
// поэтому публичный принимаем без сверки.
func TestUsableWithoutStun(t *testing.T) {
	if !Usable(netip.MustParseAddr("203.0.113.5"), netip.Addr{}) {
		t.Fatal("без STUN публичный адрес обязан приниматься")
	}
	if Usable(netip.MustParseAddr("100.64.1.1"), netip.Addr{}) {
		t.Fatal("без STUN CGNAT-адрес всё равно бесполезен")
	}
}
```

- [ ] **Шаг 2: Прогнать — обязан упасть**

```
C:\Users\ivest\go-sdk\go\bin\go.exe test ./internal/portmap/ -v -count=1
```

Ожидается: `FAIL` — пакета нет.

- [ ] **Шаг 3: Реализовать отбраковку**

`internal/portmap/portmap.go`:

```go
// Package portmap просит роутер пробросить UDP-порт: PCP -> NAT-PMP -> UPnP-IGD,
// первый ответивший выигрывает.
//
// Порядок не случаен: PCP (RFC 6887) — единственный, который в принципе может
// работать сквозь операторский CGN; NAT-PMP проще и быстрее; UPnP самый
// распространённый, но самый тяжёлый (SSDP + SOAP + XML).
//
// Зачем это вообще: проброшенный вход endpoint-independent, то есть принимает
// пакет от ЛЮБОГО источника. Это расшивает тупик «port-restricted cone ↔
// симметричный CGNAT», который сегодня лечится только ретранслятором.
package portmap

import "net/netip"

// cgnat — 100.64.0.0/10, адреса операторского NAT (RFC 6598).
var cgnat = netip.MustParsePrefix("100.64.0.0/10")

// Usable — можно ли анонсировать этот внешний адрес.
//
// Отбраковка обязательна: роутер за операторским NAT честно отдаст свой WAN-адрес
// из 100.64/10, а при двойном NAT — приватный. Анонс такого адреса сделает ХУЖЕ,
// чем отсутствие проброса: пиры будут долбиться в мусор вместо рабочих кандидатов.
//
// stunIP пустой = STUN промолчал; тогда проброшенный адрес единственный, и сверять
// его не с чем — достаточно, чтобы он был публичным.
func Usable(ext, stunIP netip.Addr) bool {
	if !ext.IsValid() || ext.IsLoopback() || ext.IsPrivate() ||
		ext.IsLinkLocalUnicast() || ext.IsUnspecified() || cgnat.Contains(ext) {
		return false
	}
	if !stunIP.IsValid() {
		return true
	}
	// IP обязан совпасть со STUN. Расхождение означает, что мы видим не тот NAT,
	// который нас реально выпускает наружу, — двойной NAT.
	return ext == stunIP
}
```

- [ ] **Шаг 4: Прогнать — обязаны пройти**

```
C:\Users\ivest\go-sdk\go\bin\go.exe test ./internal/portmap/ -v -count=1
```

Ожидается: девять `PASS`.

- [ ] **Шаг 5: Реализовать NAT-PMP (самый простой из трёх)**

`internal/portmap/natpmp.go` — запрос к шлюзу на UDP:5351, opcode 1 (UDP-маппинг): версия `0`, опкод `1`, 2 байта резерв, внутренний порт, желаемый внешний порт, время аренды (сек, big-endian). Ответ: версия, опкод `129`, код результата, время с эпохи, внутренний порт, внешний порт, аренда. Разбор ответа вынести в `parseNATPMPResponse([]byte) (netip.AddrPort, time.Duration, error)` — чистая функция, тестируется на записанных байтах без сети.

Тест перед реализацией, в `portmap_test.go`:

```go
func TestParseNATPMPResponse(t *testing.T) {
	// [версия 0][опкод 129][результат 0][время 0][внутр. порт 25000]
	// [внешн. порт 31337][аренда 7200] + внешний адрес отдаётся отдельным
	// запросом опкода 0, поэтому здесь только порт и аренда.
	raw := []byte{0, 129, 0, 0, 0, 0, 0, 0, 0x61, 0xa8, 0x7a, 0x69, 0, 0, 0x1c, 0x20}
	port, lease, err := parseNATPMPResponse(raw)
	if err != nil {
		t.Fatalf("разбор: %v", err)
	}
	if port != 31337 {
		t.Fatalf("внешний порт %d, ожидали 31337", port)
	}
	if lease != 7200*time.Second {
		t.Fatalf("аренда %v, ожидали 2ч", lease)
	}
}
```

- [ ] **Шаг 7: Реализовать PCP (`pcp.go`)**

PCP — RFC 6887, тот же порт UDP:5351, что у NAT-PMP, но версия 2. Запрос MAP: `[версия=2][опкод=1][резерв 2][время аренды 4][адрес клиента 16 (v4-in-v6)][nonce 12][протокол=17 (UDP)][резерв 3][внутренний порт 2][желаемый внешний порт 2][желаемый внешний адрес 16]` — 60 байт. Ответ: `[версия=2][опкод=0x81][резерв][код результата 1]…[назначенный внешний порт][назначенный внешний адрес 16]`.

Разбор — чистая функция, тестируется без сети:

```go
// parsePCPMap разбирает ответ PCP MAP. nonce обязателен к сверке: роутер (или
// кто угодно в локалке) может прислать ответ на чужой запрос, и без сверки мы
// приняли бы его как свой маппинг.
func parsePCPMap(raw []byte, wantNonce [12]byte) (netip.AddrPort, time.Duration, error)
```

Тест перед реализацией — случай «чужой nonce» важнее удачного разбора:

```go
func TestParsePCPRejectsForeignNonce(t *testing.T) {
	var mine, other [12]byte
	mine[0], other[0] = 1, 2
	raw := pcpMapResponse(other, 31337, netip.MustParseAddr("203.0.113.5"), 7200)
	if _, _, err := parsePCPMap(raw, mine); err == nil {
		t.Fatal("ответ с чужим nonce принят как свой")
	}
}
```

`pcpMapResponse` — тестовый помощник, собирающий байты ответа; пишется в том же файле.

- [ ] **Шаг 8: Реализовать UPnP-IGD (`upnp.go`)**

Три шага: SSDP-обнаружение → GET описания устройства → SOAP-вызовы.

SSDP — мультикаст на `239.255.255.250:1900`:

```
M-SEARCH * HTTP/1.1
HOST: 239.255.255.250:1900
MAN: "ssdp:discover"
MX: 2
ST: urn:schemas-upnp-org:device:InternetGatewayDevice:1

```

(строки через `\r\n`, в конце пустая строка). В ответе берётся заголовок `LOCATION:` — URL описания. Дальше GET по нему, из XML достаётся `controlURL` сервиса `WANIPConnection:1` или `WANPPPConnection:1`. Затем два SOAP-вызова: `GetExternalIPAddress` и `AddPortMapping`.

Разбор XML — чистые функции, тесты на записанном ответе реального роутера (снять своим же `natcheck` на шаге задачи 10 и положить в `testdata/`):

```go
func parseControlURL(descXML string) (string, error)
func parseExternalIP(soapXML string) (netip.Addr, error)
```

```go
func TestParseExternalIP(t *testing.T) {
	const resp = `<?xml version="1.0"?><s:Envelope><s:Body>` +
		`<u:GetExternalIPAddressResponse><NewExternalIPAddress>203.0.113.5` +
		`</NewExternalIPAddress></u:GetExternalIPAddressResponse></s:Body></s:Envelope>`
	got, err := parseExternalIP(resp)
	if err != nil {
		t.Fatalf("разбор: %v", err)
	}
	if got != netip.MustParseAddr("203.0.113.5") {
		t.Fatalf("получили %s", got)
	}
}
```

Разбирать XML через `encoding/xml` с узкой структурой под нужное поле, а не регуляркой: ответы роутеров отличаются пространствами имён и переносами, и регулярка на них ломается непредсказуемо.

- [ ] **Шаг 9: Собрать каскад `Run`**

Каскад пробует три протокола **параллельно**, а не по очереди, с общим таймаутом 3 секунды: последовательный опрос стоил бы 3×таймаут на роутере, который не умеет ни одного, а `Run` вызывается на старте узла. Берётся первый пригодный (`Usable`) результат; аренда обновляется на половине срока; при отмене контекста маппинг снимается и зовётся `RemoveInbound`.

Непригодный результат в канал **не отдаётся** — вызывающий не должен повторять проверку у себя, иначе правило отбраковки размажется по двум местам и разъедется.

Шлюз для PCP/NAT-PMP берётся через `iphlpapi.GetBestRoute` (единственный syscall в пакете); UPnP обходится SSDP-мультикастом и шлюз знать не обязан.

- [ ] **Шаг 10: Прогнать всё и закоммитить**

```bash
C:\Users\ivest\go-sdk\go\bin\go.exe test ./... -count=1
git add internal/portmap/
git commit -m "feat(portmap): каскад PCP -> NAT-PMP -> UPnP с арендой"
```

---

## Задача 8: Интеграция проброса — `pickExternal`, брандмауэр, статус

**Files:**
- Modify: `internal/app/session.go:1156` (`pickExternal`), `internal/app/util_test.go`
- Create: `internal/portmap/firewall_windows.go`
- Modify: `cmd/lanmesh-gui/main.go` (поле `PortMap`, статус в `/api/state`), `cmd/lanmesh-gui/web/views/settings.js`

- [ ] **Шаг 1: Дописать тесты `pickExternal` на новый источник**

В `internal/app/util_test.go` (существующие вызовы получают шестым аргументом `""`, ожидания не меняются). Новые:

```go
// STUN промолчал: проброшенный адрес — единственный источник внешнего.
func TestPickExternalPortmapWhenStunSilent(t *testing.T) {
	pm := "203.0.113.5:31337"
	if got := pickExternal("", "", "", "", "", pm); got != pm {
		t.Fatalf("без STUN ожидали %s, получили %s", pm, got)
	}
}

// Симметричный NAT: IP совпал, порт другой — рефлекс врёт, проброс выигрывает.
func TestPickExternalPortmapBeatsReflexOnSymmetric(t *testing.T) {
	pm := "203.0.113.5:31337"
	if got := pickExternal("203.0.113.5:50001", "203.0.113.5:50001", "", "", "203.0.113.5:50002", pm); got != pm {
		t.Fatalf("ожидали проброшенный %s, получили %s", pm, got)
	}
}

// Проброса нет — поведение обязано остаться ровно прежним.
func TestPickExternalUnchangedWithoutPortmap(t *testing.T) {
	a1, a2 := "203.0.113.5:1", "203.0.113.5:2"
	if got := pickExternal(a1, a1, "", "", a2, ""); got != a2 {
		t.Fatalf("без проброса поведение изменилось: %s", got)
	}
}
```

- [ ] **Шаг 2: Прогнать — обязаны упасть на числе аргументов**

```
C:\Users\ivest\go-sdk\go\bin\go.exe test ./internal/app/ -run TestPickExternal -v -count=1
```

- [ ] **Шаг 3: Добавить источник в `pickExternal`**

```go
// portmapExt — адрес, проброшенный на роутере. Он ВНЕ гистерезиса и бьёт
// остальные источники: в отличие от рефлекса, он endpoint-independent (один и
// тот же для всех адресатов), поэтому не участвует в скачках, ради подавления
// которых гистерезис писался, — он их устраняет. Приходит уже отбракованным
// (см. portmap.Usable), так что проверять его здесь нечего.
func pickExternal(cur, stunExt, selfRefl, relayPub, liveStun, portmapExt string) string {
	if portmapExt != "" {
		return portmapExt
	}
	// ... существующее тело без изменений
}
```

- [ ] **Шаг 4: Прогнать — обязаны пройти все, включая старые**

```
C:\Users\ivest\go-sdk\go\bin\go.exe test ./internal/app/ -run TestPickExternal -v -count=1
```

- [ ] **Шаг 5: Правило брандмауэра**

`internal/portmap/firewall_windows.go` — переиспользует подход `runNetsh` из `internal/tun/tun_windows.go:163` (таймаут и подавление окна консоли там уже сделаны):

```go
// Входящее правило брандмауэра — условие работоспособности проброса, а не
// удобство. В паре cone↔CGNAT мы слали на рефлексивный адрес IP:портX, а
// входящий приходит с IP:портY (симметричный NAT выдал другой порт). Для
// брандмауэра это НЕСВЯЗАННЫЙ входящий пакет: роутер перешлёт, Windows выбросит.
// Без правила проброс бесполезен ровно в том сценарии, ради которого затевался.
//
// Правило привязано к program=, а не открывает порт всем желающим, и снимается
// вместе с маппингом: иначе после отказа от фичи в системе оставалось бы
// разрешающее правило, о котором пользователь не знает.
package portmap

import (
	"context"
	"fmt"
	"os"
	"os/exec"
	"syscall"
	"time"
)

// ruleName — по нему же правило и удаляется, поэтому имя фиксированное.
const ruleName = "lanmesh"

func AllowInbound(port int) error {
	exe, err := os.Executable()
	if err != nil {
		return fmt.Errorf("путь к exe: %w", err)
	}
	// Старое правило сносим всегда: порт мог смениться, а netsh при add с тем же
	// именем создаёт ВТОРОЕ правило, а не заменяет первое.
	_ = RemoveInbound()
	return netsh("advfirewall", "firewall", "add", "rule",
		"name="+ruleName, "dir=in", "action=allow", "protocol=UDP",
		fmt.Sprintf("localport=%d", port), "program="+exe, "enable=yes")
}

func RemoveInbound() error {
	return netsh("advfirewall", "firewall", "delete", "rule", "name="+ruleName)
}

// netsh — та же схема, что в internal/tun/tun_windows.go:163: таймаут, чтобы
// зависший netsh не вешал старт узла, и HideWindow, чтобы у GUI-сборки не
// мелькала консоль.
func netsh(args ...string) error {
	ctx, cancel := context.WithTimeout(context.Background(), 10*time.Second)
	defer cancel()
	cmd := exec.CommandContext(ctx, "netsh", args...)
	cmd.SysProcAttr = &syscall.SysProcAttr{HideWindow: true}
	out, err := cmd.CombinedOutput()
	if ctx.Err() != nil {
		return fmt.Errorf("netsh %v: превышен таймаут (10с)", args)
	}
	if err != nil {
		return fmt.Errorf("netsh %v: %w (%s)", args, err, out)
	}
	return nil
}
```

Провал `AllowInbound` **не является фатальным**: узел продолжает работать, проброс остаётся анонсированным, а в статус уходит «брандмауэр блокирует входящие» — ровно строка из матрицы деградации в спеке. Домены с групповой политикой запрещают правила даже администратору, и падать из-за этого нельзя.

- [ ] **Шаг 6: Галка и статус в GUI**

В `Config` — `PortMap *bool \`json:"portMap,omitempty"\`` (указатель по образцу `SendLogs` на строке 94: отсутствие поля читается как «включено», чтобы обновление не выключало фичу молча). Метод `func (c Config) portMap() bool { return c.PortMap == nil || *c.PortMap }`. В `/api/state` — поля `portmap` (строка статуса) и `ipv6` (bool). В `views/settings.js` — галка, в подробном режиме — строка статуса.

- [ ] **Шаг 7: Прогнать всё и закоммитить**

```bash
C:\Users\ivest\go-sdk\go\bin\go.exe test ./... -count=1
git add internal/app/ internal/portmap/ cmd/lanmesh-gui/
git commit -m "feat(app): подключить проброс порта — pickExternal, брандмауэр, статус

Без правки pickExternal проброс не давал бы НИЧЕГО в DHT-режиме: там
анонсируется ровно один порт. Проброшенный адрес вне гистерезиса и бьёт
остальные источники — он endpoint-independent и потому не скачет.

Входящее правило брандмауэра обязательно: в паре cone↔CGNAT входящий
приходит с другого порта, чем тот, куда мы слали, и Windows отбрасывает его
как несвязанный — роутер бы переслал, а система бы выбросила."
```

---

## Задача 9: PEX — обмен адресами пиров между пирами

**Files:**
- Modify: `internal/proto/proto.go:132` (после `FrameHello`)
- Modify: `internal/peer/engine.go` (отправка в `maintenance`, приём в `netToTun`)
- Create: `internal/peer/pex_test.go`

**Interfaces:**
- Produces: `proto.FramePeers byte = 8`; `encodePeers([]netip.AddrPort) []byte`; `decodePeers([]byte) []netip.AddrPort`.

- [ ] **Шаг 1: Написать падающие тесты кодирования**

`internal/peer/pex_test.go`:

```go
package peer

import (
	"net/netip"
	"testing"
)

func TestEncodeDecodePeersRoundTrip(t *testing.T) {
	in := []netip.AddrPort{
		netip.MustParseAddrPort("203.0.113.5:31337"),
		netip.MustParseAddrPort("[2001:db8::1]:25565"),
	}
	got := decodePeers(encodePeers(in))
	if len(got) != len(in) {
		t.Fatalf("получили %d адресов, ожидали %d: %v", len(got), len(in), got)
	}
	for i := range in {
		if got[i] != in[i] {
			t.Fatalf("адрес %d исказился: %s -> %s", i, in[i], got[i])
		}
	}
}

// Потолок: кадр не должен раздуваться под MTU 1280.
func TestEncodePeersCap(t *testing.T) {
	var many []netip.AddrPort
	for i := 0; i < 100; i++ {
		many = append(many, netip.AddrPortFrom(netip.AddrFrom4([4]byte{203, 0, 113, byte(i)}), 1))
	}
	if got := len(decodePeers(encodePeers(many))); got != maxPexEntries {
		t.Fatalf("в кадр попало %d записей, потолок %d", got, maxPexEntries)
	}
}

// На неизвестном семействе разбор останавливается, но уже собранное сохраняется.
// Пропустить запись поштучно нельзя: её длина вычисляется из семейства, и не зная
// семейства, непонятно, откуда читать следующую.
func TestDecodePeersStopsAtUnknownFamily(t *testing.T) {
	// Одна валидная запись, за ней запись с семейством 9.
	frame := encodePeers([]netip.AddrPort{netip.MustParseAddrPort("203.0.113.5:1")})
	frame[0] = 2                          // объявляем две записи
	frame = append(frame, 9, 0, 0, 0)     // вторая — с неизвестным семейством

	got := decodePeers(frame)
	if len(got) != 1 || got[0] != netip.MustParseAddrPort("203.0.113.5:1") {
		t.Fatalf("валидная запись до мусора обязана сохраниться, получили %v", got)
	}
}

// Обрезанный кадр не должен ронять разбор.
func TestDecodePeersTruncated(t *testing.T) {
	frame := encodePeers([]netip.AddrPort{netip.MustParseAddrPort("203.0.113.5:1")})
	if got := decodePeers(frame[:len(frame)-1]); len(got) != 0 {
		t.Fatalf("из обрезанного кадра прочиталось %v", got)
	}
}
```

- [ ] **Шаг 2: Прогнать — обязаны упасть**

```
C:\Users\ivest\go-sdk\go\bin\go.exe test ./internal/peer/ -run TestEncodeDecodePeers -v -count=1
```

- [ ] **Шаг 3: Добавить тип кадра**

`internal/proto/proto.go`, после `FrameHello` (строка 132):

```go
	// FramePeers — PEX: список адресов пиров, которых знает отправитель. Тело:
	// [count:1], затем count раз [family:1][addr:4|16][port:2 big-endian],
	// family = 4 или 6.
	//
	// PeerID сознательно НЕ передаётся: кто окажется за адресом, выяснится при
	// расшифровке, ровно как с адресами из DHT. Это убирает и код сопоставления
	// идентификаторов, и саму возможность наврать про чужой ID.
	//
	// Старые клиенты неизвестный тип молча игнорируют — совместимость цела.
	FramePeers byte = 8
```

- [ ] **Шаг 4: Реализовать кодирование**

В `internal/peer/engine.go`:

```go
// maxPexEntries — потолок записей в кадре PEX. 16×19+1 = 305 байт худшего
// случая (все IPv6) — с запасом влезает в MTU 1280.
const maxPexEntries = 16

func encodePeers(addrs []netip.AddrPort) []byte {
	if len(addrs) > maxPexEntries {
		addrs = addrs[:maxPexEntries]
	}
	out := make([]byte, 0, 1+len(addrs)*19)
	out = append(out, byte(len(addrs)))
	for _, ap := range addrs {
		a := ap.Addr().Unmap()
		if a.Is4() {
			b := a.As4()
			out = append(out, 4)
			out = append(out, b[:]...)
		} else {
			b := a.As16()
			out = append(out, 6)
			out = append(out, b[:]...)
		}
		out = append(out, byte(ap.Port()>>8), byte(ap.Port()))
	}
	return out
}

func decodePeers(p []byte) []netip.AddrPort {
	if len(p) < 1 {
		return nil
	}
	n := int(p[0])
	if n > maxPexEntries {
		n = maxPexEntries
	}
	p = p[1:]
	var out []netip.AddrPort
	for i := 0; i < n; i++ {
		if len(p) < 1 {
			break
		}
		var size int
		switch p[0] {
		case 4:
			size = 4
		case 6:
			size = 16
		default:
			// Неизвестное семейство: длину записи посчитать нельзя, поэтому
			// дальше разбирать нечего — выходим, но уже собранное отдаём.
			return out
		}
		if len(p) < 1+size+2 {
			break
		}
		addr, ok := netip.AddrFromSlice(p[1 : 1+size])
		if ok {
			port := uint16(p[1+size])<<8 | uint16(p[1+size+1])
			out = append(out, netip.AddrPortFrom(addr.Unmap(), port))
		}
		p = p[1+size+2:]
	}
	return out
}
```

- [ ] **Шаг 5: Прогнать тесты кодирования**

```
C:\Users\ivest\go-sdk\go\bin\go.exe test ./internal/peer/ -run "TestEncodeDecodePeers|TestEncodePeersCap|TestDecodePeersSkips" -v -count=1
```

Ожидается: три `PASS`.

- [ ] **Шаг 6: Написать падающий интеграционный тест транзитивности**

Ключевой тест фичи. В `internal/peer/pex_test.go`:

```go
// A связан с B, B связан с C, про C узел A не знает. После раунда PEX адрес C
// должен доехать до A через B — и стать настоящим пиром только после того, как
// придёт кадр, расшифрованный ключом сети.
func TestPexMakesThirdPeerReachable(t *testing.T) {
	sealer, err := crypto.NewSealer(crypto.DeriveNetworkKey("pex", "пароль"))
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}
	a, b, c := newNode(t, sealer), newNode(t, sealer), newNode(t, sealer)
	defer a.conn.Close()
	defer b.conn.Close()
	defer c.conn.Close()

	// Сигналка свела только пары A-B и B-C. Про C узел A не знает.
	a.eng.SyncPeers(testTag, []proto.PeerInfo{b.info("B")})
	b.eng.SyncPeers(testTag, []proto.PeerInfo{a.info("A"), c.info("C")})
	c.eng.SyncPeers(testTag, []proto.PeerInfo{b.info("B")})

	go a.eng.Run()
	go b.eng.Run()
	go c.eng.Run()

	deadline := time.Now().Add(60 * time.Second)
	for time.Now().Before(deadline) {
		for _, v := range a.eng.PeerViews(testTag) {
			if v.VirtualIP == c.ip && v.Status == "direct" {
				return // C доехал через B и пробился
			}
		}
		time.Sleep(500 * time.Millisecond)
	}
	t.Fatalf("C не доехал до A через PEX: %+v", a.eng.PeerViews(testTag))
}
```

- [ ] **Шаг 7: Реализовать отправку и приём**

В `maintenance` рядом с рассылкой `FrameAddr` (`addrGossipTick`, строка 62) — раз в 30 секунд слать подтверждённым пирам `FramePeers` с адресами **только подтверждённых прямых** пиров той же сети (мусор не размножается). В `netToTun` в разборе типов кадров:

```go
		case proto.FramePeers:
			// Прямо в общий пул проб: там уже есть дедуп, потолок maxProbes и
			// отдельная от endpoints очередь. Настоящим пиром адрес станет
			// только когда придёт кадр, расшифрованный ключом сети.
			pexAddrs := make([]string, 0, maxPexEntries)
			for _, ap := range decodePeers(payload) {
				if ap.Addr() == e.selfIP { // свой же адрес долбить незачем
					continue
				}
				pexAddrs = append(pexAddrs, ap.String())
			}
			if len(pexAddrs) > 0 {
				e.AddProbes(nw.tag, pexAddrs)
			}
```

- [ ] **Шаг 8: Прогнать тест транзитивности**

```
C:\Users\ivest\go-sdk\go\bin\go.exe test ./internal/peer/ -run TestPexMakesThird -v -count=1 -timeout 3m
```

Ожидается: `PASS`.

- [ ] **Шаг 9: Прогнать всё и закоммитить**

```bash
C:\Users\ivest\go-sdk\go\bin\go.exe test ./... -count=1
git add internal/proto/ internal/peer/
git commit -m "feat(peer): транзитивный PEX — пиры обмениваются адресами пиров

Кадр несёт только адреса, без PeerID: кто за ними окажется, выяснится при
расшифровке, ровно как с DHT. Так наврать про чужой ID нельзя в принципе,
и не нужен код сопоставления идентификаторов.

Приём идёт прямиком в AddProbes со всей его защитой — дедуп, потолок,
отдельная от endpoints очередь. Сеть теперь переживает падение ВСЕХ
сигналок после первого успешного соединения."
```

---

## Задача 10: Диагностика и README

**Files:**
- Modify: `cmd/natcheck/main.go` (сейчас 76 строк, два шага)
- Modify: `README.md:378` («Ограничения / что дальше»)

- [ ] **Шаг 1: Добавить в natcheck шаг проброса**

После существующего вердикта о типе NAT: попытка `portmap.Run` с таймаутом 5с, печать какого протокола хватило, полученного адреса и результата `Usable` со STUN-рефлексом. Формулировки вердикта — теми же словами, что в панели, чтобы вывод друга можно было сопоставить с его скриншотом.

- [ ] **Шаг 2: Добавить в natcheck проверку IPv6**

Перечислить интерфейсы, напечатать глобальные IPv6-юникасты (или «нативного IPv6 нет»). Это то, ради чего natcheck рассылается друзьям: без цифр непонятно, окупается ли IPv6 в этой группе.

- [ ] **Шаг 3: Прогнать natcheck на своей машине**

```
C:\Users\ivest\go-sdk\go\bin\go.exe run ./cmd/natcheck
```

Ожидается: тип NAT, результат проброса, наличие IPv6. Вывод сохранить — это первая строка натурной статистики.

- [ ] **Шаг 4: Актуализировать README**

Раздел `README.md:378` сейчас устарел: там написано «Нет relay-фолбэка», хотя `cmd/lanmesh-relay` существует и описан выше по тексту. Переписать раздел и добавить описание проброса порта, IPv6 и PEX по образцу существующего раздела про DHT — с объяснением, когда каждое помогает, а когда нет.

- [ ] **Шаг 5: Коммит**

```bash
git add cmd/natcheck/ README.md
git commit -m "feat(natcheck): пробы проброса и IPv6; актуализировать README

natcheck рассылается друзьям — без цифр по их роутерам и провайдерам
непонятно, окупаются ли проброс и IPv6 в этой конкретной группе.

Раздел «Ограничения» в README устарел: там значилось «Нет relay-фолбэка»,
хотя ретранслятор давно есть и описан выше по тексту."
```

---

## Натурная проверка (после всех задач)

Ни один пункт не проверяется в сессии разработки. Не считать выполненным без вывода с живой машины.

- [ ] **cone ↔ CGNAT — главный тест подпроекта.** ПК дома + вторая машина через телефон-точку. Прогнать дважды: с выключенной галкой проброса и с включённой. Ожидается 🔵 релей → 🟢 прямой. Если не произойдёт — задача 7–8 не окупилась, и это надо признать, а не списать на «наверное, у роутера особенности».
- [ ] **Время до адаптера не выросло.** Замер от запуска до появления `25.x.y.z`, до и после изменений.
- [ ] **v4-пробитие цело.** Обычная сеть с другом, как раньше.
- [ ] **PEX по-настоящему.** Собраться втроём, затем выключить сигналки в настройках — сеть обязана жить.
- [ ] **Кэш.** Собраться, закрыть, поднять снова, засечь время до линка.
- [ ] **Статистика.** Разослать `natcheck` друзьям, собрать вывод: скольким реально помогает проброс, у скольких есть нативный IPv6.
