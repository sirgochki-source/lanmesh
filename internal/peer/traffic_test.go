package peer

import (
	"net"
	"testing"
	"time"

	"github.com/sirgochki-source/lanmesh/internal/crypto"
	"github.com/sirgochki-source/lanmesh/internal/proto"
)

// Счётчики трафика: после пробития один IP-пакет данных A->B должен увеличить
// BytesTx у A (к B) и BytesRx у B (от A) на размер полезной нагрузки.
func TestTrafficCountersCountData(t *testing.T) {
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

	// Ждём прямого соединения — иначе слать нечем.
	deadline := time.Now().Add(20 * time.Second)
	for {
		v := a.eng.PeerViews(testTag)
		if len(v) == 1 && v[0].Status == "direct" {
			break
		}
		if time.Now().After(deadline) {
			t.Fatal("узлы не пробились друг к другу для теста трафика")
		}
		time.Sleep(150 * time.Millisecond)
	}

	// Один IPv4-пакет данных с dst = виртуальный IP B (роутится в B через tunToNet).
	bAddr := net.ParseIP(b.ip).To4()
	if bAddr == nil {
		t.Fatalf("не разобрать vip B %q", b.ip)
	}
	pkt := make([]byte, 40)
	pkt[0] = 0x45 // IPv4, IHL 5
	copy(pkt[16:20], bAddr)
	a.tun.read <- pkt

	// B пишет payload в свой TUN => данные приняты и расшифрованы.
	select {
	case got := <-b.tun.wrote:
		if len(got) != len(pkt) {
			t.Fatalf("B принял %d байт, ждали %d", len(got), len(pkt))
		}
	case <-time.After(5 * time.Second):
		t.Fatal("B не получил данные за 5с")
	}

	// TX у A к B и RX у B от A учли полезную нагрузку.
	deadline = time.Now().Add(3 * time.Second)
	for {
		av := a.eng.PeerViews(testTag)
		bv := b.eng.PeerViews(testTag)
		if len(av) == 1 && len(bv) == 1 &&
			av[0].BytesTx >= uint64(len(pkt)) && bv[0].BytesRx >= uint64(len(pkt)) {
			return // счётчики учли данные
		}
		if time.Now().After(deadline) {
			t.Fatalf("счётчики не обновились: A.BytesTx=%d B.BytesRx=%d (ждали >=%d)",
				av[0].BytesTx, bv[0].BytesRx, len(pkt))
		}
		time.Sleep(100 * time.Millisecond)
	}
}
