package app

import (
	"net"
	"net/netip"
	"testing"
)

// isEligibleLocalIP: таблица покрывает и IPv4, и IPv6 ветки отбора кандидатов
// localEndpoints — включая туннели Teredo/6to4, которые IsGlobalUnicast() не
// отсеивает, хотя они проходят СКВОЗЬ NAT (см. комментарий у isTunneledIPv6).
func TestIsEligibleLocalIP(t *testing.T) {
	cases := []struct {
		name string
		ip   string
		want bool
	}{
		{"IPv6 link-local", "fe80::1", false},
		{"IPv6 ULA", "fd00::1", false},
		{"IPv6 Teredo", "2001:0:c614:203:1c1e:3f57:fefd:e42", false},
		{"IPv6 6to4", "2002:c000:204::1", false},
		{"IPv6 нативный глобальный", "2001:db8::1", true},
		{"IPv4 обычный", "192.0.2.10", true},
		{"IPv4 наш виртуальный адаптер", "25.1.2.3", false},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			ip := net.ParseIP(c.ip)
			if ip == nil {
				t.Fatalf("net.ParseIP(%q) = nil", c.ip)
			}
			if got := isEligibleLocalIP(ip); got != c.want {
				t.Fatalf("isEligibleLocalIP(%s) = %v, ожидалось %v", c.ip, got, c.want)
			}
		})
	}
}

// isTunneledIPv6: точечная проверка границ префиксов Teredo/6to4 — включая
// первый/последний адрес каждого диапазона и соседние адреса вне него.
func TestIsTunneledIPv6Boundaries(t *testing.T) {
	cases := []struct {
		addr string
		want bool
	}{
		{"2001:0000::", true},                             // начало Teredo
		{"2001:0000:ffff:ffff:ffff:ffff:ffff:ffff", true}, // конец Teredo (/32)
		{"2001:1::1", false},                              // сразу после диапазона Teredo
		{"2002::", true},                                  // начало 6to4
		{"2002:ffff:ffff:ffff:ffff:ffff:ffff:ffff", true}, // конец 6to4 (/16)
		{"2003::1", false},                                // сразу после диапазона 6to4
		{"2001:db8::1", false},                            // документационный префикс, не туннель
	}
	for _, c := range cases {
		addr := netip.MustParseAddr(c.addr)
		if got := isTunneledIPv6(addr); got != c.want {
			t.Fatalf("isTunneledIPv6(%s) = %v, ожидалось %v", c.addr, got, c.want)
		}
	}
}

// formatEndpoint: IPv6 обязан заворачиваться в скобки, иначе приёмная сторона
// (netip.ParseAddrPort) не сможет отделить адрес от порта по последнему ':'.
func TestFormatEndpointIPv6Brackets(t *testing.T) {
	ip := net.ParseIP("2001:db8::1")
	got := formatEndpoint(ip, 25566)
	want := "[2001:db8::1]:25566"
	if got != want {
		t.Fatalf("formatEndpoint = %q, ожидалось %q", got, want)
	}
	if _, err := netip.ParseAddrPort(got); err != nil {
		t.Fatalf("netip.ParseAddrPort(%q) не смог разобрать: %v", got, err)
	}
}

// formatEndpoint: IPv4 без скобок, как и раньше.
func TestFormatEndpointIPv4NoBrackets(t *testing.T) {
	ip := net.ParseIP("192.0.2.10")
	got := formatEndpoint(ip, 25566)
	want := "192.0.2.10:25566"
	if got != want {
		t.Fatalf("formatEndpoint = %q, ожидалось %q", got, want)
	}
	if _, err := netip.ParseAddrPort(got); err != nil {
		t.Fatalf("netip.ParseAddrPort(%q) не смог разобрать: %v", got, err)
	}
}

