//go:build windows

// Package tun оборачивает виртуальный сетевой адаптер Wintun: создание,
// назначение виртуального IP и чтение/запись IP-пакетов.
//
// Требует wintun.dll рядом с exe (качается с https://www.wintun.net) и запуска
// с правами администратора — создание сетевого адаптера иначе недоступно.
package tun

import (
	"crypto/sha256"
	_ "embed"
	"fmt"
	"net/netip"
	"os"
	"path/filepath"
	"sync"
	"sync/atomic"

	"github.com/sirgochki-source/lanmesh/internal/winexec"
	"golang.org/x/sys/windows"
	"golang.zx2c4.com/wintun"
)

// ringCapacity — размер кольцевого буфера Wintun (4 МБ; должен быть степенью двойки
// в диапазоне 128 КБ..64 МБ).
const ringCapacity = 0x400000

// virtualMTU — MTU виртуального адаптера. Туннель добавляет к каждому пакету
// служебные байты, и худший случай — путь ЧЕРЕЗ РЕТРАНСЛЯТОР:
//   IP(20)+UDP(8)+нонс(12)+тег(16)+заголовок кадра(17)+relay-заголовок(49) = 122.
// Прежние 1400 считались без relay-заголовка (49Б): напрямую пакет 1400 давал
// 1473Б на проводе и влезал в 1500, а через relay — 1522Б, то есть за пределом
// Ethernet. Крупные пакеты (чанки Minecraft) при этом дробились/терялись, и через
// ретранслятор соединение вроде «в сети», а по факту виснет.
// Берём 1280 — минимум IPv6 и стандарт WireGuard/Tailscale: с запасом переживает
// и relay, и мобильный LTE/CGNAT (там путь уже 1500 и фрагменты часто режут).
const virtualMTU = 1280

// Device — открытый Wintun-адаптер с активной сессией.
type Device struct {
	adapter  *wintun.Adapter
	session  wintun.Session
	readWait windows.Handle // событие «в кольце есть данные»
	name     string

	// sessMu защищает обращения к session от гонки с Close: закрывать сессию,
	// пока читатель в ней, НЕЛЬЗЯ — Wintun падает с access violation.
	sessMu sync.Mutex
	closed atomic.Bool
}

// wintunDLL — сам wintun.dll, вшитый в бинарник. Так пользователю достаточно
// одного exe: отдельную dll рядом класть не нужно.
//
//go:embed wintun.dll
var wintunDLL []byte

var (
	wintunMu   sync.Mutex
	wintunDone bool // dll на месте и совпадает по хэшу — проверять больше не нужно
)

// ensureWintun распаковывает встроенный wintun.dll РЯДОМ С EXE. Wintun-пакет грузит
// библиотеку только из папки приложения и System32 (флаги LOAD_LIBRARY_SEARCH_
// APPLICATION_DIR|SYSTEM32) и игнорирует прочие пути, поэтому кладём именно туда.
//
// Идентичность файла проверяем по SHA-256 встроенных байт, а не по размеру: чужая
// dll того же размера (папка приложения обычно доступна на запись) иначе прошла бы
// проверку и была бы загружена в процесс с правами администратора. Запись атомарна
// (временный файл в той же папке + rename), чтобы параллельный запуск не подсунул
// читателю усечённый файл. Ошибку НЕ кэшируем: временная причина (антивирус держит
// файл) не должна залипнуть до перезапуска — следующий вызов повторит попытку.
func ensureWintun() error {
	wintunMu.Lock()
	defer wintunMu.Unlock()
	if wintunDone {
		return nil
	}
	exe, err := os.Executable()
	if err != nil {
		return err
	}
	dir := filepath.Dir(exe)
	path := filepath.Join(dir, "wintun.dll")
	want := sha256.Sum256(wintunDLL)

	// Уже лежит наш файл — не трогаем (перезапись могла бы наткнуться на in-use).
	if data, err := os.ReadFile(path); err == nil && sha256.Sum256(data) == want {
		wintunDone = true
		return nil
	}

	tmp, err := os.CreateTemp(dir, "wintun-*.tmp")
	if err != nil {
		return fmt.Errorf("не удалось распаковать wintun.dll рядом с exe "+
			"(перенеси exe в папку с правом записи, напр. Загрузки): %w", err)
	}
	tmpName := tmp.Name()
	if _, err := tmp.Write(wintunDLL); err != nil {
		tmp.Close()
		os.Remove(tmpName)
		return fmt.Errorf("запись wintun.dll: %w", err)
	}
	tmp.Close()
	if err := os.Rename(tmpName, path); err != nil {
		os.Remove(tmpName)
		// Не переименовалось — возможно, файл держит параллельный экземпляр, уже
		// положивший корректную dll. Перепроверим хэш, прежде чем сдаваться.
		if data, e := os.ReadFile(path); e == nil && sha256.Sum256(data) == want {
			wintunDone = true
			return nil
		}
		return fmt.Errorf("не удалось заменить wintun.dll рядом с exe "+
			"(перенеси exe в папку с правом записи, напр. Загрузки): %w", err)
	}
	wintunDone = true
	return nil
}

