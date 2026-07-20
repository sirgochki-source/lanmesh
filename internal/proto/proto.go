// Package proto — общие типы: идентичность пира, виртуальный IP, сообщения сигналки.
package proto

import (
	"crypto/rand"
	"crypto/sha256"
	"encoding/hex"
	"fmt"
	"net/netip"
)

// Виртуальная подсеть. 25.0.0.0/8 — историческая «серая» сеть таких VPN
// (Hamachi жил в 25.x), в публичном интернете не маршрутизируется, конфликт с
// домашними 192.168/10.x/172.16 исключён.
const (
	VirtualPrefix = "25.0.0.0/8"
	virtualBase   = 25 << 24 // первый октет
)

// PeerID — стабильный идентификатор узла (16 случайных байт, генерится один раз
// и хранится в конфиге). От него детерминированно зависит виртуальный IP.
type PeerID [16]byte

// NewPeerID генерирует новый случайный идентификатор.
func NewPeerID() (PeerID, error) {
	var id PeerID
	_, err := rand.Read(id[:])
	return id, err
}

func (id PeerID) String() string { return hex.EncodeToString(id[:]) }

// ParsePeerID разбирает hex-представление.
func ParsePeerID(s string) (PeerID, error) {
	var id PeerID
	b, err := hex.DecodeString(s)
	if err != nil || len(b) != len(id) {
		return id, fmt.Errorf("bad peer id %q", s)
	}
	copy(id[:], b)
	return id, nil
}

// VirtualIP выводит адрес 25.x.y.z из PeerID детерминированно.
//
// Берём 3 младших октета из хэша id (первый октет всегда 25). .0 и .255 в младшем
// октете сдвигаем, чтобы не попасть на broadcast/сетевой адрес. Коллизия для
// десятка друзей крайне маловероятна; при желании сигналка может её разрулить,
// но для MVP хватает вывода на клиенте.
func VirtualIP(id PeerID) netip.Addr {
	h := sha256.Sum256(id[:])
	oct2, oct3, oct4 := uint32(h[0]), uint32(h[1]), uint32(h[2])
	if oct4 == 0 || oct4 == 255 {
		oct4 = 1
	}
	v := uint32(virtualBase) | oct2<<16 | oct3<<8 | oct4
	return netip.AddrFrom4([4]byte{byte(v >> 24), byte(v >> 16), byte(v >> 8), byte(v)})
}

// --- Сообщения сигналки (JSON поверх HTTP к Cloudflare Worker) ---------------

// RegisterRequest — пир объявляет о себе в сети и обновляет свой endpoint.
//
// NetworkTag — НЕ имя сети открытым текстом: это хэш(имя+пароль). Так сервер
// сводит вместе только тех, кто знает пароль, но самого пароля/имени не узнаёт.
type RegisterRequest struct {
	NetworkTag string   `json:"net"`  // hex(sha256("lanmesh-tag|" + key))
	PeerID     string   `json:"id"`   // hex PeerID
	Name       string   `json:"name"` // человекочитаемое имя узла (hostname)
	VirtualIP  string   `json:"vip"`  // виртуальный IP (клиент считает сам из PeerID)
	Endpoints  []string `json:"eps"`  // "ip:port": STUN-адрес + локальные кандидаты
}

// PeerInfo — запись о другом участнике сети, которую сигналка отдаёт клиенту.
type PeerInfo struct {
	PeerID    string   `json:"id"`
	Name      string   `json:"name"`
	VirtualIP string   `json:"vip"`
	Endpoints []string `json:"eps"`
}

// RegisterResponse — ответ сигналки: список остальных участников сети.
//
// SeenFrom — IP, с которого сигналка УВИДЕЛА наш запрос. Это наш настоящий
// внешний адрес глазами сервера — его нельзя подделать с клиента и он не зависит
// от STUN. Сверка с адресом от STUN ловит full-tunnel/split VPN (STUN отравлен
// адресом выхода VPN). Только IP: register идёт по TCP, порт там не тот, что у
// боевого UDP-сокета. Пусто, если сигналка старая или это воркер (там не делаем).
type RegisterResponse struct {
	Self     PeerInfo   `json:"self"`
	Peers    []PeerInfo `json:"peers"`
	SeenFrom string     `json:"seen,omitempty"`
}

// LogRequest — пачка строк лога, которую узел отправляет на сигналку для
// диагностики. Нужна, чтобы смотреть логи участника, не прося его присылать
// файл руками. Уходит только если пользователь не выключил отправку и только
// когда есть новые строки — пустых запросов не шлём (записи в KV не бесплатны).
type LogRequest struct {
	NetworkTag string   `json:"net"`   // тот же тег, что и в RegisterRequest
	PeerID     string   `json:"id"`    // hex PeerID
	Name       string   `json:"name"`  // hostname — чтобы отличать узлы в выдаче
	Lines      []string `json:"lines"` // строки лога, как есть
}

// --- Типы кадров UDP-транспорта между пирами --------------------------------

// Тип кадра — первый байт ПОСЛЕ расшифровки полезной нагрузки транспортом.
const (
	// FrameData — обычный IP-пакет из TUN.
	FrameData byte = 1
	// FramePunch — служебный пакет пробития NAT/keepalive (тела нет).
	FramePunch byte = 2
	// FrameBroadcast — эмуляция широковещалки: IP-пакет, который надо разослать
	// всем пирам сети (для обнаружения LAN-игр).
	FrameBroadcast byte = 3
	// FramePing — замер задержки: тело = 8 байт номера (big-endian). Получатель
	// обязан вернуть его без изменений в FramePong.
	FramePing byte = 4
	// FramePong — эхо на FramePing с тем же номером. RTT считает отправитель по
	// СВОИМ часам, поэтому часы пиров синхронизировать не нужно.
	FramePong byte = 5
	// FrameAddr — обмен адресами напрямую, минуя сигналку: тело несёт (а) адрес,
	// с которого отправитель видит ПОЛУЧАТЕЛЯ (peer-reflexive: так узел узнаёт свой
	// внешний адрес без STUN), и (б) актуальные кандидаты отправителя (чтобы пир
	// перепробился на новый адрес при смене IP/порта). Формат кодирует peer-пакет.
	FrameAddr byte = 6
	// FrameHello — отправитель называет своё отображаемое имя (тело = UTF-8, до
	// HelloMaxLen байт). Нужен там, где имя неоткуда взять: при обнаружении через
	// DHT пир узнаётся из входящего кадра, а имена раздаёт только сигналка.
	// Старые клиенты неизвестный тип молча игнорируют — совместимость цела.
	FrameHello byte = 7
)

// HelloMaxLen — потолок длины имени в FrameHello. Имя приходит от пира и едет в
// панель, поэтому длину режем на приёме, а не надеемся на отправителя.
const HelloMaxLen = 64
