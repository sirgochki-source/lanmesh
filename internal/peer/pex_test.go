package peer

import (
	"net/netip"
	"testing"
	"time"

	"github.com/sirgochki-source/lanmesh/internal/crypto"
	"github.com/sirgochki-source/lanmesh/internal/proto"
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
	frame[0] = 2                      // объявляем две записи
	frame = append(frame, 9, 0, 0, 0) // вторая — с неизвестным семейством

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

// A связан с B, B связан с C, про C узел A не знает. После раунда PEX адрес C
// должен доехать до A через B — и стать настоящим пиром только после того, как
// придёт кадр, расшифрованный ключом сети (а не по слову B).
func TestPexMakesThirdPeerReachable(t *testing.T) {
	if testing.Short() {
		t.Skip("ждёт пробития NAT до 25с — только полный прогон")
	}

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
