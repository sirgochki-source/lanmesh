package peer

import (
	"encoding/binary"
	"net"
	"testing"
	"time"

	"github.com/sirgochki-source/lanmesh/internal/crypto"
)

// fakeSTUN — минимальный STUN-сервер для теста: на Binding Request отвечает
// Binding Success с XOR-MAPPED-ADDRESS = адрес источника. Проверяем, что движок
// шлёт запрос с боевого сокета и разбирает ответ в readLoop.
func startFakeSTUN(t *testing.T) *net.UDPConn {
	t.Helper()
	conn, err := net.ListenUDP("udp4", &net.UDPAddr{IP: net.IPv4(127, 0, 0, 1), Port: 0})
	if err != nil {
		t.Fatalf("сокет STUN: %v", err)
	}
	go func() {
		buf := make([]byte, 1024)
		for {
			n, src, err := conn.ReadFromUDP(buf)
			if err != nil {
				return
			}
			if n < 20 || binary.BigEndian.Uint16(buf[0:]) != 0x0001 {
				continue // не Binding Request
			}
			var tx [12]byte
			copy(tx[:], buf[8:20])
			conn.WriteToUDP(stunSuccess(tx, src), src)
		}
	}()
	return conn
}

// stunSuccess собирает Binding Success с XOR-MAPPED-ADDRESS для src (IPv4).
func stunSuccess(tx [12]byte, src *net.UDPAddr) []byte {
	msg := make([]byte, 20)
	binary.BigEndian.PutUint16(msg[0:], 0x0101) // Binding Success
	binary.BigEndian.PutUint16(msg[2:], 12)     // длина атрибутов
	binary.BigEndian.PutUint32(msg[4:], 0x2112A442)
	copy(msg[8:20], tx[:])

	attr := make([]byte, 12)
	binary.BigEndian.PutUint16(attr[0:], 0x0020) // XOR-MAPPED-ADDRESS
	binary.BigEndian.PutUint16(attr[2:], 8)
	attr[5] = 0x01 // family IPv4
	binary.BigEndian.PutUint16(attr[6:], uint16(src.Port)^0x2112)
	ip := src.IP.To4()
	cookie := []byte{0x21, 0x12, 0xA4, 0x42}
	for i := 0; i < 4; i++ {
		attr[8+i] = ip[i] ^ cookie[i]
	}
	return append(msg, attr...)
}

// Движок обязан переспросить внешний адрес по STUN со своего боевого сокета и
// разобрать ответ в readLoop (разморозка адреса против замерзания порта).
func TestLiveSTUNUpdatesReflex(t *testing.T) {
	sealer, err := crypto.NewSealer(crypto.DeriveNetworkKey("тест", "пароль"))
	if err != nil {
		t.Fatalf("sealer: %v", err)
	}

	stun := startFakeSTUN(t)
	defer stun.Close()

	a := newNode(t, sealer)
	defer a.conn.Close()
	a.eng.SetSTUNServers([]string{stun.LocalAddr().String()})

	go a.eng.Run()

	// STUN видит нас с адреса нашего сокета (127.0.0.1:port) — его и должен вернуть.
	want := a.conn.LocalAddr().String()
	deadline := time.Now().Add(10 * time.Second)
	for time.Now().Before(deadline) {
		if got, ok := a.eng.StunReflex(); ok {
			if got != want {
				t.Fatalf("stunReflex=%q, ждали %q", got, want)
			}
			return
		}
		time.Sleep(100 * time.Millisecond)
	}
	t.Fatal("движок не получил внешний адрес по живому STUN")
}
