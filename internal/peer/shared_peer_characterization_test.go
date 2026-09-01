// Характеризационный снимок ОДНОГО пира, состоящего сразу в ДВУХ общих сетях, —
// случай, который не покрывал ни один существующий тест: multinet_test проверяет
// узел в двух сетях, но с РАЗНЫМИ пирами в каждой.
//
// Написан ДО разделения peerState на общий путь (peerPath) и посетевое членство.
// После рефакторинга обязан пройти без единой правки: живёт во ВНЕШНЕМ пакете
// peer_test, поэтому компилятор не пускает его к неэкспортированным полям и
// «поехать» вместе с внутренностями он не может.
//
// Что снимает — ровно то, что общий путь способен сломать:
//   - пир виден как direct и с измеренным RTT в ОБЕИХ сетях, а не в одной;
//   - данные ходят по туннелю (виртуальный IP у пира один на все сети);
//   - отключение одной сети НЕ рвёт связь в другой (после рефакторинга путь
//     станет общим и разделяемым — если счёт ссылок ошибётся, отвалится всё).
package peer_test

import (
	"testing"
	"time"

	"github.com/sirgochki-source/lanmesh/internal/crypto"
	"github.com/sirgochki-source/lanmesh/internal/proto"
)

// Теги двух общих сетей. Заполняются одним повторяющимся байтом СОЗНАТЕЛЬНО:
// copy в [32]byte режет по БАЙТАМ, а не по символам, и пара осмысленных
// кириллических строк легко совпадает в первых 32 байтах (два байта на букву) —
// тогда вторая сеть молча затирает первую в карте движка, и тест проверяет не то,
// что написано в его названии. Здесь различие видно глазом и обрезаться нечему.
func fillTag(b byte) [32]byte {
	var t [32]byte
	for i := range t {
		t[i] = b
	}
	return t
}

var (
	tagShared1 = fillTag('1')
	tagShared2 = fillTag('2')
)

func TestCharacterizationSamePeerInTwoNetworks(t *testing.T) {
	if testing.Short() {
		t.Skip("ждёт пробития NAT до 25с — только полный прогон")
	}

	// Три РАЗНЫХ ключа. Два ключа на разные сети совпадать не должны: на приёме
	// кадр отдаётся первой сети, чей sealer его открыл, и при совпадении ключей
	// атрибуция стала бы случайной (порядок обхода карты), а тест — плавающим.
	s0, err := crypto.NewSealer(crypto.DeriveNetworkKey("посторонняя", "пароль-0"))
	if err != nil {
		t.Fatalf("sealer 0: %v", err)
	}
	s1, err := crypto.NewSealer(crypto.DeriveNetworkKey("общая-1", "пароль-1"))
	if err != nil {
		t.Fatalf("sealer 1: %v", err)
	}
	s2, err := crypto.NewSealer(crypto.DeriveNetworkKey("общая-2", "пароль-2"))
	if err != nil {
		t.Fatalf("sealer 2: %v", err)
	}

	// Оба узла — в обеих общих сетях. newTestNode заводит ещё сеть tag с ключом
	// s0: пиров в ней нет, на снимок она не влияет.
	a, b := newTestNode(t, s0), newTestNode(t, s0)
	for _, n := range []*testNode{a, b} {
		n.eng.AddNetwork(tagShared1, s1, "общая-1")
		n.eng.AddNetwork(tagShared2, s2, "общая-2")
	}

	for _, tg := range [][32]byte{tagShared1, tagShared2} {
		a.eng.SyncPeers(tg, []proto.PeerInfo{b.info("B")})
		b.eng.SyncPeers(tg, []proto.PeerInfo{a.info("A")})
	}
	go a.eng.Run()
	go b.eng.Run()

	// 1. Пир обязан стать direct с измеренным RTT в ОБЕИХ сетях.
	waitDirect(t, a, tagShared1, "общая-1", b.ip)
	waitDirect(t, a, tagShared2, "общая-2", b.ip)

	// 2. Данные ходят по туннелю. Виртуальный IP у B один на обе сети — движок
	//    обязан доставить пакет, не запутавшись, через какую из них слать.
	pkt := ipv4Packet(a.ip, b.ip, []byte("две-сети"))
	select {
	case a.tun.read <- pkt:
	case <-time.After(2 * time.Second):
		t.Fatal("движок A не забрал пакет из TUN")
	}
	select {
	case got := <-b.tun.wrote:
		if string(got) != string(pkt) {
			t.Fatalf("пакет исказился:\nбыло %q\nстало %q", pkt, got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("пакет не дошёл до TUN узла B")
	}

	// 3. Отключение одной сети не должно ронять связь в другой. Сейчас это
	//    очевидно (таблицы независимы), после перехода на общий путь — уже нет:
	//    именно здесь ошибка в счёте ссылок оторвёт живое соединение.
	a.eng.RemoveNetwork(tagShared2)
	if v := a.eng.PeerViews(tagShared2); len(v) != 0 {
		t.Fatalf("сеть отключена, а пиры в ней остались: %+v", v)
	}
	waitDirect(t, a, tagShared1, "общая-1 после отключения общая-2", b.ip)

	// И трафик в оставшейся сети по-прежнему ходит.
	pkt2 := ipv4Packet(a.ip, b.ip, []byte("после-отключения"))
	select {
	case a.tun.read <- pkt2:
	case <-time.After(2 * time.Second):
		t.Fatal("движок A не забрал второй пакет из TUN")
	}
	select {
	case got := <-b.tun.wrote:
		if string(got) != string(pkt2) {
			t.Fatalf("второй пакет исказился:\nбыло %q\nстало %q", pkt2, got)
		}
	case <-time.After(5 * time.Second):
		t.Fatal("после отключения второй сети трафик в первой встал")
	}
}

// waitDirect ждёт, пока в сети tg появится ровно один пир с ожидаемым vip,
// статусом direct и измеренным RTT.
func waitDirect(t *testing.T, n *testNode, tg [32]byte, what, wantIP string) {
	t.Helper()
	deadline := time.Now().Add(25 * time.Second)
	for {
		v := n.eng.PeerViews(tg)
		if len(v) == 1 && v[0].Status == "direct" && v[0].RttMs >= 0 {
			if v[0].VirtualIP != wantIP {
				t.Fatalf("%s: не тот пир: %+v (ждали vip %s)", what, v[0], wantIP)
			}
			return
		}
		if time.Now().After(deadline) {
			t.Fatalf("%s: прямой путь не установился за 25с: %+v", what, n.eng.PeerViews(tg))
		}
		time.Sleep(200 * time.Millisecond)
	}
}
