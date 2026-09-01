//go:build linux

package tun

import (
	"errors"
	"net/netip"
	"os"
	"os/exec"
	"strings"
	"testing"
	"time"
)

// TestLiveTun поднимает НАСТОЯЩИЙ адаптер на живом ядре и проверяет то, чего не
// проверить ни сборкой, ни юнит-тестом: что интерфейс сконфигурирован (адрес,
// MTU, UP, связанный маршрут), что пакеты приходят из ОС сырыми, что Close
// будит висящего в Read читателя и что интерфейс исчезает вместе с дескриптором.
//
// Требует root: TUNSETIFF нуждается в CAP_NET_ADMIN. Без него молча скипается,
// поэтому в обычном `go test ./...` (и в CI) он безвреден. Тот же приём, что у
// internal/discovery/dhtdisc/live_test.go.
//
// Запуск: GOOS=linux go test -c ./internal/tun && sudo ./tun.test -test.run TestLiveTun -test.v
func TestLiveTun(t *testing.T) {
	if os.Geteuid() != 0 {
		t.Skip("нужен root")
	}
	d, err := New("lmprobe", netip.MustParseAddr("25.1.2.3"), 8)
	if err != nil {
		t.Fatalf("New: %v", err)
	}
	t.Logf("поднят интерфейс %q", d.Name())

	addr := run(t, "ip", "addr", "show", d.Name())
	t.Logf("ip addr show %s:\n%s", d.Name(), addr)
	for _, want := range []string{"25.1.2.3/8", "mtu 1280"} {
		if !strings.Contains(addr, want) {
			t.Errorf("в выводе ip addr нет %q", want)
		}
	}
	if !strings.Contains(addr, "UP") {
		t.Errorf("интерфейс не поднят (нет UP)")
	}

	routes := run(t, "ip", "route")
	t.Logf("ip route:\n%s", routes)
	if !strings.Contains(routes, "25.0.0.0/8") {
		t.Errorf("ядро не завело связанный маршрут 25.0.0.0/8")
	}

	// Пакеты из ОС обязаны приходить СЫРЫМИ — без 4 байт struct tun_pi впереди.
	// Проверяем именно это, а не «первым придёт IPv4»: ядро само шлёт в свежий
	// интерфейс IPv6-мультикаст (MLD/router solicitation — интерфейс получает
	// link-local fe80::), и он запросто опережает наш ping. Признак включённого
	// PI — старший полубайт 0 вместо версии IP: заголовок tun_pi начинается с
	// двух нулевых байт флагов.
	pkts := make(chan []byte, 32)
	readErr := make(chan error, 1)
	go func() {
		for {
			buf := make([]byte, 2048)
			n, err := d.Read(buf)
			if err != nil {
				readErr <- err
				return
			}
			select {
			case pkts <- buf[:n]:
			default:
			}
		}
	}()

	target := netip.MustParseAddr("25.9.9.9")
	go func() { _ = exec.Command("ping", "-c", "3", "-W", "1", target.String()).Run() }()

	deadline := time.After(6 * time.Second)
	sawPing, seen := false, 0
collect:
	for !sawPing {
		select {
		case p := <-pkts:
			seen++
			ver := p[0] >> 4
			if ver != 4 && ver != 6 {
				t.Fatalf("пакет #%d начинается с 0x%02x — это не версия IP; похоже, "+
					"IFF_NO_PI не сработал и впереди лежит struct tun_pi", seen, p[0])
			}
			if ver == 4 && len(p) >= 20 {
				dst := netip.AddrFrom4([4]byte{p[16], p[17], p[18], p[19]})
				t.Logf("пакет #%d: сырой IPv4, %d байт, dst %s", seen, len(p), dst)
				if dst == target {
					sawPing = true
				}
				continue
			}
			t.Logf("пакет #%d: сырой IPv%d, %d байт (служебный трафик ядра)", seen, ver, len(p))
		case <-deadline:
			break collect
		}
	}
	if !sawPing {
		t.Errorf("ICMP к %s не дошёл до Read за 6с (всего пакетов получено: %d)", target, seen)
	}

	// Close при активном Read обязан разбудить читателя, а не подвесить его.
	if err := d.Close(); err != nil {
		t.Errorf("Close: %v", err)
	}
	select {
	case err := <-readErr:
		if !errors.Is(err, os.ErrClosed) {
			t.Errorf("после Close ждали os.ErrClosed, получили: %v", err)
		} else {
			t.Logf("Read после Close вернул os.ErrClosed — контракт соблюдён")
		}
	case <-time.After(3 * time.Second):
		t.Error("Close не разбудил читателя за 3с — Read подвис")
	}

	if out, err := exec.Command("ip", "addr", "show", d.Name()).CombinedOutput(); err == nil {
		t.Errorf("интерфейс %s пережил Close: %s", d.Name(), out)
	} else {
		t.Logf("интерфейс исчез вместе с дескриптором — уборка не нужна")
	}
}

func run(t *testing.T, name string, args ...string) string {
	t.Helper()
	out, err := exec.Command(name, args...).CombinedOutput()
	if err != nil {
		t.Fatalf("%s %v: %v (%s)", name, args, err, out)
	}
	return string(out)
}
