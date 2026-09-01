package peer

import (
	"net"
	"sync/atomic"
	"testing"
	"time"

	"github.com/sirgochki-source/lanmesh/internal/crypto"
	"github.com/sirgochki-source/lanmesh/internal/proto"
)

// silentPeer — сокет, который только считает входящие пакеты и НИКОГДА не
// отвечает. Молчание здесь принципиально: пир остаётся неподтверждённым, и узел
// честно долбит его кандидат пробитием каждый punchInterval — ровно тот трафик,
// который раньше множился на число общих сетей.
type silentPeer struct {
	conn *net.UDPConn
	id   proto.PeerID
	got  atomic.Int64
}

func newSilentPeer(t *testing.T) *silentPeer {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("сокет: %v", err)
	}
	id, err := proto.NewPeerID()
	if err != nil {
		t.Fatalf("id: %v", err)
	}
	p := &silentPeer{conn: conn, id: id}
	go func() {
		buf := make([]byte, 2048)
		for {
			if _, _, err := conn.ReadFromUDP(buf); err != nil {
				return
			}
			p.got.Add(1)
		}
	}()
	t.Cleanup(func() { conn.Close() })
	return p
}

func (p *silentPeer) info() proto.PeerInfo {
	return proto.PeerInfo{
		PeerID:    p.id.String(),
		Name:      "молчун",
		VirtualIP: proto.VirtualIP(p.id).String(),
		Endpoints: []string{p.conn.LocalAddr().String()},
	}
}

// Служебный трафик к пиру НЕ должен зависеть от того, в скольких общих сетях мы
// с ним состоим: путь до него физически один (тот же сокет, тот же endpoint),
// различается только ключ шифрования.
//
// До разделения peerState на общий peerPath и посетевое членство узел с двумя
// общими сетями слал вдвое больше пробития в ту же дырку, с тремя — втрое.
// Тест сравнивает два узла в одинаковых условиях: у одного пир в одной сети, у
// другого тот же пир в двух. Счётчики обязаны совпасть.
func TestServiceTrafficDoesNotScaleWithSharedNetworks(t *testing.T) {
	if testing.Short() {
		t.Skip("считает пакеты в реальном времени — только полный прогон")
	}

	s1, err := crypto.NewSealer(crypto.DeriveNetworkKey("путь-1", "пароль-1"))
	if err != nil {
		t.Fatalf("sealer 1: %v", err)
	}
	s2, err := crypto.NewSealer(crypto.DeriveNetworkKey("путь-2", "пароль-2"))
	if err != nil {
		t.Fatalf("sealer 2: %v", err)
	}
	tagA := fillTag32('A')
	tagB := fillTag32('B')

	// Контроль: одна сеть. Опыт: две сети, тот же самый пир в обеих.
	control, experiment := newBareNode(t), newBareNode(t)
	defer control.conn.Close()
	defer experiment.conn.Close()

	control.eng.AddNetwork(tagA, s1, "одна")
	experiment.eng.AddNetwork(tagA, s1, "первая")
	experiment.eng.AddNetwork(tagB, s2, "вторая")

	pControl, pExperiment := newSilentPeer(t), newSilentPeer(t)
	control.eng.SyncPeers(tagA, []proto.PeerInfo{pControl.info()})
	experiment.eng.SyncPeers(tagA, []proto.PeerInfo{pExperiment.info()})
	experiment.eng.SyncPeers(tagB, []proto.PeerInfo{pExperiment.info()})

	go control.eng.Run()
	go experiment.eng.Run()

	// Несколько тиков punchInterval, чтобы разница была заведомо больше единицы.
	time.Sleep(7 * time.Second)

	one := pControl.got.Load()
	two := pExperiment.got.Load()
	t.Logf("пакетов за 7с: одна сеть = %d, две общие сети = %d", one, two)

	if one == 0 {
		t.Fatal("контрольный узел не слал пробитие вовсе — тест ничего не проверяет")
	}
	// Допуск в один пакет — на несовпадение фаз тикеров у двух движков.
	if two > one+1 {
		t.Fatalf("трафик вырос с числом общих сетей: одна сеть = %d, две = %d "+
			"(ожидали примерно поровну — путь до пира один)", one, two)
	}
}

// fillTag32 — тег из одного повторяющегося байта. Осмысленные кириллические
// строки для этого не годятся: copy в [32]byte режет по БАЙТАМ, и две разные с
// виду строки легко совпадают в первых 32 байтах (два байта на букву).
func fillTag32(b byte) [relayTagLen]byte {
	var t [relayTagLen]byte
	for i := range t {
		t[i] = b
	}
	return t
}
