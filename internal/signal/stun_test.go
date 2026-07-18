package signal

import (
	"encoding/binary"
	"net"
	"testing"
)

// stunTxID — произвольный, но фиксированный transaction id для тестов.
var stunTxID = []byte{1, 2, 3, 4, 5, 6, 7, 8, 9, 10, 11, 12}

// stunHeader собирает заголовок Binding Success с нашим magic cookie и txID.
func stunHeader(attrsLen int) []byte {
	h := make([]byte, 20)
	binary.BigEndian.PutUint16(h[0:], 0x0101) // Binding Success
	binary.BigEndian.PutUint16(h[2:], uint16(attrsLen))
	binary.BigEndian.PutUint32(h[4:], 0x2112A442) // magic cookie
	copy(h[8:20], stunTxID)
	return h
}

func TestParseSTUNRejectsGarbage(t *testing.T) {
	cases := map[string][]byte{
		"пустой":            {},
		"короче заголовка":  make([]byte, 10),
		"не success":        append([]byte{0x00, 0x01}, make([]byte, 30)...),
		"чужой txID":        append(stunHeaderWithTx([]byte("cccccccccccc"), 0), 0),
		"обрезанный атрибут": append(stunHeader(8), 0x00, 0x20, 0x00, 0x08, 0x00), // заявлено 8, дано 1
		// Регресс: последний атрибут длиной не кратной 4 без хвостового паддинга —
		// раньше округление adv за границу буфера паниковало.
		"атрибут без паддинга": func() []byte {
			b := stunHeader(5)
			b = append(b, 0x80, 0x22, 0x00, 0x01, 0xFF) // unknown attr, len=1, 1 байт значения
			return b
		}(),
	}
	for name, msg := range cases {
		// Главное — не паникует и не находит адрес в мусоре.
		if ip, port, ok := parseXorMappedAddress(msg, stunTxID); ok {
			t.Fatalf("%s: разобрался как %s:%d, ожидался отказ", name, ip, port)
		}
	}
}

func stunHeaderWithTx(tx []byte, attrsLen int) []byte {
	h := stunHeader(attrsLen)
	copy(h[8:20], tx)
	return h
}

func TestParseSTUNValidXorMapped(t *testing.T) {
	ip := net.IPv4(203, 0, 113, 5).To4()
	const port = 51234
	val := make([]byte, 8)
	val[0], val[1] = 0x00, 0x01 // reserved, family IPv4
	binary.BigEndian.PutUint16(val[2:], uint16(port)^0x2112)
	cookie := []byte{0x21, 0x12, 0xA4, 0x42}
	for i := 0; i < 4; i++ {
		val[4+i] = ip[i] ^ cookie[i]
	}
	msg := stunHeader(12)
	msg = append(msg, 0x00, 0x20, 0x00, 0x08) // XOR-MAPPED-ADDRESS, len 8
	msg = append(msg, val...)

	gotIP, gotPort, ok := parseXorMappedAddress(msg, stunTxID)
	if !ok {
		t.Fatal("валидный XOR-MAPPED-ADDRESS не разобрался")
	}
	if !gotIP.Equal(ip) || gotPort != port {
		t.Fatalf("разобрано %s:%d, ожидалось %s:%d", gotIP, gotPort, ip, port)
	}
}
