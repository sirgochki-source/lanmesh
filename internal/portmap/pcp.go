package portmap

import (
	"context"
	"crypto/rand"
	"encoding/binary"
	"errors"
	"fmt"
	"net/netip"
	"time"
)

// pcpVersion, pcpOpcodeMap, pcpOpcodeMapResp, pcpProtoUDP — константы RFC 6887.
const (
	pcpVersion        = 2
	pcpOpcodeMap      = 1
	pcpOpcodeMapResp  = 0x81 // opcode 1 с установленным R-битом (ответ)
	pcpProtoUDP       = 17   // IANA protocol number
	pcpRequestLen     = 60
	pcpResponseMinLen = 60
)

// pcpMapper — проброс через PCP (RFC 6887). Тот же порт UDP:5351, что у
// NAT-PMP, но версия 2 в заголовке и обязательная сверка nonce в ответе.
type pcpMapper struct {
	gateway netip.Addr
}

func (m *pcpMapper) name() string { return "pcp" }

func (m *pcpMapper) add(ctx context.Context, localPort int) (netip.AddrPort, time.Duration, error) {
	conn, err := dialGateway(m.gateway, natpmpPort)
	if err != nil {
		return netip.AddrPort{}, 0, fmt.Errorf("pcp: подключение к шлюзу: %w", err)
	}
	defer conn.Close()
	defer closeOnDone(ctx, conn)()

	clientIP, ok := localAddrOf(conn)
	if !ok {
		return netip.AddrPort{}, 0, errors.New("pcp: не удалось определить локальный адрес")
	}

	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return netip.AddrPort{}, 0, fmt.Errorf("pcp: nonce: %w", err)
	}

	req := buildPCPMapRequest(clientIP, nonce, localPort, localPort, mappingLease)
	buf := make([]byte, 128)
	n, err := sendRecv(conn, req, buf)
	if err != nil {
		return netip.AddrPort{}, 0, err
	}
	return parsePCPMap(buf[:n], nonce)
}

func (m *pcpMapper) remove(ctx context.Context, localPort int) {
	conn, err := dialGateway(m.gateway, natpmpPort)
	if err != nil {
		return
	}
	defer conn.Close()
	defer closeOnDone(ctx, conn)()

	clientIP, ok := localAddrOf(conn)
	if !ok {
		return
	}
	var nonce [12]byte
	if _, err := rand.Read(nonce[:]); err != nil {
		return
	}
	// Аренда 0 — команда удалить маппинг (RFC 6887 §11.1). Ответ не разбираем:
	// best-effort уборка на выходе, подтверждение нам не нужно.
	req := buildPCPMapRequest(clientIP, nonce, localPort, localPort, 0)
	buf := make([]byte, 128)
	_, _ = sendRecv(conn, req, buf)
}

// buildPCPMapRequest собирает запрос MAP (60 байт, RFC 6887 §11.1 и §9.1):
// версия(1) опкод(1) резерв(2) аренда(4) адрес клиента(16, v4-in-v6) nonce(12)
// протокол(1) резерв(3) внутренний порт(2) желаемый внешний порт(2)
// желаемый внешний адрес(16, v4-in-v6, все нули = без предпочтения).
func buildPCPMapRequest(clientIP netip.Addr, nonce [12]byte, internalPort, extPortHint int, lease time.Duration) []byte {
	req := make([]byte, pcpRequestLen)
	req[0] = pcpVersion
	req[1] = pcpOpcodeMap
	binary.BigEndian.PutUint32(req[4:8], uint32(lease/time.Second))
	clientBytes := v4InV6(clientIP)
	copy(req[8:24], clientBytes[:])
	copy(req[24:36], nonce[:])
	req[36] = pcpProtoUDP
	binary.BigEndian.PutUint16(req[40:42], uint16(internalPort))
	binary.BigEndian.PutUint16(req[42:44], uint16(extPortHint))
	// req[44:60] — желаемый внешний адрес, нулями уже (::ffff:0.0.0.0 при желании
	// расписать явно, но всё поле итак нулевое) = без предпочтения.
	return req
}

// parsePCPMap разбирает ответ PCP MAP (60 байт). nonce обязателен к сверке ДО
// чтения остальных полей: ответ на чужой запрос (в т.ч. с виду "успешный") может
// прислать кто угодно в локальной сети, и без сверки мы приняли бы его как свой
// маппинг.
func parsePCPMap(raw []byte, wantNonce [12]byte) (netip.AddrPort, time.Duration, error) {
	if len(raw) < pcpResponseMinLen {
		return netip.AddrPort{}, 0, fmt.Errorf("pcp: короткий ответ (%d байт)", len(raw))
	}
	if raw[0] != pcpVersion || raw[1] != pcpOpcodeMapResp {
		return netip.AddrPort{}, 0, fmt.Errorf("pcp: неожиданные версия/опкод %d/%d", raw[0], raw[1])
	}

	var nonce [12]byte
	copy(nonce[:], raw[24:36])
	if nonce != wantNonce {
		return netip.AddrPort{}, 0, errors.New("pcp: ответ с чужим nonce — не наш запрос")
	}

	if code := raw[3]; code != 0 {
		return netip.AddrPort{}, 0, fmt.Errorf("pcp: роутер отказал, код результата %d", code)
	}

	lease := time.Duration(binary.BigEndian.Uint32(raw[4:8])) * time.Second
	port := binary.BigEndian.Uint16(raw[42:44])
	var addrBytes [16]byte
	copy(addrBytes[:], raw[44:60])
	ext := netip.AddrFrom16(addrBytes).Unmap()
	return netip.AddrPortFrom(ext, port), lease, nil
}

// v4InV6 — IPv4 в форме v4-in-v6 (::ffff:a.b.c.d), формат полей адреса в PCP.
func v4InV6(ip netip.Addr) [16]byte {
	var b [16]byte
	b[10] = 0xff
	b[11] = 0xff
	a4 := ip.As4()
	copy(b[12:], a4[:])
	return b
}
