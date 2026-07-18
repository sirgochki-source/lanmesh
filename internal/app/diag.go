package app

import (
	"net"
	"strings"

	"github.com/sirgochki-source/lanmesh/internal/signal"
)

// EnvDiag — снимок сетевого ОКРУЖЕНИЯ клиента: тип NAT и признаки того, что
// трафик перехватывает VPN/туннель. Собирается по запросу (кнопка «Проверить» или
// отправка диагностики), а не в каждом опросе состояния: проба NAT опрашивает
// несколько STUN-серверов и стоит до нескольких секунд.
//
// Зачем: клиент за full-tunnel VPN получает от STUN адрес ВЫХОДА VPN, а не свой
// реальный NAT. selfEndpoint при этом НЕ пустой — обычная проверка «нет внешнего
// адреса» молчит, и статус ложно-зелёный, хотя пробитие мертво. Здесь мы ловим
// это по типу NAT (VPN-выход почти всегда symmetric) и по тому, через какой
// интерфейс реально уходит трафик.
type EnvDiag struct {
	NATType       string   `json:"natType"` // cone|symmetric|blocked|unknown
	External      string   `json:"external"`
	StunAnswered  int      `json:"stunAnswered"`
	StunTotal     int      `json:"stunTotal"`
	HasIPv4Ext    bool     `json:"hasIPv4Ext"`
	EgressIface   string   `json:"egressIface"` // интерфейс выхода в интернет
	EgressIP      string   `json:"egressIP"`
	EgressIsVPN   bool     `json:"egressIsVPN"`   // выход идёт через VPN/туннель — STUN будет врать
	VPNAdapters   []string `json:"vpnAdapters"`   // активные VPN/туннель-адаптеры (кроме lanmesh)
	EndpointFlaps int      `json:"endpointFlaps"` // сколько раз менялся внешний адрес за сессию
	SignalSeenIP  string   `json:"signalSeenIP"`  // IP, с которого нас видит сигналка (реальный, не подделать)
	StunVsSignal  string   `json:"stunVsSignal"`  // "match" | "mismatch" | "" (нет данных)
	RelaySeenEP   string   `json:"relaySeenEP"`   // IP:port глазами релея (STUN от нашего сервера, боевой сокет)
	StunVsRelay   string   `json:"stunVsRelay"`   // "match" | "mismatch" | "" (нет данных)
	Warnings      []string `json:"warnings"`      // готовые человекочитаемые вердикты
}

// vpnNameHints — куски имён адаптеров, выдающие VPN/туннель. Эвристика по имени:
// свой lanmesh (25.x/8) исключаем отдельно, чтобы не принять его за туннель.
var vpnNameHints = []string{
	"wireguard", "wintun", "openvpn", "tap-windows", "tap-win", "tunnel",
	"radmin", "yggdrasil", "zerotier", "tailscale", "hamachi", "vpn",
	"nordlynx", "mullvad", "proton", "karing", "amnezia", "hiddify", "outline",
}

// dormantPseudoIface — спящие псевдотуннели Windows: есть почти везде, реальный
// трафик не носят. Не VPN — иначе кричали бы «туннель активен» на каждой машине.
var dormantPseudoIface = []string{"teredo", "isatap", "6to4", "ip-https"}

func looksVPN(name string) bool {
	n := strings.ToLower(name)
	for _, d := range dormantPseudoIface {
		if strings.Contains(n, d) {
			return false
		}
	}
	for _, h := range vpnNameHints {
		if strings.Contains(n, h) {
			return true
		}
	}
	return false
}

