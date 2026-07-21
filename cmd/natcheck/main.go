// Command natcheck — грубая диагностика связности: тип NAT через STUN, проброс
// порта на роутере и наличие нативного IPv6. Рассылается друзьям, у каждого
// свой роутер/провайдер, поэтому вывод — не теория, а числа по конкретной
// машине; формулировки вердиктов совпадают с панелью (см. internal/app),
// чтобы вывод друга можно было сопоставить с его же скриншотом.
package main

import (
	"context"
	"fmt"
	"net"
	"net/netip"
	"time"

	"github.com/sirgochki-source/lanmesh/internal/portmap"
	"github.com/sirgochki-source/lanmesh/internal/signal"
)

func main() {
	// Окно не должно закрыться раньше, чем прочитают вердикт: при двойном клике по
	// exe консоль исчезает сразу после выхода. defer срабатывает на любом return.
	defer func() {
		fmt.Print("\nНажми Enter, чтобы закрыть окно...")
		fmt.Scanln()
	}()

	// Тот же разнородный список, что и у клиента: заодно видно, какие серверы
	// вообще доступны с этой машины, а какие режет провайдер.
	servers := signal.DefaultSTUNServers

	conn, err := net.ListenUDP("udp4", &net.UDPAddr{Port: 0})
	if err != nil {
		fmt.Println("не открыть сокет:", err)
		return
	}
	defer conn.Close()
	localPort := conn.LocalAddr().(*net.UDPAddr).Port
	fmt.Printf("локальный порт: %d\n", localPort)

	var results []string
	for _, s := range servers {
		ep, err := signal.DiscoverEndpoint(conn, s)
		if err != nil {
			fmt.Printf("  %-28s -> ошибка: %v\n", s, err)
			continue
		}
		fmt.Printf("  %-28s -> %s\n", s, ep)
		results = append(results, ep)
	}

	fmt.Println()
	switch {
	case len(results) == 0:
		fmt.Println("ВЕРДИКТ: не ответил НИ ОДИН сервер — исходящий UDP режется. Внешний адрес не определить,")
		fmt.Println("         другие участники не смогут до тебя достучаться. Нужен relay.")
	case len(results) < 2:
		fmt.Println("ВЕРДИКТ: ответил только один сервер — внешний адрес есть, но тип NAT не определить.")
		fmt.Println("         Прямой P2P возможен, но не гарантирован.")
	default:
		same := true
		for _, r := range results[1:] {
			if portOf(r) != portOf(results[0]) {
				same = false
			}
		}
		if same {
			fmt.Println("ВЕРДИКТ: внешний порт одинаковый для всех (cone NAT). Hole punching, скорее всего, СРАБОТАЕТ — прямой P2P возможен.")
		} else {
			fmt.Println("ВЕРДИКТ: внешний порт РАЗНЫЙ (симметричный NAT). Прямой P2P НЕ пробьётся — нужен relay (orangepi).")
		}
	}

	// STUN-рефлекс нужен и каскаду проброса (Usable сверяет с ним внешний адрес
	// роутера, чтобы отсеять CGNAT/двойной NAT — см. internal/portmap), и просто
	// для наглядности вывода.
	var stunAddr netip.Addr
	stunDisplay := "нет данных"
	if len(results) > 0 {
		if ap, err := netip.ParseAddrPort(results[0]); err == nil {
			stunAddr = ap.Addr()
			stunDisplay = stunAddr.String()
		}
	}

	fmt.Println()
	fmt.Printf("Проброс порта на роутере (PCP/NAT-PMP/UPnP, до 5с, STUN-рефлекс: %s)...\n", stunDisplay)
	pmCtx, pmCancel := context.WithTimeout(context.Background(), 5*time.Second)
	defer pmCancel()
	mapped := false
	for m := range portmap.Run(pmCtx, localPort, stunAddr) {
		mapped = true
		// portmap.Run уже отдаёт в канал только Usable-маппинги (см. его doc) —
		// повторный вызов Usable здесь просто показывает сверку со STUN наглядно,
		// а не меняет решение.
		usable := portmap.Usable(m.External.Addr(), stunAddr)
		fmt.Printf("  хватило: %s, внешний адрес: %s\n", m.Proto, m.External)
		fmt.Printf("  ВЕРДИКТ: работает: %s (%s) — пригоден (Usable=%v относительно STUN-рефлекса).\n",
			m.External, m.Proto, usable)
	}
	if !mapped {
		// Та же формулировка, что и в панели (internal/app.PortmapStatus), — так
		// вывод natcheck сопоставим со скриншотом панели. Оговорка: если роутер
		// ответил, но адрес оказался непригоден (CGNAT/двойной NAT), сюда попадёт
		// та же фраза, что и при полном отсутствии поддержки протокола, — канал
		// portmap.Run отдаёт наружу только Usable-результаты и не различает эти
		// два случая (см. комментарий в internal/portmap.Run).
		fmt.Println("  ВЕРДИКТ: роутер не поддерживает проброс (PCP/NAT-PMP/UPnP не ответили).")
	}

	fmt.Println()
	fmt.Println("Нативный IPv6:")
	if v6 := globalIPv6Addrs(); len(v6) > 0 {
		for _, a := range v6 {
			fmt.Printf("  %s: %s\n", a.iface, a.addr)
		}
		fmt.Println("  ВЕРДИКТ: нативный IPv6 есть — для него NAT нет вовсе, проброс/relay не нужны, если у пира тоже есть IPv6.")
	} else {
		fmt.Println("  нативного IPv6 нет")
	}
}

