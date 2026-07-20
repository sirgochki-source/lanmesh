package peer

import (
	"io"
	"net"
	"net/netip"
	"testing"
	"time"

	"github.com/sirgochki-source/lanmesh/internal/crypto"
	"github.com/sirgochki-source/lanmesh/internal/proto"
)

// fakeTUN подменяет виртуальный адаптер: Read висит до закрытия, Write копит
// пакеты. Движку хватает, а прав администратора и Wintun не нужно.
type fakeTUN struct {
	read  chan []byte
	wrote chan []byte
}

func newFakeTUN() *fakeTUN {
	return &fakeTUN{read: make(chan []byte), wrote: make(chan []byte, 16)}
}

func (f *fakeTUN) Read(buf []byte) (int, error) {
	p, ok := <-f.read
	if !ok {
		return 0, io.EOF
	}
	return copy(buf, p), nil
}

func (f *fakeTUN) Write(pkt []byte) (int, error) {
	cp := append([]byte(nil), pkt...)
	select {
	case f.wrote <- cp:
	default: // тест не читает — не блокируем движок
	}
	return len(pkt), nil
}

// testTag — общий тег сети для тестов (значение неважно, важно чтобы у обоих узлов
// совпадало: по нему их сводит fakeRelay и по нему же работают SyncPeers/PeerViews).
var testTag = func() [relayTagLen]byte {
	var t [relayTagLen]byte
	copy(t[:], []byte("тег-сети-для-теста-32-байта-ровно!!"))
	return t
}()

// node — один участник для теста. Держит свой тег сети для вызовов API.
type node struct {
	eng  *Engine
	conn *net.UDPConn
	tun  *fakeTUN
	id   proto.PeerID
	ip   string
	tag  [relayTagLen]byte
}

func newNode(t *testing.T, sealer *crypto.Sealer) *node {
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
	tun := newFakeTUN()
	eng := NewEngine(conn, tun, id, ip)
	eng.AddNetwork(testTag, sealer, "test")
	return &node{eng: eng, conn: conn, tun: tun, id: id, ip: ip.String(), tag: testTag}
}

func (n *node) info(name string) proto.PeerInfo {
	return proto.PeerInfo{
		PeerID:    n.id.String(),
		Name:      name,
		VirtualIP: n.ip,
		Endpoints: []string{n.conn.LocalAddr().String()},
	}
}

