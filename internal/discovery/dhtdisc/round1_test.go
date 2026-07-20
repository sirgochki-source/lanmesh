package dhtdisc

import (
	"bytes"
	"context"
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/anacrolix/dht/v2"
	alog "github.com/anacrolix/log"
	peer_store "github.com/anacrolix/dht/v2/peer-store"
)

// sniffConn — обёртка над UDP-сокетом, считающая входящие KRPC-запросы по сырым
// байтам bencode. Позволяет проверить, реально ли инициатор передал нам
// announce_peer/get_peers, минуя всю логику ответов библиотеки.
type sniffConn struct {
	net.PacketConn
	announceQueries int32
	getPeersQueries int32
}

func (c *sniffConn) ReadFrom(p []byte) (int, net.Addr, error) {
	n, addr, err := c.PacketConn.ReadFrom(p)
	if n > 0 {
		buf := p[:n]
		if bytes.Contains(buf, []byte("13:announce_peer")) {
			atomic.AddInt32(&c.announceQueries, 1)
		}
		if bytes.Contains(buf, []byte("9:get_peers")) {
			atomic.AddInt32(&c.getPeersQueries, 1)
		}
	}
	return n, addr, err
}

func newLoopbackServer(t *testing.T, starting func() ([]dht.Addr, error)) (*dht.Server, *sniffConn) {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1)})
	if err != nil {
		t.Fatal(err)
	}
	sc := &sniffConn{PacketConn: conn}
	cfg := dht.NewDefaultServerConfig()
	cfg.Conn = sc
	cfg.Logger = alog.Default.FilterLevel(alog.Warning)
	// PeerStore нужен, чтобы get_peers-ответы несли token; без него обход
	// отбраковывает отвечающего, и он не становится целью announce_peer — тогда
	// тест ничего бы не доказал.
	cfg.PeerStore = &peer_store.InMemory{}
	if starting != nil {
		cfg.StartingNodes = starting
	} else {
		cfg.StartingNodes = func() ([]dht.Addr, error) { return nil, nil }
	}
	srv, err := dht.NewServer(cfg)
	if err != nil {
		t.Fatal(err)
	}
	t.Cleanup(srv.Close)
	return srv, sc
}

// Offline-проверка round1 без выхода в интернет (то, что раньше гонял только
// opt-in live-тест): реальный round1 против loopback-узла B ДОЛЖЕН доставить B
// наш announce_peer с указанным портом. Регрессия на найденный ревью баг, где
// defer a.Close() отменял announce по таймауту поиска — теперь round1 сперва
// гасит только обход и даёт announce-фазе уйти (см. finishAnnounce в round1).
func TestRound1SendsAnnounceOffline(t *testing.T) {
	B, bSniff := newLoopbackServer(t, nil)

	var ih [20]byte
	copy(ih[:], []byte("round1-offline-announce-check!!!"))

	d := &Discoverer{srv: mustServerToward(t, B)}
	const port = 54321
	if _, err := d.round1(context.Background(), ih, port); err != nil {
		t.Fatalf("round1: %v", err)
	}
	// announce-фаза асинхронна — дадим ей дойти до сокета B.
	deadline := time.Now().Add(3 * time.Second)
	for time.Now().Before(deadline) {
		if atomic.LoadInt32(&bSniff.announceQueries) > 0 {
			break
		}
		time.Sleep(50 * time.Millisecond)
	}

	if atomic.LoadInt32(&bSniff.getPeersQueries) == 0 {
		t.Fatal("B не получил ни одного get_peers — обход не дошёл до узла, тест не показателен")
	}
	if atomic.LoadInt32(&bSniff.announceQueries) == 0 {
		t.Fatal("B не получил announce_peer — наш анонс не ушёл в DHT (round1 обрывает announce)")
	}
}

// port=0 означает «только искать, себя не анонсировать» — announce_peer уходить
// НЕ должен, а поиск (get_peers) — должен.
func TestRound1PortZeroDoesNotAnnounce(t *testing.T) {
	B, bSniff := newLoopbackServer(t, nil)

	var ih [20]byte
	copy(ih[:], []byte("round1-search-only-no-announce!!"))

	d := &Discoverer{srv: mustServerToward(t, B)}
	if _, err := d.round1(context.Background(), ih, 0); err != nil {
		t.Fatalf("round1: %v", err)
	}
	time.Sleep(1 * time.Second)

	if atomic.LoadInt32(&bSniff.getPeersQueries) == 0 {
		t.Fatal("B не получил get_peers — поиск не работает")
	}
	if got := atomic.LoadInt32(&bSniff.announceQueries); got != 0 {
		t.Fatalf("при port=0 announce_peer не должен уходить, а B получил %d", got)
	}
}

// mustServerToward поднимает DHT-сервер, стартующий обход с узла toward.
func mustServerToward(t *testing.T, toward *dht.Server) *dht.Server {
	t.Helper()
	srv, _ := newLoopbackServer(t, func() ([]dht.Addr, error) {
		return []dht.Addr{dht.NewAddr(toward.Addr())}, nil
	})
	return srv
}
