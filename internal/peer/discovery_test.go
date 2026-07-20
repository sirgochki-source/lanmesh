package peer

import (
	"net"
	"testing"
	"time"

	"github.com/sirgochki-source/lanmesh/internal/crypto"
	"github.com/sirgochki-source/lanmesh/internal/proto"
)

// Обнаружение БЕЗ сигналки целиком: A знает только голый адрес B (так их отдаёт
// DHT — «ip:port», без PeerID и без имени), B не знает про A вообще ничего.
// Ожидаем: A долбит адрес → B принимает кадр, расшифровывает его ключом сети и
// заводит пира из самого факта расшифровки → отвечает → A точно так же узнаёт B.
// В итоге ОБА видят друг друга как direct и знают имена (FrameHello).
//
// Это и есть проверка того, что связь возможна без единого сервера: SyncPeers тут
// не вызывается ни разу.
func TestLearnPeersFromProbeWithoutSignal(t *testing.T) {
	sealer, err := crypto.NewSealer(crypto.DeriveNetworkKey("тест", "пароль"))
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}
	a, b := newNode(t, sealer), newNode(t, sealer)
	defer a.conn.Close()
	defer b.conn.Close()
	a.eng.SetSelfName("A")
	b.eng.SetSelfName("B")

	// Всё, что известно A, — адрес B. Ровно это отдаёт раунд DHT.
	a.eng.AddProbes(testTag, []string{b.conn.LocalAddr().String()})

	go a.eng.Run()
	go b.eng.Run()

	deadline := time.Now().Add(25 * time.Second)
	for {
		av, bv := a.eng.PeerViews(testTag), b.eng.PeerViews(testTag)
		if len(av) == 1 && len(bv) == 1 && av[0].Status == "direct" && bv[0].Status == "direct" {
			if av[0].VirtualIP != b.ip || bv[0].VirtualIP != a.ip {
				t.Fatalf("узнали не тех: A видит %s (ждали %s), B видит %s (ждали %s)",
					av[0].VirtualIP, b.ip, bv[0].VirtualIP, a.ip)
			}
			// Имя приходит отдельным кадром — оно может отстать от статуса.
			if av[0].Name == "B" && bv[0].Name == "A" {
				return
			}
		}
		if time.Now().After(deadline) {
			t.Fatalf("не сошлись без сигналки: A=%+v B=%+v", av, bv)
		}
		time.Sleep(200 * time.Millisecond)
	}
}

// Свой же кадр (эхо через петлю) не должен заводить «пира-себя».
func TestLearnIgnoresSelf(t *testing.T) {
	sealer, err := crypto.NewSealer(crypto.DeriveNetworkKey("тест", "пароль"))
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}
	a := newNode(t, sealer)
	defer a.conn.Close()

	a.eng.mu.Lock()
	ps := a.eng.learnPeerLocked(a.eng.nets[testTag], a.id)
	a.eng.mu.Unlock()
	if ps != nil {
		t.Fatalf("завели пира из собственного PeerID")
	}
}

// Потолок узнанных из трафика пиров не даёт таблице расти без предела.
func TestLearnedPeersCapped(t *testing.T) {
	sealer, err := crypto.NewSealer(crypto.DeriveNetworkKey("тест", "пароль"))
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}
	a := newNode(t, sealer)
	defer a.conn.Close()

	nw := a.eng.nets[testTag]
	a.eng.mu.Lock()
	defer a.eng.mu.Unlock()
	for i := 0; i < maxLearnedPeers+10; i++ {
		id, err := proto.NewPeerID()
		if err != nil {
			t.Fatalf("id: %v", err)
		}
		a.eng.learnPeerLocked(nw, id)
	}
	if len(nw.peers) != maxLearnedPeers {
		t.Fatalf("узнанных пиров %d, ждали потолок %d", len(nw.peers), maxLearnedPeers)
	}
}