// Два движка на localhost должны пробиться друг к другу и померить задержку.
// Проверяет разом: punch -> подтверждение endpoint'а -> ping/pong -> RTT.
func TestPeersPunchAndMeasureRTT(t *testing.T) {
	sealer, err := crypto.NewSealer(crypto.DeriveNetworkKey("тест", "пароль"))
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}

	a, b := newNode(t, sealer), newNode(t, sealer)
	defer a.conn.Close()
	defer b.conn.Close()

	a.eng.SyncPeers(testTag, []proto.PeerInfo{b.info("B")})
	b.eng.SyncPeers(testTag, []proto.PeerInfo{a.info("A")})

	go a.eng.Run()
	go b.eng.Run()

	// punchInterval=2с, ping шлётся со следующего тика после подтверждения.
	deadline := time.Now().Add(20 * time.Second)
	for {
		v := a.eng.PeerViews(testTag)
		if len(v) == 1 && v[0].Status == "direct" && v[0].RttMs >= 0 {
			if v[0].RttMs > 1000 {
				t.Fatalf("RTT на localhost = %v мс — похоже, меряем не то", v[0].RttMs)
			}
			if v[0].Name != "B" || v[0].VirtualIP != b.ip {
				t.Fatalf("не тот пир: %+v", v[0])
			}
			return // дошли: пробились и померили
		}
		if time.Now().After(deadline) {
			a.eng.mu.RLock()
			for _, ps := range a.eng.nets[testTag].peers {
				t.Logf("A: seq=%d pingAt=%v pingSent=%v rtt=%v rttAt=%v active=%v",
					ps.pingSeq, ps.pingAt, ps.pingSent, ps.rtt, ps.rttAt, ps.active)
			}
			a.eng.mu.RUnlock()
			b.eng.mu.RLock()
			for _, ps := range b.eng.nets[testTag].peers {
				t.Logf("B: seq=%d pingAt=%v pingSent=%v rtt=%v active=%v",
					ps.pingSeq, ps.pingAt, ps.pingSent, ps.rtt, ps.active)
			}
			b.eng.mu.RUnlock()
			t.Fatalf("не дождались RTT, последнее состояние: %+v", v)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// Осечка сигналки не должна рвать уже работающее соединение.
//
// Регрессия на баг из боя: Durable Object выгружался из памяти по простою и
// возвращал ПУСТОЙ список участников. SyncPeers сносил peerState вместе с
// пробитым endpoint'ом — связь рвалась, движок начинал пробиваться заново и
// уходил на ретранслятор. Один пустой ответ не должен ничего значить.
func TestSignalHiccupDoesNotDropPeer(t *testing.T) {
	sealer, err := crypto.NewSealer(crypto.DeriveNetworkKey("тест", "пароль"))
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}
	a, b := newNode(t, sealer), newNode(t, sealer)
	defer a.conn.Close()
	defer b.conn.Close()

	a.eng.SyncPeers(testTag, []proto.PeerInfo{b.info("B")})
	b.eng.SyncPeers(testTag, []proto.PeerInfo{a.info("A")})
	go a.eng.Run()
	go b.eng.Run()

	// Дожидаемся именно ПРОБИТОГО соединения — иначе терять будет нечего.
	deadline := time.Now().Add(20 * time.Second)
	for {
		v := a.eng.PeerViews(testTag)
		if len(v) == 1 && v[0].Status == "direct" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("пир не пробился: %+v", v)
		}
		time.Sleep(200 * time.Millisecond)
	}

	a.eng.mu.RLock()
	var activeBefore netip.AddrPort
	for _, ps := range a.eng.nets[testTag].peers {
		activeBefore = ps.active
	}
	a.eng.mu.RUnlock()
	if !activeBefore.IsValid() {
		t.Fatal("endpoint не подтверждён, тест бессмысленен")
	}

	// Сигналка моргнула: пустой список. Дважды подряд — мало ли.
	a.eng.SyncPeers(testTag, nil)
	a.eng.SyncPeers(testTag, nil)

	v := a.eng.PeerViews(testTag)
	if len(v) != 1 {
		t.Fatalf("пир исчез после осечки сигналки: %+v", v)
	}
	if v[0].Status != "direct" {
		t.Fatalf("соединение развалилось после осечки сигналки: статус %q", v[0].Status)
	}

	a.eng.mu.RLock()
	var activeAfter netip.AddrPort
	for _, ps := range a.eng.nets[testTag].peers {
		activeAfter = ps.active
	}
	a.eng.mu.RUnlock()
	if !activeAfter.IsValid() || activeAfter.String() != activeBefore.String() {
		t.Fatalf("потерян пробитый endpoint: было %v, стало %v", activeBefore, activeAfter)
	}

	// А вот пир, которого сигналка долго не отдаёт И от которого нет пакетов,
	// должен-таки забыться. Молчание тут обязательно: пока трафик идёт, пир
	// остаётся в таблице несмотря на сигналку (см. TestLivePeerSurvivesAbsence-
	// FromSignal) — иначе участник, переставший регистрироваться, терял бы живую
	// связь. Поэтому сначала глушим B, потом отматываем время.
	b.conn.Close()
	a.eng.mu.Lock()
	for _, ps := range a.eng.nets[testTag].peers {
		ps.absentSince = time.Now().Add(-peerForget - time.Second)
		ps.lastRecv = time.Now().Add(-peerForget - time.Second)
		ps.lastRelayRecv = time.Time{}
	}
	a.eng.mu.Unlock()
	a.eng.SyncPeers(testTag, nil)
	if v := a.eng.PeerViews(testTag); len(v) != 0 {
		t.Fatalf("пир не забылся спустя peerForget: %+v", v)
	}
}

