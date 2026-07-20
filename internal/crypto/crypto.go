// Package crypto реализует шифрование пакетов lanmesh.
//
// Модель доступа — как у Radmin: «имя сети + пароль». Из этой пары детерминированно
// выводится 256-битный сетевой ключ (одинаковый у всех участников одной сети),
// которым шифруется весь трафик. Знать пароль = быть в сети; сервер-сигналка ключ
// никогда не видит (он лишь сводит пиров, KDF считается локально на клиенте).
package crypto

import (
	"crypto/rand"
	"errors"
	"fmt"

	"golang.org/x/crypto/argon2"
	"golang.org/x/crypto/chacha20poly1305"
)

// KeySize — длина сетевого ключа (ChaCha20-Poly1305).
const KeySize = chacha20poly1305.KeySize

// DeriveNetworkKey выводит сетевой ключ из имени сети и пароля.
//
// Соль = имя сети: одинаковое имя+пароль на любой машине дают один ключ, а разные
// сети под одним паролем — разные ключи. Argon2id с умеренными параметрами: KDF
// считается один раз при старте, так что можно не экономить.
func DeriveNetworkKey(networkName, password string) [KeySize]byte {
	return deriveKey("lanmesh|v1|"+networkName, password)
}

// DeriveNetworkKeyMode — ключ сети с учётом способа обнаружения участников.
//
// Способ обнаружения (сигналки или публичная DHT) вшит В КЛЮЧ, а не оставлен
// настройкой клиента. Причина: точка встречи у режимов разная, и участник, по
// ошибке выбравший не тот режим, при общем ключе оказался бы в подвешенном
// состоянии — вроде «та же сеть», а никого не видит и понять почему нельзя.
// Разные ключи превращают это в честное «ты не в этой сети»: симптом тот же, что
// у неверного пароля, и лечится так же — взять правильное приглашение.
//
// Режим сигналок считается ровно как раньше (та же соль) — существующие сети и
// старые клиенты не затронуты. Соли не пересекаются: после "lanmesh|v1" у одного
// варианта идёт '|', у другого '-', так что имя сети, каким бы оно ни было, не
// может подделать чужую соль.
func DeriveNetworkKeyMode(networkName, password, mode string) [KeySize]byte {
	switch mode {
	case "dht":
		return deriveKey("lanmesh|v1-dht|"+networkName, password)
	case "dht+relay":
		// Разрешение на ретранслятор тоже часть режима: если у одного он разрешён,
		// а у другого нет, пара за симметричным NAT молча не соединится, и понять
		// причину будет нельзя. Разные ключи делают это разными сетями.
		return deriveKey("lanmesh|v1-dhtr|"+networkName, password)
	}
	return DeriveNetworkKey(networkName, password)
}

func deriveKey(salt, password string) [KeySize]byte {
	raw := argon2.IDKey([]byte(password), []byte(salt), 3, 64*1024, 4, KeySize)
	var key [KeySize]byte
	copy(key[:], raw)
	return key
}

// Sealer шифрует и расшифровывает пакеты одним сетевым ключом.
type Sealer struct {
	aead interface {
		Seal(dst, nonce, plaintext, additionalData []byte) []byte
		Open(dst, nonce, ciphertext, additionalData []byte) ([]byte, error)
		NonceSize() int
		Overhead() int
	}
}

// NewSealer строит Sealer на сетевом ключе.
//
// XChaCha20-Poly1305 (расширенный 24-байтный нонс), а не обычный ChaCha20-Poly1305:
// нонс случайный на каждый пакет, а при 12 байтах вероятность его повтора на одном
// статичном сетевом ключе за годы игрового трафика (миллиарды пакетов) выходит за
// безопасный предел birthday-bound, а коллизия нонса ломает и конфиденциальность, и
// целостность. 24 байта отодвигают этот предел за пределы достижимого. Защиту от
// повторов (replay) даёт отдельный счётчик в заголовке кадра, см. пакет peer.
func NewSealer(key [KeySize]byte) (*Sealer, error) {
	aead, err := chacha20poly1305.NewX(key[:])
	if err != nil {
		return nil, fmt.Errorf("xchacha20poly1305: %w", err)
	}
	return &Sealer{aead: aead}, nil
}

// Seal шифрует plaintext. Формат на проводе: nonce(24) || ciphertext || tag(16).
// Нонс случайный на каждый пакет (см. NewSealer про XChaCha и размер нонса).
func (s *Sealer) Seal(plaintext []byte) ([]byte, error) {
	nonce := make([]byte, s.aead.NonceSize())
	if _, err := rand.Read(nonce); err != nil {
		return nil, err
	}
	// Seal дописывает ciphertext+tag в хвост nonce -> получаем самодостаточный кадр.
	return s.aead.Seal(nonce, nonce, plaintext, nil), nil
}

var errShort = errors.New("crypto: пакет короче нонса")

// Open расшифровывает кадр, собранный Seal. Ошибку (обрезка/чужой ключ/подмена)
// возвращает без паники — вызывающий просто дропает пакет.
func (s *Sealer) Open(frame []byte) ([]byte, error) {
	ns := s.aead.NonceSize()
	if len(frame) < ns {
		return nil, errShort
	}
	nonce, ct := frame[:ns], frame[ns:]
	return s.aead.Open(nil, nonce, ct, nil)
}