// Голые адреса-кандидаты: дубли не плодятся, потолок соблюдается, мусор
// (неразбираемое, приватные порты) отсеивается.
func TestAddProbesDedupAndCap(t *testing.T) {
	sealer, err := crypto.NewSealer(crypto.DeriveNetworkKey("тест", "пароль"))
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}
	a := newNode(t, sealer)
	defer a.conn.Close()

	a.eng.AddProbes(testTag, []string{"203.0.113.7:5000", "203.0.113.7:5000", "мусор", "203.0.113.8:0"})
	if got := a.eng.ProbeCount(testTag); got != 1 {
		t.Fatalf("кандидатов %d, ждали 1 (дубль и мусор должны отсеяться)", got)
	}

	many := make([]string, 0, maxProbes+20)
	for i := 0; i < maxProbes+20; i++ {
		many = append(many, "198.51.100.1:"+itoa(1000+i))
	}
	a.eng.AddProbes(testTag, many)
	if got := a.eng.ProbeCount(testTag); got != maxProbes {
		t.Fatalf("кандидатов %d, ждали потолок %d", got, maxProbes)
	}
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}

// Регрессия: пира с ЖИВЫМ трафиком нельзя забывать из-за того, что его нет в
// ответе сигналки. Иначе достаточно одному участнику перейти в режим без
// сигналки (или уйти в DHT-сеть), чтобы остальные разорвали ему рабочую связь.
func TestLivePeerSurvivesAbsenceFromSignal(t *testing.T) {
	sealer, err := crypto.NewSealer(crypto.DeriveNetworkKey("тест", "пароль"))
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}
	a, b := newNode(t, sealer), newNode(t, sealer)
	defer a.conn.Close()
	defer b.conn.Close()

	a.eng.SyncPeers(testTag, []proto.PeerInfo{b.info("B")})

	// Пир давно пропал из ответов сигналки, но пакеты от него идут прямо сейчас.
	a.eng.mu.Lock()
	ps := a.eng.nets[testTag].peers[b.id]
	ps.absentSince = time.Now().Add(-10 * peerForget)
	ps.lastRecv = time.Now()
	a.eng.mu.Unlock()

	a.eng.SyncPeers(testTag, nil) // сигналка его не видит
	if len(a.eng.PeerViews(testTag)) != 1 {
		t.Fatalf("живого пира снесли по мнению сигналки")
	}

	// А вот замолчавшего — забываем, как и раньше.
	a.eng.mu.Lock()
	ps.lastRecv = time.Now().Add(-10 * peerForget)
	a.eng.mu.Unlock()
	a.eng.SyncPeers(testTag, nil)
	if len(a.eng.PeerViews(testTag)) != 0 {
		t.Fatalf("молчащего пира не забыли")
	}
}

// Сети без серверов ретранслятор запрещён ПОЛНОСТЬЮ: она не должна слать ему даже
// bind — иначе релей узнал бы её тег, а весь смысл режима в том, что о такой сети
// не знает ни один сервер. Проверяем на настоящем сокете-ретрансляторе: за время
// нескольких тиков bind от запрещённой сети не приходит ни разу, а от разрешённой
// приходит.
func TestRelayForbiddenNetworkNeverBinds(t *testing.T) {
	sealer, err := crypto.NewSealer(crypto.DeriveNetworkKey("тест", "пароль"))
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}
	relay, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("сокет ретранслятора: %v", err)
	}
	defer relay.Close()

	// Тег второй сети — чтобы отличать, чей bind пришёл.
	var openTag [relayTagLen]byte
	copy(openTag[:], []byte("тег-сети-с-разрешённым-релеем-32!!!"))

	a := newNode(t, sealer) // testTag — сеть без серверов
	defer a.conn.Close()
	a.eng.AddNetwork(openTag, sealer, "с релеем")
	a.eng.SetNetworkRelay(testTag, false)
	a.eng.SetNetworkRelay(openTag, true)
	a.eng.UseRelay(relay.LocalAddr().(*net.UDPAddr))

	// Пиры нужны, чтобы движку вообще было ради чего шевелиться.
	b := newNode(t, sealer)
	defer b.conn.Close()
	a.eng.SyncPeers(testTag, []proto.PeerInfo{b.info("B")})
	a.eng.SyncPeers(openTag, []proto.PeerInfo{b.info("B")})

	go a.eng.Run()

	// relayBindTick=20с, но первый bind уходит на первом же тике maintenance.
	relay.SetReadDeadline(time.Now().Add(15 * time.Second))
	sawOpen := false
	buf := make([]byte, 2048)
	for !sawOpen {
		n, _, err := relay.ReadFromUDP(buf)
		if err != nil {
			break // дедлайн
		}
		if n < 1+relayTagLen || buf[0] != relayBind {
			continue
		}
		var tag [relayTagLen]byte
		copy(tag[:], buf[1:1+relayTagLen])
		if tag == testTag {
			t.Fatal("сеть без серверов представилась ретранслятору — тег утёк на сервер")
		}
		if tag == openTag {
			sawOpen = true
		}
	}
	if !sawOpen {
		t.Fatal("сеть с разрешённым релеем к нему не привязалась — тест ничего не доказывает")
	}
}

