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
