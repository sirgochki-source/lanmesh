package peer

import (
	"net"
	"testing"
	"time"

	"github.com/sirgochki-source/lanmesh/internal/crypto"
	"github.com/sirgochki-source/lanmesh/internal/proto"
)

// fakeRelay — минимальная реализация протокола ретранслятора для тестов
// (см. cmd/lanmesh-relay). Проверяем клиентскую сторону, поэтому сервер тут
// нарочно тупой: таблица bind'ов и пересылка.
type fakeRelay struct {
	conn *net.UDPConn
	t    *testing.T
}

func startFakeRelay(t *testing.T) *fakeRelay {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("сокет ретранслятора: %v", err)
	}
	r := &fakeRelay{conn: conn, t: t}
	go r.run()
	return r
}

func (r *fakeRelay) addr() *net.UDPAddr { return r.conn.LocalAddr().(*net.UDPAddr) }

func (r *fakeRelay) run() {
	type key struct {
		tag [32]byte
		id  [16]byte
	}
	table := map[key]*net.UDPAddr{}

	buf := make([]byte, 2048)
	for {
		n, src, err := r.conn.ReadFromUDP(buf)
		if err != nil {
			return // сокет закрыт — тест кончился
		}
		pkt := buf[:n]
		if len(pkt) < 1 {
			continue
		}
		switch pkt[0] {
		case relayBind:
			if len(pkt) < 1+32+16 {
				continue
			}
			var k key
			copy(k.tag[:], pkt[1:33])
			copy(k.id[:], pkt[33:49])
			table[k] = src
			// Расширенный ack: как настоящий релей, дописываем адрес клиента —
			// STUN с нашего сервера на боевом сокете.
			ack := append([]byte{relayBindOK}, pkt[1:49]...)
			ack = append(ack, []byte(src.String())...)
			r.conn.WriteToUDP(ack, src)

		case relayData:
			if len(pkt) < 1+32+16 {
				continue
			}
			var k key
			copy(k.tag[:], pkt[1:33])
			copy(k.id[:], pkt[33:49])
			dst, ok := table[k]
			if !ok {
				continue
			}
			out := append([]byte{relayForward}, pkt[49:]...)
			r.conn.WriteToUDP(out, dst)
		}
	}
}

// blackhole — заведомо недостижимый endpoint: TEST-NET-1 из RFC 5737, туда нет
// маршрута и ICMP оттуда не прилетит. Нужен, чтобы прямое пробитие ГАРАНТИРОВАННО
// провалилось и пиры были вынуждены пойти через ретранслятор.
func blackhole(t *testing.T) string {
	t.Helper()
	return "192.0.2.1:9"
}

// Пиры, которые не могут пробиться напрямую, обязаны сойтись через ретранслятор.
// Это случай симметричного NAT и мобильного CGNAT — ради него relay и сделан.
func TestPeersFallBackToRelay(t *testing.T) {
	sealer, err := crypto.NewSealer(crypto.DeriveNetworkKey("тест", "пароль"))
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}

	relay := startFakeRelay(t)
	defer relay.conn.Close()

	a, b := newNode(t, sealer), newNode(t, sealer)
	defer a.conn.Close()
	defer b.conn.Close()

	a.eng.UseRelay(relay.addr())
	b.eng.UseRelay(relay.addr())

	// Кандидаты ведут в никуда — прямой путь не откроется.
	ai, bi := a.info("A"), b.info("B")
	ai.Endpoints = []string{blackhole(t)}
	bi.Endpoints = []string{blackhole(t)}
	a.eng.SyncPeers(testTag, []proto.PeerInfo{bi})
	b.eng.SyncPeers(testTag, []proto.PeerInfo{ai})

	go a.eng.Run()
	go b.eng.Run()

	// relayGrace=6с даётся на честные попытки пробиться, дальше идёт relay.
	deadline := time.Now().Add(25 * time.Second)
	for {
		v := a.eng.PeerViews(testTag)
		if len(v) == 1 && v[0].Status == "relay" && v[0].RttMs >= 0 {
			if v[0].VirtualIP != b.ip {
				t.Fatalf("не тот пир: %+v", v[0])
			}
			return
		}
		if len(v) == 1 && v[0].Status == "direct" {
			t.Fatalf("пробились НАПРЯМУЮ через blackhole — так не бывает: %+v", v[0])
		}
		if time.Now().After(deadline) {
			t.Fatalf("не дошли до relay, последнее состояние: %+v", v)
		}
		time.Sleep(250 * time.Millisecond)
	}
}