// New создаёт адаптер name, назначает ему виртуальный IP ip в подсети /prefixBits
// и запускает сессию ввода-вывода.
func New(name string, ip netip.Addr, prefixBits int) (*Device, error) {
	if err := ensureWintun(); err != nil {
		return nil, err
	}

	adapter, err := wintun.CreateAdapter(name, "lanmesh", nil)
	if err != nil {
		return nil, fmt.Errorf("create wintun adapter (нужен админ + wintun.dll): %w", err)
	}

	session, err := adapter.StartSession(ringCapacity)
	if err != nil {
		adapter.Close()
		return nil, fmt.Errorf("start wintun session: %w", err)
	}

	d := &Device{
		adapter:  adapter,
		session:  session,
		readWait: session.ReadWaitEvent(),
		name:     name,
	}
	if err := d.assignIP(ip, prefixBits); err != nil {
		d.Close()
		return nil, err
	}
	if err := d.setMTU(virtualMTU); err != nil {
		d.Close()
		return nil, err
	}
	return d, nil
}

// setMTU занижает MTU адаптера под накладные расходы туннеля (см. virtualMTU).
func (d *Device) setMTU(mtu int) error {
	return winexec.Netsh("interface", "ipv4", "set", "subinterface",
		d.name, fmt.Sprintf("mtu=%d", mtu), "store=active")
}

// assignIP выставляет статический адрес на адаптере. netsh надёжнее ручной возни с
// LUID/IP Helper API и не требует лишних зависимостей. Только IPv4: virtualMTU-стек
// и maskString рассчитаны на него, а netip.Addr формально допускает и IPv6.
func (d *Device) assignIP(ip netip.Addr, prefixBits int) error {
	if !ip.Is4() {
		return fmt.Errorf("assignIP: ожидался IPv4-адрес, получен %s", ip)
	}
	return winexec.Netsh("interface", "ip", "set", "address",
		fmt.Sprintf("name=%s", d.name), "static", ip.String(), maskString(prefixBits))
}

// Read блокируется до прихода IP-пакета из ОС и копирует его в buf.
//
// Обращение к session идёт под sessMu, а ожидание события — вне лока. Так Close
// может добудиться и дождаться завершения чтения, не порождая гонку с закрытием
// сессии (иначе Wintun падает с access violation и уносит весь процесс).
func (d *Device) Read(buf []byte) (int, error) {
	for {
		if d.closed.Load() {
			return 0, os.ErrClosed
		}

		d.sessMu.Lock()
		if d.closed.Load() { // могли закрыть, пока ждали лок
			d.sessMu.Unlock()
			return 0, os.ErrClosed
		}
		packet, err := d.session.ReceivePacket()
		if err == nil {
			n := copy(buf, packet)
			d.session.ReleaseReceivePacket(packet)
			d.sessMu.Unlock()
			return n, nil
		}
		d.sessMu.Unlock()

		switch err {
		case windows.ERROR_NO_MORE_ITEMS:
			// Кольцо пусто — ждём сигнала (или пробуждения из Close) вне лока.
			windows.WaitForSingleObject(d.readWait, windows.INFINITE)
		case windows.ERROR_HANDLE_EOF:
			return 0, os.ErrClosed
		default:
			return 0, err
		}
	}
}

// Write отправляет IP-пакет в ОС (как будто он пришёл из сети).
func (d *Device) Write(pkt []byte) (int, error) {
	d.sessMu.Lock()
	defer d.sessMu.Unlock()
	if d.closed.Load() {
		return 0, os.ErrClosed
	}
	out, err := d.session.AllocateSendPacket(len(pkt))
	if err != nil {
		return 0, err
	}
	copy(out, pkt)
	d.session.SendPacket(out)
	return len(pkt), nil
}

// Name возвращает имя интерфейса.
func (d *Device) Name() string { return d.name }

// Close завершает сессию и удаляет адаптер. Безопасен при активном Read/Write:
// сначала помечаем закрытие и будим читателя (SetEvent), затем под sessMu ждём,
// пока текущее обращение к session завершится, и только после этого зовём End().
func (d *Device) Close() error {
	if d.closed.Swap(true) {
		return nil // уже закрыт
	}
	// Разбудить читателя, висящего в WaitForSingleObject: увидев closed, он выйдет.
	windows.SetEvent(d.readWait)

	d.sessMu.Lock()
	defer d.sessMu.Unlock()
	d.session.End()
	return d.adapter.Close()
}

// maskString переводит длину префикса в маску вида 255.0.0.0.
func maskString(bits int) string {
	var m [4]byte
	for i := 0; i < 4; i++ {
		if bits >= 8 {
			m[i] = 255
			bits -= 8
		} else if bits > 0 {
			m[i] = byte(0xff << (8 - bits))
			bits = 0
		}
	}
	return fmt.Sprintf("%d.%d.%d.%d", m[0], m[1], m[2], m[3])
}