// probeTTL — абсолютный потолок: повторное появление адреса в раунде DHT НЕ
// продлевает его жизнь. Иначе мусорный/подставленный адрес долбился бы вечно.
func TestProbeTTLNotRefreshedOnReAdd(t *testing.T) {
	sealer, err := crypto.NewSealer(crypto.DeriveNetworkKey("тест", "пароль"))
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}
	a := newNode(t, sealer)
	defer a.conn.Close()

	a.eng.AddProbes(testTag, []string{"203.0.113.7:5000"})
	a.eng.mu.Lock()
	pr := a.eng.nets[testTag].probes["203.0.113.7:5000"]
	first := pr.added
	// Состарим запись почти до TTL, затем «переоткроем» тем же адресом.
	pr.added = pr.added.Add(-probeTTL + time.Second)
	a.eng.nets[testTag].probes["203.0.113.7:5000"] = pr
	aged := pr.added
	a.eng.mu.Unlock()

	a.eng.AddProbes(testTag, []string{"203.0.113.7:5000"}) // повторное появление
	a.eng.mu.Lock()
	got := a.eng.nets[testTag].probes["203.0.113.7:5000"].added
	a.eng.mu.Unlock()
	if !got.Equal(aged) {
		t.Fatalf("added обновился при повторе (%v != %v) — TTL стал скользящим", got, aged)
	}
	_ = first
}

// Backoff растёт с числом попыток и упирается в потолок.
func TestProbeBackoffGrows(t *testing.T) {
	if probeBackoff(0) != probeBackoffBase {
		t.Fatalf("backoff(0)=%v, ждали %v", probeBackoff(0), probeBackoffBase)
	}
	if probeBackoff(1) != 2*probeBackoffBase {
		t.Fatalf("backoff(1)=%v, ждали %v", probeBackoff(1), 2*probeBackoffBase)
	}
	if probeBackoff(100) != probeBackoffMax {
		t.Fatalf("backoff(100)=%v, ждали потолок %v", probeBackoff(100), probeBackoffMax)
	}
	if probeBackoff(3) <= probeBackoff(1) {
		t.Fatalf("backoff не монотонен: b(3)=%v b(1)=%v", probeBackoff(3), probeBackoff(1))
	}
}

// Пир, подтверждённый сигналкой после того как был узнан из трафика, теряет флаг
// learned — иначе его снесёт чистка learnedExpire вопреки подтверждению сигналки.
func TestSyncPeersClearsLearnedFlag(t *testing.T) {
	sealer, err := crypto.NewSealer(crypto.DeriveNetworkKey("тест", "пароль"))
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}
	a, b := newNode(t, sealer), newNode(t, sealer)
	defer a.conn.Close()
	defer b.conn.Close()

	// B узнан из входящего кадра (learned=true).
	a.eng.mu.Lock()
	ps := a.eng.learnPeerLocked(a.eng.nets[testTag], b.id)
	a.eng.mu.Unlock()
	if ps == nil || !ps.learned {
		t.Fatalf("пир не заведён как learned")
	}

	// Теперь сигналка его подтверждает.
	a.eng.SyncPeers(testTag, []proto.PeerInfo{b.info("B")})
	a.eng.mu.Lock()
	stillLearned := a.eng.nets[testTag].peers[b.id].learned
	a.eng.mu.Unlock()
	if stillLearned {
		t.Fatal("learned не сброшен после подтверждения сигналкой")
	}
}
