package app

import (
	"testing"

	"github.com/sirgochki-source/lanmesh/internal/proto"
)

func TestPeerSetKeyStable(t *testing.T) {
	// Порядок вставки не влияет — ключ сортирован по PeerID.
	m := map[string]proto.PeerInfo{"b": {}, "a": {}, "c": {}}
	if got := peerSetKey(m); got != "a|b|c" {
		t.Fatalf("peerSetKey = %q, ожидалось a|b|c", got)
	}
	if got := peerSetKey(map[string]proto.PeerInfo{}); got != "" {
		t.Fatalf("пустой набор дал %q, ожидалась пустая строка", got)
	}
	// Смена состава меняет ключ (разгон регистрации на это и опирается).
	if peerSetKey(m) == peerSetKey(map[string]proto.PeerInfo{"a": {}, "b": {}}) {
		t.Fatal("разный состав участников дал одинаковый ключ")
	}
}

func TestUnionStrings(t *testing.T) {
	got := unionStrings([]string{"a", "b"}, []string{"b", "c", ""})
	want := []string{"a", "b", "c"} // без дублей, без пустых, порядок сохранён
	if len(got) != len(want) {
		t.Fatalf("union = %v, ожидалось %v", got, want)
	}
	for i := range want {
		if got[i] != want[i] {
			t.Fatalf("union = %v, ожидалось %v", got, want)
		}
	}
}

// pickExternal: живой STUN должен разморозить внешний адрес после смены порта —
// стартовый (замороженный) stunExt не имеет права держать ext вечно.
func TestPickExternalUnfreezesOnPortChange(t *testing.T) {
	const a1 = "1.2.3.4:1000" // стартовый STUN
	const a2 = "1.2.3.4:2000" // живой STUN после смены маппинга

	// Старт: только stunExt, cur==stunExt (как в bringUpNode).
	got := pickExternal(a1, a1, "", "", "")
	if got != a1 {
		t.Fatalf("на старте ждали %s, получили %s", a1, got)
	}
	// Порт сменился: живой STUN=a2, cur всё ещё a1. Должны перейти на a2, а не
	// залипнуть на стартовом.
	got = pickExternal(a1, a1, "", "", a2)
	if got != a2 {
		t.Fatalf("после смены порта ждали %s, получили %s (заморозка не снята)", a2, got)
	}
}

// pickExternal: на cone NAT (liveStun == stunExt == cur) адрес держится стабильно —
// стикинес не должен теряться там, где он оправдан.
func TestPickExternalStableOnConeNAT(t *testing.T) {
	const a = "5.6.7.8:1111"
	if got := pickExternal(a, a, "", "", a); got != a {
		t.Fatalf("на cone NAT адрес должен быть стабилен: ждали %s, получили %s", a, got)
	}
}

// pickExternal: держим cur, пока он среди живых источников (гистерезис против флапа
// между одновременно валидными адресами у symmetric NAT).
func TestPickExternalHoldsCurrentWhenStillLive(t *testing.T) {
	const cur = "9.9.9.9:100"
	const other = "9.9.9.9:200"
	// cur всё ещё виден как relay-reflex, а живой STUN показывает другой порт.
	if got := pickExternal(cur, "9.9.9.9:1", "", cur, other); got != cur {
		t.Fatalf("гистерезис не удержал живой cur: ждали %s, получили %s", cur, got)
	}
}
