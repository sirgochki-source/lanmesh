package signal

import (
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net"
	"time"
)

// stunTimeout — сколько ждём ответа STUN. Общий для одиночного и группового опроса.
const stunTimeout = 3 * time.Second

// isTimeout — истёк ли дедлайн чтения (пора выходить), в отличие от прочих ошибок
// сокета. На Windows недоставленный по недоступному STUN-серверу пакет прилетает
// назад как ICMP Port Unreachable, и следующий recvfrom на том же сокете отдаёт
// WSAECONNRESET — это НЕ таймаут: другие серверы ещё могут ответить, читаем дальше.
func isTimeout(err error) bool {
	var ne net.Error
	return errors.As(err, &ne) && ne.Timeout()
}

// DefaultSTUNServers — серверы для определения внешнего адреса.
//
// Список намеренно РАЗНОРОДНЫЙ: у одного оператора (или в одном домене, как
// stun*.l.google.com) блокировка кладёт сразу все записи, и узел остаётся без
// внешнего адреса — а без него пробитие NAT невозможно в принципе, и участник
// становится невидимым для остальных. Поэтому здесь разные сети и разные порты.
var DefaultSTUNServers = []string{
	"stun.l.google.com:19302",
	"stun.cloudflare.com:3478",
	"stun.nextcloud.com:3478",
	"stun.miwifi.com:3478",
	"stun.sipnet.ru:3478",
	"stun.flashdance.cx:3478",
}

// DiscoverEndpointAny опрашивает несколько STUN-серверов ОДНОВРЕМЕННО с того же
// сокета и возвращает первый пришедший ответ вместе с именем ответившего сервера.
//
// Почему разом, а не по очереди: при переборе каждый молчащий сервер стоит
// stunTimeout, и на заблокированном провайдере старт растягивался бы на десятки
// секунд. Здесь цена молчания — один stunTimeout на всех.
//
// Ответы различаем по transaction id: у каждого запроса свой, чужие пакеты (в
// т.ч. от пиров — сокет общий) просто не совпадут и будут пропущены.
func DiscoverEndpointAny(conn *net.UDPConn, servers []string) (endpoint, via string, err error) {
	type pending struct {
		server string
		txID   [12]byte
	}

	var sent []pending
	var lastErr error
	for _, s := range servers {
		raddr, err := net.ResolveUDPAddr("udp4", s)
		if err != nil {
			lastErr = err
			continue
		}
		req, tx, err := buildBindingRequest()
		if err != nil {
			return "", "", err
		}
		if _, err := conn.WriteToUDP(req, raddr); err != nil {
			lastErr = err
			continue
		}
		sent = append(sent, pending{server: s, txID: tx})
	}
	if len(sent) == 0 {
		return "", "", fmt.Errorf("stun: не удалось опросить ни один сервер: %v", lastErr)
	}

	deadline := time.Now().Add(stunTimeout)
	_ = conn.SetReadDeadline(deadline)
	defer conn.SetReadDeadline(time.Time{})

	buf := make([]byte, 1024)
	for time.Now().Before(deadline) {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			if isTimeout(err) {
				break // дедлайн — дальше ждать нечего
			}
			continue // ICMP-отлуп по одному серверу — остальные ещё могут ответить
		}
		for _, p := range sent {
			if ip, port, ok := parseXorMappedAddress(buf[:n], p.txID[:]); ok {
				return net.JoinHostPort(ip.String(), itoa(port)), p.server, nil
			}
		}
	}
	return "", "", fmt.Errorf("stun: ни один из %d серверов не ответил за %s", len(sent), stunTimeout)
}

// BuildSTUNRequest — Binding Request + его transaction id. Нужен тем, кто шлёт
// STUN сам со своего сокета (напр. движок с боевого UDP-сокета), а ответ ловит в
// общем цикле чтения и опознаёт по txID. Так внешний порт в ответе совпадёт с
// портом реального туннеля — что и требуется для актуального внешнего адреса.
func BuildSTUNRequest() ([]byte, [12]byte, error) { return buildBindingRequest() }