// Diagnose собирает окружение: тип NAT (мульти-STUN) + признаки перехвата трафика
// VPN/туннелем. Работает и без поднятой сети (можно проверить NAT до подключения).
func (s *Session) Diagnose() EnvDiag {
	s.mu.Lock()
	stun := append([]string(nil), s.stunServers...)
	iface := s.iface
	flaps := s.extChurn
	seenIP := s.signalSeen
	relayEP := s.relaySeen
	s.mu.Unlock()

	nat := signal.ProbeNAT(stun)
	d := EnvDiag{
		NATType:       nat.Type,
		External:      nat.External,
		StunAnswered:  nat.Answered,
		StunTotal:     nat.Total,
		HasIPv4Ext:    nat.External != "",
		EndpointFlaps: flaps,
		SignalSeenIP:  seenIP,
	}

	// Сверка: STUN и сигналка ДОЛЖНЫ видеть один и тот же внешний IP. Расходятся —
	// трафик раздваивается (split-tunnel VPN) или STUN отравлен адресом выхода VPN.
	// Сигналке верим больше: её адрес — реальный src глазами сервера, не подделать.
	if seenIP != "" && nat.External != "" {
		if stunHost := hostOnly(nat.External); stunHost != "" {
			if stunHost == seenIP {
				d.StunVsSignal = "match"
			} else {
				d.StunVsSignal = "mismatch"
			}
		}
	}

	// Сверка со STUN от РЕЛЕЯ (по IP): релей видит наш боевой UDP-сокет — тот же
	// транспорт, что и STUN и данные, поэтому улика сильнее сигнальной. Если STUN
	// молчал, но релей дал адрес — External пуст, а RelaySeenEP есть (не сверяем).
	d.RelaySeenEP = relayEP
	// Сверяем только ПУБЛИЧНЫЙ адрес от релея: приватный (hairpin — мы в одной
	// локалке с релеем) заведомо разойдётся со STUN, но это не отравление, а
	// ожидаемая NAT-петля, ложную тревогу поднимать нельзя.
	if isPublicEndpoint(relayEP) && nat.External != "" {
		if hostOnly(relayEP) == hostOnly(nat.External) {
			d.StunVsRelay = "match"
		} else {
			d.StunVsRelay = "mismatch"
		}
	}

	// Через какой интерфейс уходит трафик в интернет: UDP-"connect" к публичному
	// адресу НЕ шлёт пакетов, но заставляет ОС выбрать маршрут и вернуть source IP.
	// Если это адрес VPN-адаптера — весь трафик (включая STUN) идёт в туннель.
	d.EgressIface, d.EgressIP = egressInfo()
	if d.EgressIface != "" && looksVPN(d.EgressIface) {
		d.EgressIsVPN = true
	}

	// Активные VPN/туннель-адаптеры (кроме нашего lanmesh).
	if ifaces, err := net.Interfaces(); err == nil {
		for _, ifc := range ifaces {
			if ifc.Flags&net.FlagUp == 0 || ifc.Name == iface || isLanmeshIface(ifc) {
				continue
			}
			if looksVPN(ifc.Name) {
				d.VPNAdapters = append(d.VPNAdapters, ifc.Name)
			}
		}
	}

	d.Warnings = envWarnings(d)
	return d
}