// PortmapStatus — таблица всех веток статуса, без сети (поля выставляются
// напрямую, тот же пакет). Порядок веток важен: выключено и «узел не поднят»
// должны перебивать любые залежавшиеся portmapAddr/NoRouter от прошлого цикла.
//
// running (portmapCancel != nil) отличает «цикл реально запущен и ещё ничего не
// нашёл» от «включили галку на уже поднятом узле, цикл не стартовал» — без этого
// различения второй случай навечно показывал бы «определяем…», хотя проверять
// нечего: ждём только следующего подключения (см. PortmapStatus/SetPortMap).
func TestPortmapStatus(t *testing.T) {
	cases := []struct {
		name     string
		enabled  bool
		up       bool
		running  bool
		addr     string
		proto    string
		fwErr    string
		noRouter bool
		want     string
	}{
		{"выключено — не важно что в полях", false, true, true, "1.2.3.4:5", "upnp", "", true, "выключен"},
		{"узел не поднят", true, false, false, "", "", "", false, "узел не запущен"},
		{"цикл идёт, ни маппинга, ни отказа — ещё проверяем", true, true, true, "", "", "", false, "определяем поддержку роутером…"},
		{"включили галку на ходу — цикл ещё не стартовал", true, true, false, "", "", "", false, "включится при следующем подключении"},
		{"роутер не поддерживает", true, true, true, "", "", "", true, "роутер не поддерживает проброс (PCP/NAT-PMP/UPnP не ответили)"},
		{"работает", true, true, true, "203.0.113.5:31337", "pcp", "", false, "работает: 203.0.113.5:31337 (pcp)"},
		{"есть маппинг, но брандмауэр не пустил", true, true, true, "203.0.113.5:31337", "natpmp", "netsh: отказано", false,
			"проброшен 203.0.113.5:31337 (natpmp), но брандмауэр блокирует входящие"},
	}
	for _, c := range cases {
		t.Run(c.name, func(t *testing.T) {
			s := &Session{}
			s.portMapEnabled.Store(c.enabled)
			s.up = c.up
			if c.running {
				s.portmapCancel = func() {}
			}
			s.portmapAddr = c.addr
			s.portmapProto = c.proto
			s.portmapFwErr = c.fwErr
			s.portmapNoRouter = c.noRouter
			if got := s.PortmapStatus(); got != c.want {
				t.Fatalf("PortmapStatus() = %q, ожидалось %q", got, c.want)
			}
		})
	}
}

// clearPortmapIfCurrent: гонка Stop+AddNetwork — уборка СТАРОГО цикла (устаревшее
// поколение) не должна затирать состояние, которое уже успел выставить НОВЫЙ цикл.
func TestClearPortmapIfCurrentIgnoresStaleGeneration(t *testing.T) {
	s := &Session{}
	s.portmapGen = 2 // новый цикл уже стартовал и поднял поколение
	s.portmapAddr = "203.0.113.5:31337"
	s.portmapProto = "upnp"
	s.portmapCancel = func() {}

	s.clearPortmapIfCurrent(1) // уборка старого (поколения 1) прилетела с опозданием

	if s.portmapAddr != "203.0.113.5:31337" || s.portmapProto != "upnp" {
		t.Fatalf("устаревшая уборка затёрла состояние нового цикла: addr=%q proto=%q", s.portmapAddr, s.portmapProto)
	}
	if s.portmapCancel == nil {
		t.Fatal("устаревшая уборка обнулила cancel нового цикла")
	}
}

// clearPortmapIfCurrent: когда поколение СОВПАДАЕТ (обычный путь — цикл сам
// себя убирает), состояние обязано очиститься.
func TestClearPortmapIfCurrentClearsOwnGeneration(t *testing.T) {
	s := &Session{}
	s.portmapGen = 1
	s.portmapAddr = "203.0.113.5:31337"
	s.portmapCancel = func() {}

	s.clearPortmapIfCurrent(1)

	if s.portmapAddr != "" || s.portmapCancel != nil {
		t.Fatalf("своё поколение обязано очистить состояние: addr=%q cancel!=nil=%v", s.portmapAddr, s.portmapCancel != nil)
	}
}

// SetPortMap: выключение обязано отменить текущий цикл (звать сохранённый
// cancel) РОВНО один раз — повторное выключение (уже выключено) не должно
// вызывать cancel снова, а включение вовсе не трогает cancel (см. комментарий
// у SetPortMap — живое включение сознательно не поддерживается).
func TestSetPortMapCancelsOnceOnDisable(t *testing.T) {
	s := &Session{}
	s.portMapEnabled.Store(true)
	calls := 0
	s.portmapCancel = func() { calls++ }

	s.SetPortMap(false)
	if calls != 1 {
		t.Fatalf("первое выключение: cancel вызван %d раз, ожидался 1", calls)
	}

	s.SetPortMap(false)
	if calls != 1 {
		t.Fatalf("повторное выключение (уже выключено): cancel вызван ещё раз, итого %d", calls)
	}

	s.SetPortMap(true)
	if calls != 1 {
		t.Fatalf("включение не должно звать cancel: итого %d вызовов", calls)
	}
}