// Протухший прямой путь обязан уводить ДАННЫЕ на ретранслятор, а не в чёрную
// дыру. Регрессия на баг из боя: sendFrame слал на ps.active, сверяя лишь
// active != nil, без свежести — в отличие от статуса и maintenance. Когда NAT
// перевешивал порт (у мобильных операторов при переподключении — норма), active
// торчал на мёртвом адресе: панель честно уходила на relay (она сверяет
// lastRecv), а трафик Minecraft продолжал литься в дохлый прямой endpoint и на
// relay для данных не переключался. Симптом: «было direct — норм, свалилось на
// relay — сервер пропал даже из списка».
func TestStaleDirectPathRoutesDataViaRelay(t *testing.T) {
	sealer, err := crypto.NewSealer(crypto.DeriveNetworkKey("тест", "пароль"))
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}

	// «Ретранслятор» — простой приёмник: смотрим, ушёл ли туда relayData.
	probe, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatalf("probe: %v", err)
	}
	defer probe.Close()

	a := newNode(t, sealer)
	defer a.conn.Close()
	b := newNode(t, sealer) // только ради валидного id/ip — не запускаем
	defer b.conn.Close()

	a.eng.UseRelay(probe.LocalAddr().(*net.UDPAddr))

	bi := b.info("B")
	bi.Endpoints = []string{"192.0.2.1:9"} // мёртвый прямой адрес (RFC 5737)
	a.eng.SyncPeers(testTag, []proto.PeerInfo{bi})

	// Приводим пира в состояние «прямой путь протух»: active выставлен на мёртвый
	// адрес, пакетов оттуда давно не было, а relay уже разрешён (firstSeen стар).
	dead := &net.UDPAddr{IP: net.IPv4(192, 0, 2, 1), Port: 9}
	var ps *peerState
	a.eng.mu.Lock()
	for _, p := range a.eng.nets[testTag].peers {
		ps = p
	}
	ps.active = dead
	ps.lastRecv = time.Now().Add(-2 * peerTimeout)
	ps.firstSeen = time.Now().Add(-2 * relayGrace)
	a.eng.mu.Unlock()

	// Отправляем данные. Протухший active НЕ должен увести пакет в чёрную дыру.
	a.eng.sendFrame(ps, proto.FrameData, []byte("payload"))

	probe.SetReadDeadline(time.Now().Add(2 * time.Second))
	buf := make([]byte, 2048)
	n, _, err := probe.ReadFromUDP(buf)
	if err != nil {
		t.Fatalf("данные не ушли на relay при протухшем прямом пути: %v", err)
	}
	if buf[0] != relayData {
		t.Fatalf("на relay пришёл не relayData: % x", buf[:min(n, 8)])
	}

	// Контроль: со СВЕЖИМ прямым путём данные идут напрямую (в мёртвый адрес),
	// на relay не прилетает ничего — иначе мы бы гоняли лишний трафик через сервер.
	a.eng.mu.Lock()
	ps.lastRecv = time.Now()
	a.eng.mu.Unlock()
	a.eng.sendFrame(ps, proto.FrameData, []byte("payload2"))

	probe.SetReadDeadline(time.Now().Add(500 * time.Millisecond))
	if n2, _, err := probe.ReadFromUDP(buf); err == nil {
		t.Fatalf("свежий прямой путь ушёл на relay (% x) — не должен", buf[:min(n2, 8)])
	}
}

// Пакет, пришедший через ретранслятор, НЕ должен засчитываться как пробитый
// прямой путь: иначе движок решит, что дырка открыта, перестанет долбить
// кандидаты и начнёт слать данные на адрес ретранслятора как на прямой.
func TestRelayPacketDoesNotConfirmDirectPath(t *testing.T) {
	sealer, _ := crypto.NewSealer(crypto.DeriveNetworkKey("тест", "пароль"))

	relay := startFakeRelay(t)
	defer relay.conn.Close()

	a, b := newNode(t, sealer), newNode(t, sealer)
	defer a.conn.Close()
	defer b.conn.Close()

	a.eng.UseRelay(relay.addr())
	b.eng.UseRelay(relay.addr())

	bi := b.info("B")
	bi.Endpoints = []string{blackhole(t)}
	ai := a.info("A")
	ai.Endpoints = []string{blackhole(t)}
	a.eng.SyncPeers(testTag, []proto.PeerInfo{bi})
	b.eng.SyncPeers(testTag, []proto.PeerInfo{ai})

	go a.eng.Run()
	go b.eng.Run()

	deadline := time.Now().Add(25 * time.Second)
	for time.Now().Before(deadline) {
		v := a.eng.PeerViews(testTag)
		if len(v) == 1 && v[0].Status == "relay" {
			a.eng.mu.RLock()
			var active *net.UDPAddr
			for _, ps := range a.eng.nets[testTag].peers {
				active = ps.active
			}
			a.eng.mu.RUnlock()
			if active != nil {
				t.Fatalf("пакет через relay подтвердил прямой endpoint %v — это баг", active)
			}
			return
		}
		time.Sleep(250 * time.Millisecond)
	}
	t.Fatal("не дождались relay-статуса")
}

// Движок обязан распарсить наш внешний адрес из расширенного bind-ack релея
// (STUN с нашего сервера) и отдать его через RelayReflex. Пиров тут нет —
// проверяем именно опорную bind/ack-петлю с ретранслятором.
func TestRelayReflexFromExtendedAck(t *testing.T) {
	sealer, err := crypto.NewSealer(crypto.DeriveNetworkKey("тест", "пароль"))
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}

	relay := startFakeRelay(t)
	defer relay.conn.Close()

	a := newNode(t, sealer)
	defer a.conn.Close()

	a.eng.UseRelay(relay.addr())

	go a.eng.Run()

	// bind уходит на первом тике maintenance (~punchInterval), ack прилетает сразу.
	want := a.conn.LocalAddr().String() // релей видит нас именно с этого адреса
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if got, ok := a.eng.RelayReflex(); ok {
			if got != want {
				t.Fatalf("relayReflex=%q, ждали %q", got, want)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("движок не получил relayReflex из расширенного ack")
}
