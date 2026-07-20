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
