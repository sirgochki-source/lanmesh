//go:build linux

// Реализация Device поверх /dev/net/tun. Требует root или CAP_NET_ADMIN.

package tun

import (
	"errors"
	"fmt"
	"net/netip"
	"os"

	"golang.org/x/sys/unix"
)

// cloneDevice — управляющее устройство, через которое ядро выдаёт новый tun.
const cloneDevice = "/dev/net/tun"

// Device — открытый tun-интерфейс.
//
// В отличие от Wintun-версии здесь не нужны ни мьютекс, ни собственный флаг
// закрытия. Дескриптор переведён в неблокирующий режим и обёрнут в os.File,
// поэтому рантайм Go держит его в epoll: Read висит в netpoller, а Close
// штатно будит читателя, и тот получает ошибку с os.ErrClosed. С Wintun так
// нельзя — закрытие сессии под живым читателем роняет процесс, отсюда sessMu,
// closed и SetEvent в tun_windows.go.
type Device struct {
	f    *os.File
	name string
}

// New создаёт интерфейс name, назначает ему ip в подсети /prefixBits, ставит
// MTU и поднимает интерфейс.
func New(name string, ip netip.Addr, prefixBits int) (*Device, error) {
	// Только IPv4: маска считается как IPv4, а виртуальная подсеть — 25.0.0.0/8
	// (proto.VirtualPrefix). Ровно то же ограничение, что у Windows-версии.
	if !ip.Is4() {
		return nil, fmt.Errorf("tun: ожидался IPv4-адрес, получен %s", ip)
	}
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		return nil, fmt.Errorf("tun: имя %q не годится (максимум %d символов): %w",
			name, unix.IFNAMSIZ-1, err)
	}

	fd, err := unix.Open(cloneDevice, unix.O_RDWR|unix.O_CLOEXEC, 0)
	if err != nil {
		return nil, openError(err)
	}

	// IFF_NO_PI обязателен: без него ядро дописывает к каждому пакету 4 байта
	// struct tun_pi, а internal/peer ждёт сырой IP-пакет — ровно то, что отдаёт
	// Wintun. Разошлись бы молча: адаптер «поднят», трафик не ходит.
	ifr.SetUint16(unix.IFF_TUN | unix.IFF_NO_PI)
	if err := unix.IoctlIfreq(fd, unix.TUNSETIFF, ifr); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("tun: TUNSETIFF %q (нужен root или CAP_NET_ADMIN): %w", name, err)
	}
	// Дальше настраиваем по имени, которое вернуло ЯДРО, а не по запрошенному:
	// иначе ioctl'ы ушли бы в несуществующий интерфейс.
	actual := ifr.Name()

	if err := unix.SetNonblock(fd, true); err != nil {
		unix.Close(fd)
		return nil, fmt.Errorf("tun: SetNonblock: %w", err)
	}
	d := &Device{f: os.NewFile(uintptr(fd), cloneDevice), name: actual}

	if err := configure(actual, ip, prefixBits); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// configure выставляет адрес, маску и MTU и поднимает интерфейс — всё ioctl'ами