func envWarnings(d EnvDiag) []string {
	var w []string
	switch d.NATType {
	case "blocked":
		w = append(w, "Исходящий UDP режется — ни один STUN не ответил. Тебя не пробьют, и relay под вопросом. Обычно это публичный или корпоративный Wi-Fi.")
	case "symmetric":
		w = append(w, "Симметричный NAT — прямой P2P невозможен, только через relay. Частые причины: включённый VPN, мобильный оператор (CGNAT) или строгий роутер.")
	}
	if d.EgressIsVPN {
		w = append(w, "Интернет-трафик уходит через VPN/туннель «"+d.EgressIface+"» — STUN покажет адрес VPN, а не твой реальный, и пробитие сломается (статус может быть ложно-зелёным). Выключи VPN или сделай split-tunnel: исключить lanmesh (25.0.0.0/8) и его UDP.")
	} else if len(d.VPNAdapters) > 0 {
		w = append(w, "Активны VPN/туннели: "+strings.Join(d.VPNAdapters, ", ")+". Сейчас интернет идёт мимо них, но если у кого-то не строится direct — проверь, не включён ли туннель на всю сеть.")
	}
	if !d.HasIPv4Ext && d.NATType != "blocked" {
		w = append(w, "Нет внешнего IPv4 (возможно DS-Lite или IPv6-only) — lanmesh работает по IPv4, узел будет недостижим напрямую.")
	}
	if d.EndpointFlaps >= 3 {
		w = append(w, "Внешний адрес менялся много раз за сессию (флапает) — ротация IP у VPN/оператора мешает удержать прямой путь.")
	}
	if d.StunVsSignal == "mismatch" {
		w = append(w, "STUN и сигналка видят тебя с РАЗНЫХ адресов (STUN: "+d.External+", сигналка: "+d.SignalSeenIP+") — трафик раздваивается split-туннелем VPN, а STUN, скорее всего, отравлен адресом выхода VPN. Сигналке верить: её адрес реальный.")
	}
	if d.StunVsRelay == "mismatch" {
		w = append(w, "STUN и релей видят тебя с РАЗНЫХ адресов (STUN: "+d.External+", релей: "+d.RelaySeenEP+") — публичный STUN отравлен/раздвоен путь. Релею верить: он видит твой боевой UDP-сокет напрямую.")
	}
	if isPublicEndpoint(d.RelaySeenEP) && !d.HasIPv4Ext {
		w = append(w, "Публичный STUN не ответил, но релей дал внешний адрес ("+d.RelaySeenEP+") — узел достижим через адрес от нашего сервера. Так и работаем, публичный STUN не нужен.")
	}
	return w
}

// hostOnly — IP из "ip:port"; если разобрать нельзя, вернёт строку как есть.
func hostOnly(hostport string) string {
	if h, _, err := net.SplitHostPort(hostport); err == nil {
		return h
	}
	return hostport
}

// isPublicEndpoint — истина, если "ip:port" содержит публичный (маршрутизируемый)
// IP. Приватные (RFC1918), loopback и link-local не годятся как внешний адрес:
// релей, стоящий в одной локалке с нами, видит нас через NAT-петлю по адресу
// роутера (192.168.x) — это не наш внешний адрес, пирам снаружи он бесполезен.
func isPublicEndpoint(ep string) bool {
	host, _, err := net.SplitHostPort(ep)
	if err != nil {
		return false
	}
	ip := net.ParseIP(host)
	if ip == nil {
		return false
	}
	return !ip.IsPrivate() && !ip.IsLoopback() && !ip.IsLinkLocalUnicast() && !ip.IsUnspecified()
}

// egressInfo — имя интерфейса и source-IP, через который ОС выходит в интернет.
// Пакетов не шлёт: UDP-Dial лишь выбирает маршрут.
func egressInfo() (name, ip string) {
	c, err := net.Dial("udp4", "8.8.8.8:80")
	if err != nil {
		return "", ""
	}
	defer c.Close()
	la, ok := c.LocalAddr().(*net.UDPAddr)
	if !ok {
		return "", ""
	}
	ip = la.IP.String()
	ifaces, err := net.Interfaces()
	if err != nil {
		return "", ip
	}
	for _, ifc := range ifaces {
		addrs, _ := ifc.Addrs()
		for _, a := range addrs {
			if n, ok := a.(*net.IPNet); ok && n.IP.Equal(la.IP) {
				return ifc.Name, ip
			}
		}
	}
	return "", ip
}

// orDash — значение либо «—», если пусто (для читаемости снимка).
func orDash(s string) string {
	if s == "" {
		return "—"
	}
	return s
}

// isLanmeshIface — наш виртуальный адаптер (адрес 25.x/8), чтобы не счесть его VPN.
func isLanmeshIface(ifc net.Interface) bool {
	addrs, _ := ifc.Addrs()
	for _, a := range addrs {
		if n, ok := a.(*net.IPNet); ok {
			if ip4 := n.IP.To4(); ip4 != nil && ip4[0] == 25 {
				return true
			}
		}
	}
	return false
}
