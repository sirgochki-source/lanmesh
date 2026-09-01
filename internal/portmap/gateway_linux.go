package portmap

// Определение IP шлюза по умолчанию — единственный системный вызов пакета.
// Вынесен в отдельный _linux-файл по тому же принципу, что и gateway_windows.go:
// вся платформо-зависимость собрана в файлах с суффиксом платформы, а ядро
// каскада (portmap.go) и разбор протоколов (pcp/natpmp/upnp) остаются
// переносимыми.

import (
	"bufio"
	"errors"
	"fmt"
	"io"
	"net/netip"
	"os"
	"strconv"
	"strings"
)

// procRoute — таблица маршрутов ядра в текстовом виде. rtnetlink (RTM_GETROUTE)
// дал бы то же самое втрое длиннее: IPv6-шлюз каскаду не нужен (PCP, NAT-PMP и
// UPnP говорят с роутером по IPv4), а больше ничего netlink здесь не добавляет.
const procRoute = "/proc/net/route"

// Флаги маршрута из linux/route.h.
const (
	rtfUp      = 0x1
	rtfGateway = 0x2
)

func defaultGateway() (netip.Addr, error) {
	f, err := os.Open(procRoute)
	if err != nil {
		return netip.Addr{}, fmt.Errorf("portmap: %s: %w", procRoute, err)
	}
	defer f.Close()
	return parseDefaultGateway(f)
}

// parseDefaultGateway выбирает шлюз маршрута по умолчанию из содержимого
// /proc/net/route. Формат — заголовок и строки, разделённые табуляцией:
//
//	Iface  Destination  Gateway   Flags  RefCnt  Use  Metric  Mask      MTU Window IRTT
//	eth0   00000000     0190A8C0  0003   0       0    0       00000000  0   0      0
//
// Маршрут по умолчанию — тот, у которого Destination == 0 и подняты флаги
// RTF_UP|RTF_GATEWAY. Их может быть несколько (две сетевые карты, поднятый VPN)
// — берём с наименьшей метрикой, как выбирает и само ядро.
//
// Порядок байт: ядро печатает %08X от значения В СЕТЕВОМ ПОРЯДКЕ, поэтому на
// little-endian младший байт числа — первый октет адреса. Проверено на живой
// системе: 0190A8C0 == 192.168.144.1, совпало с выводом `ip route`. Это та же
// конвенция, что у DWORD-полей в gateway_windows.go, и она НЕ совпадает с
// BigEndian из пакетов PCP/NAT-PMP/UPnP — две конвенции разные, путать нельзя.
// Целевые архитектуры (amd64, arm64) little-endian; на big-endian разбор был бы
// зеркальным.
func parseDefaultGateway(r io.Reader) (netip.Addr, error) {
	var best netip.Addr
	bestMetric := 0
	sc := bufio.NewScanner(r)
	for sc.Scan() {
		f := strings.Fields(sc.Text())
		if len(f) < 7 {
			continue // заголовок или мусор
		}
		dest, e1 := strconv.ParseUint(f[1], 16, 32)
		gw, e2 := strconv.ParseUint(f[2], 16, 32)
		flags, e3 := strconv.ParseUint(f[3], 16, 32)
		metric, e4 := strconv.Atoi(f[6])
		if e1 != nil || e2 != nil || e3 != nil || e4 != nil {
			continue
		}
		if dest != 0 || gw == 0 || flags&(rtfUp|rtfGateway) != rtfUp|rtfGateway {
			continue
		}
		if best.IsValid() && metric >= bestMetric {
			continue
		}
		best = netip.AddrFrom4([4]byte{byte(gw), byte(gw >> 8), byte(gw >> 16), byte(gw >> 24)})
		bestMetric = metric
	}
	if err := sc.Err(); err != nil {
		return netip.Addr{}, fmt.Errorf("portmap: чтение %s: %w", procRoute, err)
	}
	if !best.IsValid() {
		return netip.Addr{}, errors.New("portmap: маршрута по умолчанию нет (узел не за роутером)")
	}
	return best, nil
}
