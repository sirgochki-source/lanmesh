package portmap

import (
	"context"
	"encoding/binary"
	"fmt"
	"net"
	"net/netip"
	"time"
)

// natpmpMapper — проброс через NAT-PMP (RFC 6886), самый простой из трёх
// протоколов: два коротких запроса на UDP:5351 без TCP/XML.
type natpmpMapper struct {
	gateway netip.Addr
}

func (m *natpmpMapper) name() string { return "natpmp" }

func (m *natpmpMapper) add(ctx context.Context, localPort int) (netip.AddrPort, time.Duration, error) {
	conn, err := dialGateway(m.gateway, natpmpPort)
	if err != nil {
		return netip.AddrPort{}, 0, fmt.Errorf("natpmp: подключение к шлюзу: %w", err)
	}
	defer conn.Close()
	defer closeOnDone(ctx, conn)()

	// Внешний адрес NAT-PMP отдаёт ОТДЕЛЬНЫМ запросом (опкод 0) — ответ на
	// маппинг (опкод 129) несёт только порт и аренду.
	extIP, err := natpmpExternalAddr(conn)
	if err != nil {
		return netip.AddrPort{}, 0, fmt.Errorf("natpmp: внешний адрес: %w", err)
	}

	port, lease, err := natpmpMapUDP(conn, localPort, localPort, mappingLease)
	if err != nil {
		return netip.AddrPort{}, 0, fmt.Errorf("natpmp: маппинг: %w", err)
	}
	return netip.AddrPortFrom(extIP, port), lease, nil
}

func (m *natpmpMapper) remove(ctx context.Context, localPort int) {
	conn, err := dialGateway(m.gateway, natpmpPort)
	if err != nil {
		return
	}
	defer conn.Close()
	defer closeOnDone(ctx, conn)()

	// Аренда 0 в запросе на маппинг — команда роутеру немедленно удалить
	// мэппинг (RFC 6886 §3.3). Ответ на удаление не разбираем: это best-effort
	// уборка на выходе, а не операция, от которой ждём подтверждения.
	_, _, _ = natpmpMapUDP(conn, localPort, localPort, 0)
}

// natpmpExternalAddr — опкод 0: запрос внешнего адреса роутера.
func natpmpExternalAddr(conn *net.UDPConn) (netip.Addr, error) {
	req := []byte{0, 0} // версия 0, опкод 0
	buf := make([]byte, 32)
	n, err := sendRecv(conn, req, buf)
	if err != nil {
		return netip.Addr{}, err
	}
	return parseNATPMPExternalAddr(buf[:n])
}

// parseNATPMPExternalAddr разбирает ответ на опкод 0: версия, опкод 128, код
// результата (2), время с эпохи (4), внешний адрес (4).
func parseNATPMPExternalAddr(raw []byte) (netip.Addr, error) {
	if len(raw) < 12 {
		return netip.Addr{}, fmt.Errorf("natpmp: короткий ответ на запрос адреса (%d байт)", len(raw))
	}
	if raw[0] != 0 || raw[1] != 128 {
		return netip.Addr{}, fmt.Errorf("natpmp: неожиданные версия/опкод %d/%d в ответе на адрес", raw[0], raw[1])
	}
	if code := binary.BigEndian.Uint16(raw[2:4]); code != 0 {
		return netip.Addr{}, fmt.Errorf("natpmp: роутер отказал в адресе, код результата %d", code)
	}
	var b [4]byte
	copy(b[:], raw[8:12])
	return netip.AddrFrom4(b), nil
}

// natpmpMapUDP — опкод 1: запрос/продление/удаление (lease=0) проброса UDP.
func natpmpMapUDP(conn *net.UDPConn, internalPort, extPortHint int, lease time.Duration) (uint16, time.Duration, error) {
	req := make([]byte, 12)
	// req[0] = версия 0 (нулевое значение уже верное)
	req[1] = 1 // опкод 1 — проброс UDP
	binary.BigEndian.PutUint16(req[4:6], uint16(internalPort))
	binary.BigEndian.PutUint16(req[6:8], uint16(extPortHint))
	binary.BigEndian.PutUint32(req[8:12], uint32(lease/time.Second))

	buf := make([]byte, 32)
	n, err := sendRecv(conn, req, buf)
	if err != nil {
		return 0, 0, err
	}
	return parseNATPMPResponse(buf[:n])
}

// parseNATPMPResponse разбирает ответ на опкод 1 (маппинг создан/обновлён):
// версия, опкод 129, код результата (2), время с эпохи (4), внутренний порт (2),
// внешний порт (2), аренда в секундах (4) — итого 16 байт. Чистая функция,
// тестируется на записанных байтах без сети.
func parseNATPMPResponse(raw []byte) (uint16, time.Duration, error) {
	if len(raw) < 16 {
		return 0, 0, fmt.Errorf("natpmp: короткий ответ на маппинг (%d байт)", len(raw))
	}
	if raw[0] != 0 || raw[1] != 129 {
		return 0, 0, fmt.Errorf("natpmp: неожиданные версия/опкод %d/%d в ответе на маппинг", raw[0], raw[1])
	}
	if code := binary.BigEndian.Uint16(raw[2:4]); code != 0 {
		return 0, 0, fmt.Errorf("natpmp: роутер отказал в проброшенном порту, код результата %d", code)
	}
	port := binary.BigEndian.Uint16(raw[10:12])
	lease := time.Duration(binary.BigEndian.Uint32(raw[12:16])) * time.Second
	return port, lease, nil
}