// ParseSTUNResponse достаёт наш внешний адрес "ip:port" из STUN-ответа, если это
// ответ на запрос с данным txID. ok=false — не STUN Success / чужой txID / не разобрать.
func ParseSTUNResponse(msg []byte, txID [12]byte) (string, bool) {
	ip, port, ok := parseXorMappedAddress(msg, txID[:])
	if !ok {
		return "", false
	}
	return net.JoinHostPort(ip.String(), itoa(port)), true
}

// NATResult — итог грубой пробы типа NAT (мульти-STUN с одного свежего сокета).
type NATResult struct {
	External string // внешний IP:port (первый распознанный ответ); "" если никто не ответил
	Type     string // "cone" | "symmetric" | "blocked" | "unknown"
	Answered int    // сколько серверов ответило
	Total    int    // сколько опрашивали
}

// ProbeNAT определяет тип NAT: с ОДНОГО свежего сокета шлёт Binding Request всем
// серверам разом и сравнивает внешний ПОРТ в ответах. Одинаковый у всех = cone
// (mapping не зависит от назначения, пробитие возможно); разный = symmetric
// (только relay); никто не ответил = blocked (исходящий UDP режется).
//
// Сокет свой, а не движковый (тот занят чтением трафика пиров). Для вердикта
// cone/symmetric это неважно — тип NAT определяется поведением трансляции, а не
// конкретным портом. Ограничено одним stunTimeout: шлём параллельно, ждём общий.
func ProbeNAT(servers []string) NATResult {
	res := NATResult{Type: "unknown", Total: len(servers)}
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: 0})
	if err != nil {
		return res
	}
	defer conn.Close()

	type pending struct {
		server string
		txID   [12]byte
	}
	var sent []pending
	for _, s := range servers {
		raddr, err := net.ResolveUDPAddr("udp4", s)
		if err != nil {
			continue
		}
		req, tx, err := buildBindingRequest()
		if err != nil {
			continue
		}
		if _, err := conn.WriteToUDP(req, raddr); err != nil {
			continue
		}
		sent = append(sent, pending{server: s, txID: tx})
	}

	deadline := time.Now().Add(stunTimeout)
	_ = conn.SetReadDeadline(deadline)
	defer conn.SetReadDeadline(time.Time{})

	ports := make(map[string]string) // server -> внешний порт
	buf := make([]byte, 1024)
	for time.Now().Before(deadline) && len(ports) < len(sent) {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			if isTimeout(err) {
				break
			}
			continue
		}
		for _, p := range sent {
			if ip, port, ok := parseXorMappedAddress(buf[:n], p.txID[:]); ok {
				if res.External == "" {
					res.External = net.JoinHostPort(ip.String(), itoa(port))
				}
				ports[p.server] = itoa(port)
				break
			}
		}
	}

	res.Answered = len(ports)
	switch {
	case res.Answered == 0:
		res.Type = "blocked"
	case res.Answered == 1:
		res.Type = "unknown" // одного ответа мало, чтобы судить о трансляции
	default:
		same := true
		first := ""
		for _, pt := range ports {
			if first == "" {
				first = pt
				continue
			}
			if pt != first {
				same = false
			}
		}
		if same {
			res.Type = "cone"
		} else {
			res.Type = "symmetric"
		}
	}
	return res
}

// buildBindingRequest собирает Binding Request и возвращает его вместе с
// transaction id, по которому потом опознаётся ответ.
func buildBindingRequest() ([]byte, [12]byte, error) {
	var tx [12]byte
	req := make([]byte, 20)
	binary.BigEndian.PutUint16(req[0:], 0x0001)
	binary.BigEndian.PutUint16(req[2:], 0)
	binary.BigEndian.PutUint32(req[4:], 0x2112A442)
	if _, err := rand.Read(tx[:]); err != nil {
		return nil, tx, err
	}
	copy(req[8:20], tx[:])
	return req, tx, nil
}