func portOf(hostport string) string {
	_, port, err := net.SplitHostPort(hostport)
	if err != nil {
		return hostport
	}
	return port
}

// --- IPv6 --------------------------------------------------------------

// ipv6Teredo/ipv6SixToFour — туннели поверх IPv4 (RFC 4380 / RFC 3056):
// формально IsGlobalUnicast() пропускает оба, но пакет всё равно идёт через
// чужой релей с непредсказуемой задержкой, а не как обычный маршрутизируемый
// IPv6, — фильтруем явно, как это делает internal/app.isEligibleLocalIP.
// natcheck не тянет зависимость на internal/app целиком (там Wintun/DHT/
// сигналка) ради одной проверки, поэтому фильтр продублирован здесь.
var (
	ipv6Teredo    = netip.MustParsePrefix("2001:0000::/32")
	ipv6SixToFour = netip.MustParsePrefix("2002::/16")
)

// ifaceAddr — глобальный IPv6-адрес и имя интерфейса, на котором он найден.
type ifaceAddr struct {
	iface string
	addr  netip.Addr
}

// globalIPv6Addrs — маршрутизируемые IPv6-адреса по интерфейсам этой машины:
// не loopback, не link-local, не ULA (fc00::/7 — не маршрутизируется в
// интернете) и не туннели Teredo/6to4. При таком адресе NAT для IPv6 нет —
// прямой P2P по нему не требует ни STUN, ни проброса.
func globalIPv6Addrs() []ifaceAddr {
	ifaces, err := net.Interfaces()
	if err != nil {
		return nil
	}
	var out []ifaceAddr
	for _, ifc := range ifaces {
		addrs, err := ifc.Addrs()
		if err != nil {
			continue
		}
		for _, a := range addrs {
			ipnet, ok := a.(*net.IPNet)
			if !ok || ipnet.IP.To4() != nil {
				continue // это IPv4
			}
			if ipnet.IP.IsLoopback() || ipnet.IP.IsLinkLocalUnicast() ||
				!ipnet.IP.IsGlobalUnicast() || ipnet.IP.IsPrivate() {
				continue
			}
			addr, ok := netip.AddrFromSlice(ipnet.IP.To16())
			if !ok || ipv6Teredo.Contains(addr) || ipv6SixToFour.Contains(addr) {
				continue
			}
			out = append(out, ifaceAddr{iface: ifc.Name, addr: addr})
		}
	}
	return out
}
