package dhtdisc

import (
	"net"
	"testing"
	"time"

	"github.com/anacrolix/dht/v2/krpc"

	"github.com/sirgochki-source/lanmesh/internal/crypto"
	"github.com/sirgochki-source/lanmesh/internal/signal"
)

var day = time.Date(2026, 7, 20, 12, 0, 0, 0, time.UTC)

// Инфохэш обязан совпадать у всех, кто знает имя и пароль, — на этом и держится
// обнаружение: обе стороны приходят к одному ключу, ни с кем не сговариваясь.
func TestInfohashDeterministic(t *testing.T) {
	k1 := crypto.DeriveNetworkKey("myteam", "hunter2")
	k2 := crypto.DeriveNetworkKey("myteam", "hunter2")
	if Infohash(k1, day) != Infohash(k2, day) {
		t.Fatal("одинаковые имя+пароль дали разный инфохэш")
	}
	if Infohash(crypto.DeriveNetworkKey("myteam", "hunter3"), day) == Infohash(k1, day) {
		t.Fatal("разный пароль дал тот же инфохэш")
	}
	if Infohash(crypto.DeriveNetworkKey("other", "hunter2"), day) == Infohash(k1, day) {
		t.Fatal("разное имя сети дало тот же инфохэш")
	}
}

// Ротация: сутки — новый ключ. Без неё один постоянный инфохэш позволял бы годами
// наблюдать за составом сети со стороны.
func TestInfohashRotatesDaily(t *testing.T) {
	k := crypto.DeriveNetworkKey("myteam", "hunter2")
	if Infohash(k, day) == Infohash(k, day.AddDate(0, 0, 1)) {
		t.Fatal("инфохэш не сменился на следующие сутки")
	}
	// Внутри суток — стабилен (иначе стороны разъезжались бы каждый час).
	if Infohash(k, day) != Infohash(k, day.Add(11*time.Hour)) {
		t.Fatal("инфохэш меняется внутри одних суток")
	}
	// Часовой пояс не должен влиять: считаем по UTC.
	if Infohash(k, day) != Infohash(k, day.In(time.FixedZone("MSK", 3*3600))) {
		t.Fatal("инфохэш зависит от часового пояса")
	}
}

// Инфохэш не должен совпадать с тегом сети: тег уходит на сигналки, инфохэш — в
// публичную DHT, и связывать одно с другим наблюдателю незачем.
func TestInfohashDiffersFromNetworkTag(t *testing.T) {
	k := crypto.DeriveNetworkKey("myteam", "hunter2")
	tag := signal.NetworkTag(k) // hex sha256
	ih := Infohash(k, day)
	if tag[:40] == hexOf(ih) {
		t.Fatal("инфохэш совпал с тегом сети")
	}
}

func hexOf(ih [20]byte) string {
	const hexdigits = "0123456789abcdef"
	out := make([]byte, 0, 40)
	for _, b := range ih {
		out = append(out, hexdigits[b>>4], hexdigits[b&0xf])
	}
	return string(out)
}

// В выдачу DHT кто угодно может подсунуть мусор — фильтр должен резать
// непригодное: приватные и служебные адреса, нулевые порты.
func TestUsableAddrFilter(t *testing.T) {
	cases := []struct {
		ip   string
		port int
		ok   bool
	}{
		{"203.0.113.9", 25555, true},
		{"192.168.1.5", 25555, false}, // чужая локалка
		{"10.0.0.7", 25555, false},
		{"127.0.0.1", 25555, false},
		{"169.254.3.3", 25555, false},
		{"224.0.0.1", 25555, false},
		{"0.0.0.0", 25555, false},
		{"203.0.113.9", 0, false},
	}
	for _, c := range cases {
		_, ok := usableAddr(krpc.NodeAddr{IP: net.ParseIP(c.ip), Port: c.port})
		if ok != c.ok {
			t.Errorf("%s:%d — got %v, want %v", c.ip, c.port, ok, c.ok)
		}
	}
}
