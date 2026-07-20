// Тесты боевой конфигурации сокетов: dual-stack узел против чистого udp4 (самый
// частый случай в бою) и чистый IPv6 (детерминированно на ::1, без сети).
package peer

import (
	"net"
	"net/netip"
	"strconv"
	"strings"
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
	if testing.Short() {
		t.Skip("ждёт пробития NAT до 25с — только полный прогон")
	}

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

	// Это единственная конфигурация в пакете, где ReadFromUDPAddrPort вообще
	// может вернуть IPv4-mapped адрес (v4-пир увиден через dual-stack сокет):
	// характеризационный тест и waitDirect сами по себе от формы адреса не
	// зависят (Status=="direct" ставится независимо от Unmap). Проверяем сам
	// инвариант явно, иначе тест проходит и без нормализации на чтении.
	v := ds.eng.PeerViews(testTag)
	if len(v) != 1 || strings.Contains(v[0].Endpoint, "::ffff:") {
		t.Fatalf("адрес v4-пира не размаплен на чтении (ожидали 127.0.0.1:порт): %+v", v)
	}
}

// Движок обязан сообщать наружу о подтверждённом прямом адресе — на этом
// держится кэш endpoint'ов. Кандидаты не годятся: в кэш должно попадать только
// то, по чему пир реально ответил.
func TestOnDirectConfirmedFires(t *testing.T) {
	if testing.Short() {
		t.Skip("ждёт пробития NAT до 25с — только полный прогон")
	}

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

// Два узла соединяются по IPv6-loopback. Детерминированно: ::1 есть без сети.
func TestPeersOverIPv6Loopback(t *testing.T) {
	if testing.Short() {
		t.Skip("ждёт пробития NAT до 25с — только полный прогон")
	}

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