// maintenance обязана умирать вместе с движком.
//
// Регрессия на баг, пойманный в бою по удалённым логам: горутина переживала
// отключение сети и продолжала тикать в закрытый сокет — вечно, и по одной
// лишней на каждое переподключение. В логе это выглядело как бесконечный поток
// "use of closed network connection" спустя 15 минут после "сеть отключена",
// который вдобавок вытеснял из буфера диагностики всё полезное.
func TestMaintenanceStopsWithEngine(t *testing.T) {
	sealer, err := crypto.NewSealer(crypto.DeriveNetworkKey("тест", "пароль"))
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}

	a, b := newNode(t, sealer), newNode(t, sealer)
	defer b.conn.Close()

	a.eng.SyncPeers(testTag, []proto.PeerInfo{b.info("B")})
	b.eng.SyncPeers(testTag, []proto.PeerInfo{a.info("A")})
	go a.eng.Run()
	go b.eng.Run()

	// Ждём подтверждения пира: пинги (а значит и рост pingSeq) идут только
	// подтверждённым — на неподтверждённых счётчик стоял бы и тест прошёл бы зря.
	deadline := time.Now().Add(20 * time.Second)
	for {
		v := a.eng.PeerViews(testTag)
		if len(v) == 1 && v[0].Status == "direct" && v[0].RttMs >= 0 {
			break
		}
		if time.Now().After(deadline) {
			t.Fatalf("пир не подтвердился, тест бессмысленен: %+v", v)
		}
		time.Sleep(200 * time.Millisecond)
	}

	// Останавливаем движок так же, как это делает Session.Stop.
	a.conn.Close()
	time.Sleep(1 * time.Second) // даём Run() вернуться и закрыть done

	seqOf := func() uint64 {
		a.eng.mu.RLock()
		defer a.eng.mu.RUnlock()
		for _, ps := range a.eng.nets[testTag].peers {
			return ps.pingSeq
		}
		return 0
	}

	before := seqOf()
	// pingInterval=5с: если maintenance жива, за 8с она пнёт хотя бы раз.
	time.Sleep(8 * time.Second)
	if after := seqOf(); after != before {
		t.Fatalf("maintenance пережила остановку движка: pingSeq %d -> %d", before, after)
	}
}

// SettledForPolling управляет темпом регистрации: медленно, когда всё на свежем
// прямом пути или пиров нет; быстро, когда кто-то ищется/замолчал.
func TestSettledForPolling(t *testing.T) {
	sealer, _ := crypto.NewSealer(crypto.DeriveNetworkKey("тест", "пароль"))
	a := newNode(t, sealer)
	defer a.conn.Close()
	b := newNode(t, sealer)
	defer b.conn.Close()

	if !a.eng.SettledForPolling() {
		t.Fatal("без пиров сеть считается устаканенной")
	}

	// Пир без подтверждённого прямого пути — темп быстрый.
	a.eng.SyncPeers(testTag, []proto.PeerInfo{b.info("B")})
	if a.eng.SettledForPolling() {
		t.Fatal("неподтверждённый пир — не settled")
	}

	// Свежий прямой путь — можно реже.
	a.eng.mu.Lock()
	var ps *peerState
	for _, p := range a.eng.nets[testTag].peers {
		ps = p
	}
	ps.active = netip.AddrPortFrom(netip.AddrFrom4([4]byte{127, 0, 0, 1}), 1)
	ps.lastRecv = time.Now()
	a.eng.mu.Unlock()
	if !a.eng.SettledForPolling() {
		t.Fatal("свежий прямой путь — должно быть settled")
	}

	// Замолчал дольше staleProbe — снова быстрый темп (для переоткрытия).
	a.eng.mu.Lock()
	ps.lastRecv = time.Now().Add(-staleProbe - time.Second)
	a.eng.mu.Unlock()
	if a.eng.SettledForPolling() {
		t.Fatal("замолчавший подтверждённый пир — не settled")
	}
}