// DiscoverEndpoint узнаёт наш внешний IP:port через STUN (RFC 5389), отправляя
// Binding Request С ТОГО ЖЕ UDP-сокета, что потом слушает трафик пиров. Это
// критично: только так внешний порт в ответе совпадёт с портом реального
// туннеля — иначе hole punching бьёт не туда.
//
// server — например "stun.l.google.com:19302".
func DiscoverEndpoint(conn *net.UDPConn, server string) (string, error) {
	raddr, err := net.ResolveUDPAddr("udp4", server)
	if err != nil {
		return "", err
	}

	// Заголовок STUN: type(2)=BindingRequest 0x0001, length(2)=0,
	// magic cookie(4)=0x2112A442, transaction id(12) случайный.
	req, tx, err := buildBindingRequest()
	if err != nil {
		return "", err
	}

	if _, err := conn.WriteToUDP(req, raddr); err != nil {
		return "", err
	}

	// Ждём ответ недолго: сокет общий, чужие пакеты пропускаем и читаем дальше.
	deadline := time.Now().Add(stunTimeout)
	_ = conn.SetReadDeadline(deadline)
	defer conn.SetReadDeadline(time.Time{})

	buf := make([]byte, 1024)
	for time.Now().Before(deadline) {
		n, _, err := conn.ReadFromUDP(buf)
		if err != nil {
			if isTimeout(err) {
				return "", err
			}
			continue // ICMP-отлуп/чужой пакет — читаем дальше до дедлайна
		}
		if ip, port, ok := parseXorMappedAddress(buf[:n], tx[:]); ok {
			return net.JoinHostPort(ip.String(), itoa(port)), nil
		}
		// не STUN-ответ (мог прилететь пакет пира) — читаем следующий
	}
	return "", errors.New("stun: таймаут ответа")
}

// parseXorMappedAddress достаёт наш внешний адрес из STUN-ответа. Понимает и
// XOR-MAPPED-ADDRESS (0x0020, RFC 5389), и легаси MAPPED-ADDRESS (0x0001, RFC 3489
// без XOR) — часть старых публичных серверов из пула отвечает только вторым.
// Проверяет, что это Binding Success на НАШ запрос: совпадает transaction id и на
// месте magic cookie стоит константа 0x2112A442 (мы её шлём, сервер эхом возвращает).
func parseXorMappedAddress(msg, txID []byte) (net.IP, int, bool) {
	if len(msg) < 20 || binary.BigEndian.Uint16(msg[0:]) != 0x0101 { // Binding Success
		return nil, 0, false
	}
	if binary.BigEndian.Uint32(msg[4:8]) != 0x2112A442 { // magic cookie — не наш/битый ответ
		return nil, 0, false
	}
	if string(msg[8:20]) != string(txID) {
		return nil, 0, false
	}
	cookie := msg[4:8]
	attrs := msg[20:]
	for len(attrs) >= 4 {
		typ := binary.BigEndian.Uint16(attrs[0:])
		alen := int(binary.BigEndian.Uint16(attrs[2:]))
		if 4+alen > len(attrs) {
			break
		}
		val := attrs[4 : 4+alen]
		switch {
		case typ == 0x0020 && len(val) >= 8 && val[1] == 0x01: // XOR-MAPPED, IPv4
			port := int(binary.BigEndian.Uint16(val[2:]) ^ 0x2112)
			ip := make(net.IP, 4)
			for i := 0; i < 4; i++ {
				ip[i] = val[4+i] ^ cookie[i]
			}
			return ip, port, true
		case typ == 0x0001 && len(val) >= 8 && val[1] == 0x01: // легаси MAPPED, IPv4 (без XOR)
			port := int(binary.BigEndian.Uint16(val[2:]))
			ip := make(net.IP, 4)
			copy(ip, val[4:8])
			return ip, port, true
		}
		// атрибуты выровнены по 4 байта; после округления вверх могло не остаться
		// паддинг-байтов (последний атрибут длиной не кратной 4) — не режем за границу.
		adv := 4 + alen
		if pad := alen % 4; pad != 0 {
			adv += 4 - pad
		}
		if adv > len(attrs) {
			break
		}
		attrs = attrs[adv:]
	}
	return nil, 0, false
}

func itoa(v int) string {
	if v == 0 {
		return "0"
	}
	var b [8]byte
	i := len(b)
	for v > 0 {
		i--
		b[i] = byte('0' + v%10)
		v /= 10
	}
	return string(b[i:])
}
