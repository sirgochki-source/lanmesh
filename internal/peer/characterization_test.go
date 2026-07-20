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

// Снимок прямого пути: пробитие -> подтверждение -> ping/pong -> RTT ->
// перенос данных из TUN в TUN -> счётчики трафика. Всё через публичный API.
func TestCharacterizationDirectPath(t *testing.T) {
	if testing.Short() {
		t.Skip("ждёт пробития NAT до 25с — только полный прогон")
	}

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
