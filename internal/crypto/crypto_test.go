package crypto

import (
	"bytes"
	"testing"
)

func TestSealOpenRoundTrip(t *testing.T) {
	key := DeriveNetworkKey("myteam", "hunter2")
	s, err := NewSealer(key)
	if err != nil {
		t.Fatal(err)
	}
	msg := []byte("привет, это игровой пакет")
	frame, err := s.Seal(msg)
	if err != nil {
		t.Fatal(err)
	}
	got, err := s.Open(frame)
	if err != nil {
		t.Fatal(err)
	}
	if !bytes.Equal(got, msg) {
		t.Fatalf("round-trip mismatch: %q != %q", got, msg)
	}
}

func TestWrongPasswordFails(t *testing.T) {
	a, _ := NewSealer(DeriveNetworkKey("myteam", "hunter2"))
	b, _ := NewSealer(DeriveNetworkKey("myteam", "wrong"))
	frame, _ := a.Seal([]byte("secret"))
	if _, err := b.Open(frame); err == nil {
		t.Fatal("пакет расшифровался чужим паролем — так быть не должно")
	}
}

func TestNetworkNameIsSalt(t *testing.T) {
	// Один пароль, разные имена сети -> разные ключи.
	k1 := DeriveNetworkKey("net-a", "pw")
	k2 := DeriveNetworkKey("net-b", "pw")
	if k1 == k2 {
		t.Fatal("имя сети не влияет на ключ")
	}
}

func TestOpenRejectsShortFrame(t *testing.T) {
	s, _ := NewSealer(DeriveNetworkKey("myteam", "hunter2"))
	// Кадр короче нонса не должен паниковать — только ошибка.
	for _, n := range []int{0, 1, 5, 23} {
		if _, err := s.Open(make([]byte, n)); err == nil {
			t.Fatalf("Open принял кадр длины %d, ожидалась ошибка", n)
		}
	}
}

func TestOpenRejectsTamperedFrame(t *testing.T) {
	s, _ := NewSealer(DeriveNetworkKey("myteam", "hunter2"))
	frame, _ := s.Seal([]byte("важные данные"))
	// Порча одного бита в любой позиции должна валить аутентификацию AEAD.
	for _, i := range []int{0, len(frame) / 2, len(frame) - 1} {
		bad := append([]byte(nil), frame...)
		bad[i] ^= 0x01
		if _, err := s.Open(bad); err == nil {
			t.Fatalf("Open принял кадр с испорченным байтом %d — тег не проверен", i)
		}
	}
}

func TestSealNonceIsUnique(t *testing.T) {
	// Два Seal одного и того же текста должны давать разные нонсы (первые 24 байта).
	// Страховка от катастрофической регрессии — повторного использования нонса.
	s, _ := NewSealer(DeriveNetworkKey("myteam", "hunter2"))
	f1, _ := s.Seal([]byte("одно и то же"))
	f2, _ := s.Seal([]byte("одно и то же"))
	const nonceSize = 24 // XChaCha20-Poly1305
	if bytes.Equal(f1[:nonceSize], f2[:nonceSize]) {
		t.Fatal("нонс повторился между двумя Seal — конфиденциальность под угрозой")
	}
}

// Способ обнаружения вшит в ключ: одна и та же пара имя+пароль в разных режимах —
// это РАЗНЫЕ сети. Так участник, ошибившийся режимом, получает честное «не в этой
// сети», а не подвешенное состояние «вроде та же сеть, но никого не видно».
func TestDiscoveryModeChangesKey(t *testing.T) {
	sig := DeriveNetworkKeyMode("myteam", "hunter2", "signal")
	dht := DeriveNetworkKeyMode("myteam", "hunter2", "dht")
	if sig == dht {
		t.Fatal("режим обнаружения не влияет на ключ сети")
	}
	// Режим сигналок обязан совпадать с прежней дериваций — иначе все
	// существующие сети и старые клиенты отвалятся при обновлении.
	if sig != DeriveNetworkKey("myteam", "hunter2") {
		t.Fatal("ключ обычной сети изменился — сломается совместимость")
	}
	// Пустой режим = обычный (в конфиге старых версий поля нет).
	if DeriveNetworkKeyMode("myteam", "hunter2", "") != sig {
		t.Fatal("пустой режим должен означать обычную сеть")
	}
}

// Соли режимов не должны пересекаться ни при каком имени сети: иначе имя вида
// "dht|foo" могло бы подделать ключ чужой DHT-сети.
func TestDiscoveryModeSaltsDoNotCollide(t *testing.T) {
	for _, name := range []string{"dht|foo", "|dht|foo", "-dht|foo", "foo"} {
		if DeriveNetworkKeyMode(name, "pw", "signal") == DeriveNetworkKeyMode("foo", "pw", "dht") {
			t.Fatalf("имя %q подделало ключ DHT-сети", name)
		}
	}
}

// Разрешение на ретранслятор — тоже часть режима, а значит и ключа: иначе
// «у меня релей разрешён, у тебя нет» дало бы пару, которая молча не соединяется.
func TestRelayPermissionChangesKey(t *testing.T) {
	pure := DeriveNetworkKeyMode("myteam", "hunter2", "dht")
	withRelay := DeriveNetworkKeyMode("myteam", "hunter2", "dht+relay")
	sig := DeriveNetworkKeyMode("myteam", "hunter2", "signal")
	if pure == withRelay || pure == sig || withRelay == sig {
		t.Fatal("режимы дали пересекающиеся ключи")
	}
}
