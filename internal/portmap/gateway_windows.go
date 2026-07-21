package portmap

// Определение IP шлюза по умолчанию — единственный системный вызов пакета.
// Вынесен в отдельный _windows-файл, чтобы вся платформо-зависимость (iphlpapi)
// была собрана в файлах с суффиксом _windows, а ядро каскада (portmap.go) и
// разбор протоколов (pcp/natpmp/upnp) оставались переносимыми.
//
// golang.org/x/sys/windows не оборачивает GetBestRoute (есть только более новый
// GetBestInterfaceEx), поэтому вызываем iphlpapi.dll сами — так же, как
// cmd/lanmesh-gui уже дёргает user32/shell32 напрямую через NewLazySystemDLL.

import (
	"errors"
	"fmt"
	"net/netip"
	"unsafe"

	"golang.org/x/sys/windows"
)

var (
	modIPHlpAPI      = windows.NewLazySystemDLL("iphlpapi.dll")
	procGetBestRoute = modIPHlpAPI.NewProc("GetBestRoute")
)

// mibIPForwardRow — MIB_IPFORWARDROW (iprtrmib.h), 14 полей по 4 байта, без
// внутреннего паддинга (все поля DWORD) — 56 байт что на x86, что на x64.
type mibIPForwardRow struct {
	dest      uint32
	mask      uint32
	policy    uint32
	nextHop   uint32
	ifIndex   uint32
	typ       uint32
	proto     uint32
	age       uint32
	nextHopAS uint32
	metric1   uint32
	metric2   uint32
	metric3   uint32
	metric4   uint32
	metric5   uint32
}

// defaultGateway определяет IP шлюза через GetBestRoute(0.0.0.0, ...): запрос
// маршрута к 0.0.0.0 в таблице маршрутизации совпадает ровно с маршрутом
// по умолчанию (единственная запись, чья (dest & mask) даёт 0 для ЛЮБОГО
// mask), так что next hop этого маршрута и есть шлюз. Проверено эмпирически на
// живой машине: результат совпал с "route print" (next hop == шлюз маршрута
// 0.0.0.0/0 с наименьшей метрикой).
//
// DWORD-поля Windows хранят адрес в порядке байт "как в сети" (младший байт
// DWORD = первый октет IP) — это НЕ тот же порядок, что используется в
// собственно UDP-пакетах PCP/NAT-PMP/UPnP (там encoding/binary.BigEndian по
// прямому смыслу сетевого порядка байт); две конвенции разные и путать их нельзя.
func defaultGateway() (netip.Addr, error) {
	var row mibIPForwardRow
	ret, _, _ := procGetBestRoute.Call(0, 0, uintptr(unsafe.Pointer(&row)))
	if ret != 0 {
		return netip.Addr{}, fmt.Errorf("portmap: GetBestRoute вернул код %d", ret)
	}
	if row.nextHop == 0 {
		return netip.Addr{}, errors.New("portmap: маршрута по умолчанию нет (узел не за роутером)")
	}
	b := [4]byte{byte(row.nextHop), byte(row.nextHop >> 8), byte(row.nextHop >> 16), byte(row.nextHop >> 24)}
	return netip.AddrFrom4(b), nil
}
