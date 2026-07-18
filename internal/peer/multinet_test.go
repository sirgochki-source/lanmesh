package peer

import (
	"net"
	"testing"
	"time"

	"github.com/sirgochki-source/lanmesh/internal/crypto"
	"github.com/sirgochki-source/lanmesh/internal/proto"
)

// newBareNode — узел с движком БЕЗ сетей (сети добавляем в тесте вручную).
func newBareNode(t *testing.T) *node {
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
	return &node{eng: NewEngine(conn, tun, id, ip), conn: conn, tun: tun, id: id, ip: ip.String()}
}

// Один движок в ДВУХ сетях сразу: должен пробиться к пиру каждой сети по её ключу,
// таблицы сетей изолированы (пир одной сети не виден в другой), а разные ключи
// исключают перекрёстную расшифровку. Ядро одновременной мультисети.
func TestMultiNetworkIsolationAndRouting(t *testing.T) {
	sx, err := crypto.NewSealer(crypto.DeriveNetworkKey("netX", "px"))
	if err != nil {
		t.Fatalf("sealer X: %v", err)
	}
	sy, err := crypto.NewSealer(crypto.DeriveNetworkKey("netY", "py"))
	if err != nil {
		t.Fatalf("sealer Y: %v", err)
	}
	var tagX, tagY [relayTagLen]byte
	copy(tagX[:], []byte("XXXXXXXXXXXXXXXXXXXXXXXXXXXXXXXX"))
	copy(tagY[:], []byte("YYYYYYYYYYYYYYYYYYYYYYYYYYYYYYYY"))

	// A — в обеих сетях; B только в X, C только в Y.
	a := newBareNode(t)
	a.eng.AddNetwork(tagX, sx, "X")
	a.eng.AddNetwork(tagY, sy, "Y")
	b := newBareNode(t)
	b.eng.AddNetwork(tagX, sx, "X")
	c := newBareNode(t)
	c.eng.AddNetwork(tagY, sy, "Y")
	defer a.conn.Close()
	defer b.conn.Close()
	defer c.conn.Close()

	a.eng.SyncPeers(tagX, []proto.PeerInfo{b.info("B")})
	a.eng.SyncPeers(tagY, []proto.PeerInfo{c.info("C")})
	b.eng.SyncPeers(tagX, []proto.PeerInfo{a.info("A")})
	c.eng.SyncPeers(tagY, []proto.PeerInfo{a.info("A")})

	go a.eng.Run()
	go b.eng.Run()
	go c.eng.Run()

	deadline := time.Now().Add(20 * time.Second)
	for {
		vx := a.eng.PeerViews(tagX)
		vy := a.eng.PeerViews(tagY)
		okX := len(vx) == 1 && vx[0].Status == "direct" && vx[0].VirtualIP == b.ip
		okY := len(vy) == 1 && vy[0].Status == "direct" && vy[0].VirtualIP == c.ip
		if okX && okY {
			// Изоляция: в X только B, в Y только C.
			if vx[0].VirtualIP == c.ip || vy[0].VirtualIP == b.ip {
				t.Fatalf("сети не изолированы: X=%+v Y=%+v", vx, vy)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("не пробились в обеих сетях: X=%+v Y=%+v", vx, vy)
		}
		time.Sleep(200 * time.Millisecond)
	}
}
