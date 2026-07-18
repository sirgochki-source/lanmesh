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
