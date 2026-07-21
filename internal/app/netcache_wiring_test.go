package app

import (
	"encoding/hex"
	"testing"

	"github.com/sirgochki-source/lanmesh/internal/crypto"
	"github.com/sirgochki-source/lanmesh/internal/signal"
)

// Тег кэша endpoint'ов выводится ДВУМЯ независимыми путями: при записи (движок
// отдаёт голый [32]byte в OnDirectConfirmed, bringUpNode кодирует его
// hex.EncodeToString) и при чтении (AddNetworkMode уже держит готовую hex-строку
// от signal.NetworkTag). Если бы кодировки разошлись (например, одна сторона
// использовала верхний регистр), Put клал бы записи под одним ключом, а
// Peers/Get в AddNetworkMode искали бы под другим — кэш продолжал бы молча
// копить данные, но заливка ДО первого раунда сигналки перестала бы находить
// хоть что-то. Ни TestOnDirectConfirmedFires (гоняет движок напрямую, без
// hex.EncodeToString), ни юнит-тесты netcache (не знают про signal.NetworkTag)
// эту связку не проверяют — AddNetworkMode целиком требует TUN/прав
// администратора и не покрыт тестами. Закрепляем инвариант здесь явно.
func TestCacheTagEncodingMatchesNetworkTag(t *testing.T) {
	key := crypto.DeriveNetworkKeyMode("сеть", "пароль", DiscoverySignal)
	tag := signal.NetworkTag(key) // путь чтения — AddNetworkMode

	var tagB [32]byte
	raw, err := hex.DecodeString(tag)
	if err != nil || len(raw) != len(tagB) {
		t.Fatalf("тег %q не раскодировался в 32 байта: %v", tag, err)
	}
	copy(tagB[:], raw)

	if got := hex.EncodeToString(tagB[:]); got != tag { // путь записи — OnDirectConfirmed
		t.Fatalf("кодировка тега кэша разошлась: путь записи=%q, путь чтения=%q", got, tag)
	}
}
