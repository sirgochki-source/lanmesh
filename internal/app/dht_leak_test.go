package app

import (
	"strings"
	"testing"
)

// Диагностика сети без серверов не должна раскрывать её ни в человекочитаемом
// снимке, ни (через общий лог-буфер) на сигналках соседних сетей: весь смысл
// режима в том, что серверы о ней не знают. Регрессия на найденную ревью утечку.
func TestDiagnosticSnapshotHidesDHTNetwork(t *testing.T) {
	s := &Session{up: true, peerID: "self", nets: map[[32]byte]*netSession{}}

	var homeTag, dhtTag [32]byte
	homeTag[0], dhtTag[0] = 1, 2
	homeHex := strings.Repeat("11", 32)
	dhtHex := strings.Repeat("de", 32)

	s.nets[homeTag] = &netSession{name: "HomeLAN", tag: homeHex, tagB: homeTag, discovery: DiscoverySignal}
	s.nets[dhtTag] = &netSession{name: "SecretDHT", tag: dhtHex, tagB: dhtTag, discovery: DiscoveryDHT, dhtNodes: 12, dhtRounds: 3}

	snap := strings.Join(s.diagnosticSnapshot(), "\n")

	// Имя DHT-сети — половина её секрета (вместе с паролем даёт ключ). В снимок,
	// который уходит на сигналки, оно попасть не должно.
	if strings.Contains(snap, "SecretDHT") {
		t.Errorf("снимок диагностики раскрывает имя DHT-сети:\n%s", snap)
	}
	// Обычная сеть в снимке присутствовать обязана — иначе тест бессмысленен.
	if !strings.Contains(snap, "HomeLAN") {
		t.Errorf("снимок не содержит обычную сеть — проверка ничего не доказывает:\n%s", snap)
	}
	// Полный тег DHT-сети тоже не светим (обезличенная короткая отметка допустима).
	if strings.Contains(snap, dhtHex) {
		t.Errorf("снимок содержит полный тег DHT-сети:\n%s", snap)
	}
}