// на служебном AF_INET-сокете.
//
// Рассматривались rtnetlink и вызов утилиты `ip`. rtnetlink отклонён как ~200
// строк ручной сборки атрибутов ради IPv6-адреса и своих маршрутов, которых в
// проекте нет ни на одной платформе; `ip` — как внешняя зависимость на iproute2
// на горячем пути поднятия узла, с ошибками текстом вместо кодов.
func configure(name string, ip netip.Addr, prefixBits int) error {
	sock, err := unix.Socket(unix.AF_INET, unix.SOCK_DGRAM|unix.SOCK_CLOEXEC, 0)
	if err != nil {
		return fmt.Errorf("tun: служебный сокет: %w", err)
	}
	defer unix.Close(sock)

	set := func(req uint, what string, fill func(*unix.Ifreq) error) error {
		ifr, err := unix.NewIfreq(name)
		if err != nil {
			return err
		}
		if err := fill(ifr); err != nil {
			return fmt.Errorf("tun: %s для %s: %w", what, name, err)
		}
		if err := unix.IoctlIfreq(sock, req, ifr); err != nil {
			return fmt.Errorf("tun: не удалось задать %s на %s: %w", what, name, err)
		}
		return nil
	}

	a4 := ip.As4()
	if err := set(unix.SIOCSIFADDR, "адрес", func(ifr *unix.Ifreq) error {
		return ifr.SetInet4Addr(a4[:])
	}); err != nil {
		return err
	}
	mask := maskBytes(prefixBits)
	if err := set(unix.SIOCSIFNETMASK, "маску", func(ifr *unix.Ifreq) error {
		return ifr.SetInet4Addr(mask[:])
	}); err != nil {
		return err
	}
	if err := set(unix.SIOCSIFMTU, "MTU", func(ifr *unix.Ifreq) error {
		ifr.SetUint32(virtualMTU)
		return nil
	}); err != nil {
		return err
	}

	// Флаги читаем и ДОПОЛНЯЕМ, а не перезаписываем: ядро уже выставило свои
	// (IFF_POINTOPOINT/IFF_NOARP у tun), и затирать их нельзя.
	ifr, err := unix.NewIfreq(name)
	if err != nil {
		return err
	}
	if err := unix.IoctlIfreq(sock, unix.SIOCGIFFLAGS, ifr); err != nil {
		return fmt.Errorf("tun: чтение флагов %s: %w", name, err)
	}
	ifr.SetUint16(ifr.Uint16() | unix.IFF_UP | unix.IFF_RUNNING)
	if err := unix.IoctlIfreq(sock, unix.SIOCSIFFLAGS, ifr); err != nil {
		return fmt.Errorf("tun: не удалось поднять %s: %w", name, err)
	}
	// Связанный маршрут на подсеть (25.0.0.0/8) ядро заводит само вместе с
	// адресом на поднятом интерфейсе — отдельный RTM_NEWROUTE не нужен.
	return nil
}

// Read блокируется до прихода IP-пакета из ОС и копирует его в buf.
func (d *Device) Read(buf []byte) (int, error) { return d.f.Read(buf) }

// Write отправляет IP-пакет в ОС (как будто он пришёл из сети).
func (d *Device) Write(pkt []byte) (int, error) { return d.f.Write(pkt) }

// Name возвращает фактическое имя интерфейса — то, которое выдало ядро.
func (d *Device) Name() string { return d.name }

// Close закрывает дескриптор. Интерфейс непостоянный и исчезает вместе с ним,
// доубирать за ядром нечего. Безопасен при активном Read: рантайм разбудит
// читателя, и тот получит ошибку, оборачивающую os.ErrClosed.
func (d *Device) Close() error { return d.f.Close() }

// maskBytes переводит длину префикса в 4 байта маски (для /8 — 255.0.0.0).
func maskBytes(bits int) [4]byte {
	var m [4]byte
	for i := 0; i < 4 && bits > 0; i++ {
		if bits >= 8 {
			m[i] = 0xff
			bits -= 8
			continue
		}
		m[i] = byte(0xff << (8 - bits))
		bits = 0
	}
	return m
}

// openError переводит отказ открытия /dev/net/tun в понятную причину: нет
// модуля tun и нет прав лечатся по-разному, а сырой errno об этом не говорит.
func openError(err error) error {
	switch {
	case errors.Is(err, unix.ENOENT):
		return fmt.Errorf("tun: нет %s — модуль tun не загружен, попробуй `sudo modprobe tun`: %w",
			cloneDevice, err)
	case errors.Is(err, unix.EPERM), errors.Is(err, unix.EACCES):
		return fmt.Errorf("tun: нет прав на %s — запусти через sudo или выдай бинарнику "+
			"`sudo setcap cap_net_admin+ep`: %w", cloneDevice, err)
	}
	return fmt.Errorf("tun: открытие %s: %w", cloneDevice, err)
}