// Кодек FrameAddr: reflex + кандидаты туда-обратно, плюс устойчивость к мусору.
func TestAddrCodec(t *testing.T) {
	reflex := "203.0.113.7:5000"
	cands := []string{"198.51.100.9:41000", "10.0.0.2:25000"}
	r, c := decodeAddr(encodeAddr(reflex, cands))
	if r != reflex {
		t.Fatalf("reflex: %q", r)
	}
	if len(c) != 2 || c[0] != cands[0] || c[1] != cands[1] {
		t.Fatalf("кандидаты: %v", c)
	}

	if r, c := decodeAddr(encodeAddr("", nil)); r != "" || len(c) != 0 {
		t.Fatalf("пусто: %q %v", r, c)
	}
	decodeAddr([]byte{0xff, 0x01}) // обрезанный мусор не должен паниковать
}

// FrameAddr от пира: учим свой внешний адрес (reflex) и доливаем кандидаты пира.
func TestHandleAddrReflexAndCandidates(t *testing.T) {
	sealer, _ := crypto.NewSealer(crypto.DeriveNetworkKey("тест", "пароль"))
	a := newNode(t, sealer)
	defer a.conn.Close()
	b := newNode(t, sealer)
	defer b.conn.Close()

	kick := a.eng.ReflexNotify()
	a.eng.SyncPeers(testTag, []proto.PeerInfo{b.info("B")})

	a.eng.mu.RLock()
	var ps *peerState
	for _, p := range a.eng.nets[testTag].peers {
		ps = p
	}
	before := len(ps.endpoints)
	a.eng.mu.RUnlock()

	a.eng.handleAddr(ps, "203.0.113.7:5000", []string{"198.51.100.9:41000"})

	if r, ok := a.eng.SelfReflex(); !ok || r != "203.0.113.7:5000" {
		t.Fatalf("reflex не выучен: %q %v", r, ok)
	}
	select {
	case <-kick:
	default:
		t.Fatal("смена reflex должна будить перерегистрацию")
	}
	a.eng.mu.RLock()
	after := len(ps.endpoints)
	a.eng.mu.RUnlock()
	if after != before+1 {
		t.Fatalf("кандидат не долит: было %d, стало %d", before, after)
	}

	// Тот же reflex второй раз перерегистрацию не будит.
	a.eng.handleAddr(ps, "203.0.113.7:5000", nil)
	select {
	case <-kick:
		t.Fatal("повтор того же reflex не должен будить")
	default:
	}
}

// Ответ с чужим номером не должен считаться за наш ping: иначе задержка
// окажется завышенной (или отрицательной).
func TestNotePongIgnoresWrongSeq(t *testing.T) {
	sealer, _ := crypto.NewSealer(crypto.DeriveNetworkKey("тест", "пароль"))
	n := newNode(t, sealer)
	defer n.conn.Close()

	ps := &peerState{pingSeq: 7, pingAt: time.Now().Add(-50 * time.Millisecond)}

	n.eng.notePong(ps, 6) // номер не наш
	if ps.rtt != 0 {
		t.Fatalf("чужой номер учтён: rtt=%v", ps.rtt)
	}

	n.eng.notePong(ps, 7) // наш
	if ps.rtt <= 0 {
		t.Fatalf("свой номер не учтён: rtt=%v", ps.rtt)
	}

	// Повтор того же ответа не должен пересчитывать RTT по обнулённому времени.
	was := ps.rtt
	n.eng.notePong(ps, 7)
	if ps.rtt != was {
		t.Fatalf("дубль ответа пересчитал RTT: было %v, стало %v", was, ps.rtt)
	}
}
