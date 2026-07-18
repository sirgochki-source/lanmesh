// Command lanmesh-relay — ретранслятор для пиров, которые не смогли пробить NAT.
//
// Зачем: прямое соединение открывается не всегда. За симметричным NAT (а это,
// в частности, ЛЮБОЙ мобильный интернет с CGNAT) внешний порт меняется для
// каждого адресата, и пробитие невозможно в принципе. Тогда пиры гоняют трафик
// через этот сервер — он стоит на белом адресе и доступен обоим.
//
// Ретранслятор ТУПОЙ и ничего не расшифровывает: внутри пересылаемого пакета
// лежит обычный запечатанный кадр lanmesh, ключа сети у сервера нет. Он видит
// только тег сети (несекретный хэш) и адреса — как и сигналка.
//
// Протокол (UDP):
//
//	клиент -> relay   [0x01][тег(32)][peerID(16)]              bind/keepalive
//	клиент -> relay   [0x02][тег(32)][dstPeerID(16)][кадр...]  переслать узлу dst
//	relay  -> клиент  [0x03][кадр...]                          входящий кадр
//	relay  -> клиент  [0x04][тег(32)][peerID(16)][src "ip:port"]  bind ok + STUN
//
// Таблица ключуется парой (тег, peerID): тег выводится из имени+пароля, поэтому
// посторонний не подменит запись в чужой сети, не зная пароля.
package main

import (
	"encoding/hex"
	"flag"
	"log"
	"net"
	"sync"
	"time"
)

// Типы пакетов ретранслятора.
const (
	msgBind    byte = 0x01
	msgData    byte = 0x02
	msgForward byte = 0x03
	msgBindOK  byte = 0x04
)

const (
	tagLen  = 32 // sha256 тега сети, сырые байты
	idLen   = 16 // PeerID
	maxUDP  = 2048
	bindTTL = 90 * time.Second // не слал bind дольше — запись протухла
	sweep   = 30 * time.Second // как часто чистим протухшие
)

// key — с кем связана запись: сеть + узел.
type key struct {
	tag [tagLen]byte
	id  [idLen]byte
}

type entry struct {
	addr *net.UDPAddr
	seen time.Time
}

type table struct {
	mu sync.RWMutex
	m  map[key]entry
}

func newTable() *table { return &table{m: make(map[key]entry)} }

func (t *table) bind(k key, addr *net.UDPAddr) (isNew bool) {
	t.mu.Lock()
	defer t.mu.Unlock()
	old, ok := t.m[k]
	isNew = !ok || old.addr.String() != addr.String()
	t.m[k] = entry{addr: addr, seen: time.Now()}
	return isNew
}

func (t *table) lookup(k key) (*net.UDPAddr, bool) {
	t.mu.RLock()
	defer t.mu.RUnlock()
	e, ok := t.m[k]
	if !ok || time.Since(e.seen) > bindTTL {
		return nil, false
	}
	return e.addr, true
}

// expire выкидывает протухшие записи, чтобы таблица не росла вечно.
func (t *table) expire() int {
	t.mu.Lock()
	defer t.mu.Unlock()
	n := 0
	for k, e := range t.m {
		if time.Since(e.seen) > bindTTL {
			delete(t.m, k)
			n++
		}
	}
	return n
}

func (t *table) size() int {
	t.mu.RLock()
	defer t.mu.RUnlock()
	return len(t.m)
}

func main() {
	addr := flag.String("listen", ":25555", "UDP-адрес для приёма")
	verbose := flag.Bool("v", false, "подробный лог (каждый bind/пересылка)")
	flag.Parse()

	uaddr, err := net.ResolveUDPAddr("udp4", *addr)
	if err != nil {
		log.Fatalf("адрес %q: %v", *addr, err)
	}
	conn, err := net.ListenUDP("udp4", uaddr)
	if err != nil {
		log.Fatalf("слушать %s: %v", *addr, err)
	}
	defer conn.Close()
	log.Printf("lanmesh-relay слушает %s", conn.LocalAddr())

	tbl := newTable()

	go func() {
		for range time.Tick(sweep) {
			if n := tbl.expire(); n > 0 {
				log.Printf("протухло записей: %d, осталось %d", n, tbl.size())
			}
		}
	}()

	var stats struct {
		sync.Mutex
		forwarded, dropped uint64
	}
	go func() {
		for range time.Tick(5 * time.Minute) {
			stats.Lock()
			f, d := stats.forwarded, stats.dropped
			stats.Unlock()
			log.Printf("статистика: узлов %d, переслано %d, отброшено %d", tbl.size(), f, d)
		}
	}()

	buf := make([]byte, maxUDP)
	for {
		n, src, err := conn.ReadFromUDP(buf)
		if err != nil {
			log.Printf("чтение: %v", err)
			continue
		}
		pkt := buf[:n]
		if len(pkt) < 1 {
			continue
		}

		switch pkt[0] {
		case msgBind:
			// [0x01][тег][peerID]
			if len(pkt) < 1+tagLen+idLen {
				continue
			}
			var k key
			copy(k.tag[:], pkt[1:1+tagLen])
			copy(k.id[:], pkt[1+tagLen:1+tagLen+idLen])

			if isNew := tbl.bind(k, src); isNew || *verbose {
				log.Printf("bind %s сеть %s -> %s", hex.EncodeToString(k.id[:])[:8],
					hex.EncodeToString(k.tag[:])[:8], src)
			}
			// Подтверждение bind + STUN: сообщаем клиенту адрес, с которого мы его
			// видим на его БОЕВОМ сокете. Это готовый внешний endpoint (IP+порт),
			// только от нашего сервера, а не от публичного STUN, — его не режет DPI
			// и не отравляет VPN. Хвост дописан к старому формату: старые клиенты
			// читают ack фиксированной длины и лишнее игнорируют.
			srcStr := src.String()
			ack := make([]byte, 1+tagLen+idLen+len(srcStr))
			ack[0] = msgBindOK
			copy(ack[1:], k.tag[:])
			copy(ack[1+tagLen:], k.id[:])
			copy(ack[1+tagLen+idLen:], srcStr)
			conn.WriteToUDP(ack, src)

		case msgData:
			// [0x02][тег][dstPeerID][кадр]
			if len(pkt) < 1+tagLen+idLen {
				continue
			}
			var k key
			copy(k.tag[:], pkt[1:1+tagLen])
			copy(k.id[:], pkt[1+tagLen:1+tagLen+idLen])
			payload := pkt[1+tagLen+idLen:]

			dst, ok := tbl.lookup(k)
			if !ok {
				stats.Lock()
				stats.dropped++
				stats.Unlock()
				if *verbose {
					log.Printf("некуда слать: %s не в таблице", hex.EncodeToString(k.id[:])[:8])
				}
				continue
			}

			out := make([]byte, 1+len(payload))
			out[0] = msgForward
			copy(out[1:], payload)
			if _, err := conn.WriteToUDP(out, dst); err != nil {
				if *verbose {
					log.Printf("отправка на %s: %v", dst, err)
				}
				continue
			}
			stats.Lock()
			stats.forwarded++
			stats.Unlock()

		default:
			// Чужой мусор на публичном порту — норма, молчим.
		}
	}
}
