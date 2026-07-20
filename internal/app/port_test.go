package app

import (
	"errors"
	"net"
	"testing"

	"golang.org/x/sys/windows"
)

// Первый запуск: сохранённого порта нет — берём случайный и просим сохранить.
func TestPickPortFirstRun(t *testing.T) {
	got, save := PickPort(0)
	if got < portRangeLo || got > portRangeHi {
		t.Fatalf("порт %d вне диапазона %d..%d", got, portRangeLo, portRangeHi)
	}
	if !save {
		t.Fatal("первый запуск обязан просить сохранение порта")
	}
}

// Сохранённый порт свободен — используем его и сохранять заново не нужно.
func TestPickPortReusesSaved(t *testing.T) {
	free, err := net.ListenUDP("udp4", &net.UDPAddr{Port: 0})
	if err != nil {
		t.Fatalf("сокет: %v", err)
	}
	port := free.LocalAddr().(*net.UDPAddr).Port
	free.Close()

	got, save := PickPort(port)
	if got != port {
		t.Fatalf("сохранённый порт %d не переиспользован, получили %d", port, got)
	}
	if save {
		t.Fatal("переиспользование не должно перезаписывать конфиг")
	}
}

// Сохранённый порт занят: берём другой, но конфиг НЕ трогаем — иначе второй
// экземпляр (run-node2.cmd) при каждом старте угонял бы порт у первого.
func TestPickPortBusyKeepsConfig(t *testing.T) {
	busy, err := net.ListenUDP("udp", &net.UDPAddr{Port: 0})
	if err != nil {
		t.Fatalf("сокет: %v", err)
	}
	defer busy.Close()
	port := busy.LocalAddr().(*net.UDPAddr).Port

	got, save := PickPort(port)
	if got == port {
		t.Fatalf("занятый порт %d выдан повторно", port)
	}
	if save {
		t.Fatal("при занятом порте конфиг перезаписывать нельзя")
	}
}

// Занятый порт обязан вернуть ОШИБКУ, а не тихую деградацию в udp4: иначе
// listenNode истолковал бы случайную коллизию порта (теперь, с PickPort, порт
// не всегда 0 — конфликт стал возможен) как «нет IPv6-стека» и молча оставил бы
// узел без IPv6 на весь сеанс. Проверено эмпирически (см. комментарий у
// listenNode): реальная ошибка Windows на занятый порт — windows.WSAEADDRINUSE
// (syscall.Errno(10048)), а НЕ вымышленная кросс-платформенная syscall.EADDRINUSE.
func TestListenNodeBusyPortReturnsError(t *testing.T) {
	busy, err := net.ListenUDP("udp", &net.UDPAddr{Port: 0})
	if err != nil {
		t.Fatalf("сокет: %v", err)
	}
	defer busy.Close()
	port := busy.LocalAddr().(*net.UDPAddr).Port

	conn, err := listenNode(port)
	if err == nil {
		conn.Close()
		t.Fatalf("занятый порт %d обязан вернуть ошибку, получили рабочий сокет", port)
	}
	if !errors.Is(err, windows.WSAEADDRINUSE) {
		t.Fatalf("ожидали ошибку занятого порта (WSAEADDRINUSE), получили: %v", err)
	}
}

// Порт 0 (поведение до задачи 4) обязан продолжать работать как раньше: ОС сама
// выбирает свободный порт, конфликт невозможен, фолбэк на udp4 не участвует.
func TestListenNodeZeroPortStillWorks(t *testing.T) {
	conn, err := listenNode(0)
	if err != nil {
		t.Fatalf("listenNode(0): %v", err)
	}
	conn.Close()
}
